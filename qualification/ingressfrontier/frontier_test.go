//go:build linux && cgo

// This package is an executable TEST-ONLY ingress experiment. Nothing imports
// it at runtime. It provides no native fence, release permit or deployment path.
package ingressfrontier

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var blocked = errors.New("ingress experiment blocked")

type journal struct{ db *sql.DB }
type intent struct {
	ID       string `json:"id"`
	Epoch    int64  `json:"epoch"`
	Resource string `json:"resource"`
	Units    int64  `json:"units"`
}

func openJournal(t testing.TB, file string, create bool) *journal {
	t.Helper()
	if create {
		f, e := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		f.Close()
	}
	db, e := sql.Open("sqlite3", "file:"+file+"?mode=rw&_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_txlock=immediate")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(4)
	j := &journal{db}
	t.Cleanup(func() { db.Close() })
	if create {
		_, e = db.Exec(`CREATE TABLE authority(epoch INTEGER NOT NULL, sealed INTEGER NOT NULL CHECK(sealed IN(0,1)), frontier INTEGER NOT NULL); INSERT INTO authority VALUES(1,0,0);
 CREATE TABLE requests(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,epoch INTEGER NOT NULL,resource TEXT NOT NULL,units INTEGER NOT NULL CHECK(units>0),state TEXT NOT NULL CHECK(state IN('accepted','attempted','response_observed')));
 CREATE TRIGGER immutable_identity BEFORE UPDATE OF id,epoch,resource,units ON requests BEGIN SELECT RAISE(ABORT,'immutable'); END;
 CREATE TRIGGER retain_charge BEFORE DELETE ON requests BEGIN SELECT RAISE(ABORT,'retained'); END;`)
		if e != nil {
			t.Fatal(e)
		}
	}
	return j
}
func (j *journal) accept(in intent) error {
	if in.ID == "" || in.Resource == "" || in.Units <= 0 {
		return blocked
	}
	tx, e := j.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var epoch, sealed int64
	if e = tx.QueryRow("SELECT epoch,sealed FROM authority").Scan(&epoch, &sealed); e != nil {
		return e
	}
	if sealed != 0 || epoch != in.Epoch {
		return blocked
	}
	if _, e = tx.Exec("INSERT INTO requests(id,epoch,resource,units,state) VALUES(?,?,?,?,'accepted')", in.ID, in.Epoch, in.Resource, in.Units); e != nil {
		return e
	}
	return tx.Commit()
}
func (j *journal) attempt(id string) error {
	// Durable before the network call. Process death after this commit leaves an
	// unresolved attempt and MUST NOT cause replay from a restored/opened journal.
	r, e := j.db.Exec("UPDATE requests SET state='attempted' WHERE id=? AND state='accepted'", id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return blocked
	}
	return nil
}
func (j *journal) response(id string) error {
	// A response is an observation, not native terminality or physical absence.
	r, e := j.db.Exec("UPDATE requests SET state='response_observed' WHERE id=? AND state='attempted'", id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return blocked
	}
	return nil
}
func (j *journal) seal() (int64, error) {
	tx, e := j.db.Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var f int64
	if e = tx.QueryRow("SELECT COALESCE(MAX(seq),0) FROM requests").Scan(&f); e != nil {
		return 0, e
	}
	if _, e = tx.Exec("UPDATE authority SET sealed=1,epoch=epoch+1,frontier=?", f); e != nil {
		return 0, e
	}
	return f, tx.Commit()
}
func (j *journal) facts(t testing.TB) (int64, int64) {
	t.Helper()
	var n, units int64
	if e := j.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(units),0) FROM requests").Scan(&n, &units); e != nil {
		t.Fatal(e)
	}
	return n, units
}

type proxy struct {
	journal       *journal
	upstream      string
	client        *http.Client
	leaf          [32]byte
	beforeAttempt func()
	beforeDecode  func()
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/create" {
		http.NotFound(w, r)
		return
	}
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || sha256.Sum256(r.TLS.PeerCertificates[0].Raw) != p.leaf {
		http.Error(w, "denied", 403)
		return
	}
	if p.beforeDecode != nil {
		p.beforeDecode()
	}
	var in intent
	dec := json.NewDecoder(io.LimitReader(r.Body, 1025))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&in); e != nil {
		http.Error(w, "invalid", 400)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, "invalid", 400)
		return
	}
	if e := p.journal.accept(in); e != nil {
		http.Error(w, "blocked", 409)
		return
	}
	if p.beforeAttempt != nil {
		p.beforeAttempt()
	}
	if e := p.journal.attempt(in.ID); e != nil {
		http.Error(w, "unknown", 503)
		return
	}
	// Client cancellation is not represented as cancellation of accepted work.
	// The fixture has a bounded client timeout but timeout never deletes its charge.
	raw, _ := json.Marshal(in)
	req, e := http.NewRequestWithContext(context.Background(), "POST", p.upstream, bytes.NewReader(raw))
	if e != nil {
		http.Error(w, "unknown", 503)
		return
	}
	res, e := p.client.Do(req)
	if e != nil {
		http.Error(w, "unknown", 503)
		return
	}
	defer res.Body.Close()
	_, e = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if e != nil || res.StatusCode != 200 {
		http.Error(w, "unknown", 503)
		return
	}
	if e = p.journal.response(in.ID); e != nil {
		http.Error(w, "unknown", 503)
		return
	}
	w.WriteHeader(202)
}

func mtls(t testing.TB, h http.Handler) (*httptest.Server, *http.Client, [32]byte) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	ca, e = x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	pair := func(n int64, usage x509.ExtKeyUsage) tls.Certificate {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		c := &x509.Certificate{SerialNumber: big.NewInt(n), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		d, e := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, key)
		if e != nil {
			t.Fatal(e)
		}
		return tls.Certificate{Certificate: [][]byte{d, der}, PrivateKey: k}
	}
	server, client := pair(2, x509.ExtKeyUsageServerAuth), pair(3, x509.ExtKeyUsageClientAuth)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	s := httptest.NewUnstartedServer(h)
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{server}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}
	s.StartTLS()
	t.Cleanup(s.Close)
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{client}, MinVersion: tls.VersionTLS13}}
	t.Cleanup(tr.CloseIdleConnections)
	return s, &http.Client{Transport: tr, Timeout: 5 * time.Second}, sha256.Sum256(client.Certificate[0])
}
func post(t testing.TB, c *http.Client, url string, in intent) int {
	t.Helper()
	raw, _ := json.Marshal(in)
	r, e := c.Post(url, "application/json", bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	return r.StatusCode
}

func TestAcceptedBeforeSealMayWriteLaterButRemainsDurableAndCharged(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "journal.db"), true)
	var nativeWrites atomic.Int64
	native, nc, _ := mtls(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nativeWrites.Add(1); w.WriteHeader(200) }))
	accepted, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseHeld := func() { once.Do(func() { close(release) }) }
	p := &proxy{journal: j, upstream: native.URL, client: nc, beforeAttempt: func() { close(accepted); <-release }}
	s, c, leaf := mtls(t, p)
	p.leaf = leaf
	t.Cleanup(releaseHeld)
	result := make(chan int, 1)
	go func() { result <- post(t, c, s.URL+"/create", intent{"one", 1, "resource-a", 4}) }()
	receive(t, accepted)
	f, e := j.seal()
	if e != nil || f != 1 {
		t.Fatalf("%d %v", f, e)
	}
	if nativeWrites.Load() != 0 {
		t.Fatal("fixture already wrote")
	}
	// Even two empty inventories here would not revoke the accepted callback.
	if n, u := j.facts(t); n != 1 || u != 4 {
		t.Fatal(n, u)
	}
	if got := post(t, c, s.URL+"/create", intent{"two", 1, "resource-b", 4}); got != 409 {
		t.Fatal(got)
	}
	releaseHeld()
	if receive(t, result) != 202 || nativeWrites.Load() != 1 {
		t.Fatal("late accepted write not observed")
	}
	if n, u := j.facts(t); n != 1 || u != 4 {
		t.Fatal("response released charge", n, u)
	}
}

func TestLostNativeACKNeverReplaysAndStopNewIsNotFenceComplete(t *testing.T) {
	file := filepath.Join(t.TempDir(), "journal.db")
	j := openJournal(t, file, true)
	var writes atomic.Int64
	native, nc, _ := mtls(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		h := w.(http.Hijacker)
		conn, _, e := h.Hijack()
		if e != nil {
			t.Fatal(e)
		}
		conn.Close()
	}))
	p := &proxy{journal: j, upstream: native.URL, client: nc}
	s, c, leaf := mtls(t, p)
	p.leaf = leaf
	in := intent{"lost", 1, "resource", 2}
	if post(t, c, s.URL+"/create", in) != 503 {
		t.Fatal("unknown not retained")
	}
	if post(t, c, s.URL+"/create", in) != 409 || writes.Load() != 1 {
		t.Fatal("duplicate native attempt")
	}
	if _, e := j.seal(); e != nil {
		t.Fatal(e)
	}
	j.db.Close()
	reopened := openJournal(t, file, false)
	if reopened.accept(intent{"new", 2, "new", 2}) == nil || reopened.attempt("lost") == nil {
		t.Fatal("restart revived authority")
	}
	if n, u := reopened.facts(t); n != 1 || u != 2 {
		t.Fatal(n, u)
	}
}

func TestConcurrentAcceptAndSealHaveAnExactDurableFrontier(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "journal.db"), true)
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if j.accept(intent{strconv.Itoa(i), 1, "r", 1}) == nil {
				accepted.Add(1)
			}
		}(i)
	}
	f, e := j.seal()
	if e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	n, u := j.facts(t)
	if f != n || n != u || n != accepted.Load() {
		t.Fatal("frontier crossed", f, n, u, accepted.Load())
	}
	if j.accept(intent{"after", 2, "r", 1}) == nil {
		t.Fatal("sealed accepted new request")
	}
}

// Real subprocess exits exercise durable boundaries. It never executes a native
// binary or a live service: the only remote address is the parent's httptest URL.
func TestCrashHelper(t *testing.T) {
	if os.Getenv("GCC_FRONTIER_HELPER") != "1" {
		return
	}
	file := os.Getenv("GCC_FRONTIER_DB")
	j := openJournal(t, file, false)
	if e := j.accept(intent{"crashed", 1, "resource", 3}); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("GCC_FRONTIER_STAGE") == "accepted" {
		os.Exit(19)
	}
	if e := j.attempt("crashed"); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("GCC_FRONTIER_STAGE") == "attempted" {
		os.Exit(19)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	r, e := client.Post(os.Getenv("GCC_FRONTIER_URL"), "application/json", bytes.NewBufferString("{}"))
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	os.Exit(19) // reply observed but not durably recorded
}
func TestProcessCrashBoundariesRetainIntentAndForbidAutomaticRetry(t *testing.T) {
	for _, stage := range []string{"accepted", "attempted", "response"} {
		t.Run(stage, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "journal.db")
			j := openJournal(t, file, true)
			j.db.Close()
			var writes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writes.Add(1); w.WriteHeader(200) }))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashHelper$")
			cmd.Env = []string{"GCC_FRONTIER_HELPER=1", "GCC_FRONTIER_DB=" + file, "GCC_FRONTIER_STAGE=" + stage, "GCC_FRONTIER_URL=" + server.URL}
			out, e := cmd.CombinedOutput()
			var ex *exec.ExitError
			if !errors.As(e, &ex) || ex.ExitCode() != 19 {
				t.Fatalf("helper: %v %s", e, out)
			}
			reopened := openJournal(t, file, false)
			if n, u := reopened.facts(t); n != 1 || u != 3 {
				t.Fatal(n, u)
			}
			if reopened.accept(intent{"crashed", 1, "resource", 3}) == nil {
				t.Fatal("crashed request reaccepted")
			}
			// We deliberately do not automatically dispatch accepted-only records. A
			// production recovery adapter must distinguish lost acceptance ACKs safely.
			expected := int64(0)
			if stage == "response" {
				expected = 1
			}
			if writes.Load() != expected {
				t.Fatal(writes.Load(), expected)
			}
			var state string
			reopened.db.QueryRow("SELECT state FROM requests").Scan(&state)
			want := "attempted"
			if stage == "accepted" {
				want = "accepted"
			}
			if state != want {
				t.Fatal(state, want)
			}
		})
	}
}

func TestBypassAndIndependentJournalsAreExplicitCounterexamples(t *testing.T) {
	dir := t.TempDir()
	a := openJournal(t, filepath.Join(dir, "a.db"), true)
	b := openJournal(t, filepath.Join(dir, "b.db"), true)
	if e := a.accept(intent{"a", 1, "same-physical-parent", 4}); e != nil {
		t.Fatal(e)
	}
	if e := b.accept(intent{"b", 1, "same-physical-parent", 4}); e != nil {
		t.Fatal(e)
	}
	_, ua := a.facts(t)
	_, ub := b.facts(t)
	if ua+ub != 8 {
		t.Fatal("independent domains no longer demonstrate gap")
	}
	// The journal does not measure foreign projects or enforce native limits.
	var writes atomic.Int64
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writes.Add(1) }))
	defer native.Close()
	if _, e := a.seal(); e != nil {
		t.Fatal(e)
	}
	r, e := http.Post(native.URL, "application/json", bytes.NewBufferString("{}"))
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if writes.Load() != 1 {
		t.Fatal("fixture must demonstrate unmediated native path")
	}
}

func TestNoDeleteOrIdentityRewriteInJournal(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "journal.db"), true)
	if e := j.accept(intent{"id", 1, "resource", 2}); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{"DELETE FROM requests", "UPDATE requests SET units=0", "UPDATE requests SET resource='replacement'"} {
		if _, e := j.db.Exec(q); e == nil {
			t.Fatal("mutated retained evidence", q)
		}
	}
	if n, u := j.facts(t); n != 1 || u != 2 {
		t.Fatal(n, u)
	}
}

func TestSealRejectsRequestsOnAlreadyAuthenticatedPooledConnection(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "journal.db"), true)
	var writes atomic.Int64
	native, nc, _ := mtls(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writes.Add(1); w.WriteHeader(200) }))
	p := &proxy{journal: j, upstream: native.URL, client: nc}
	s, c, leaf := mtls(t, p)
	p.leaf = leaf
	if post(t, c, s.URL+"/create", intent{"first", 1, "r1", 1}) != 202 {
		t.Fatal("fixture")
	}
	if _, e := j.seal(); e != nil {
		t.Fatal(e)
	}
	reused := false
	raw, _ := json.Marshal(intent{"second", 1, "r2", 1})
	req, e := http.NewRequest("POST", s.URL+"/create", bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	res, e := c.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if !reused || res.StatusCode != 409 || writes.Load() != 1 {
		t.Fatal("connection authentication mistaken for request authority", reused, res.StatusCode, writes.Load())
	}
}
func TestConsistentOldSnapshotCannotServeAsIndependentEpochAnchor(t *testing.T) {
	dir := t.TempDir()
	original := openJournal(t, filepath.Join(dir, "active.db"), true)
	snapshot := filepath.Join(dir, "old.db")
	// SQLite creates a consistent snapshot; no main-file/WAL copying is used.
	if _, e := original.db.Exec("VACUUM INTO ?", snapshot); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(snapshot, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := original.seal(); e != nil {
		t.Fatal(e)
	}
	old := openJournal(t, snapshot, false)
	if old.accept(intent{"old-writer", 1, "same-domain", 1}) != nil {
		t.Fatal("fixture must expose absent external monotonic anchor")
	}
	if original.accept(intent{"new", 1, "same-domain", 1}) == nil {
		t.Fatal("original seal failed")
	}
}
func TestUntrustedClientCannotReachAcceptance(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "journal.db"), true)
	p := &proxy{journal: j}
	s, c, _ := mtls(t, p) // verified TLS chain, intentionally no granted leaf
	if post(t, c, s.URL+"/create", intent{"ungranted", 1, "r", 1}) != 403 {
		t.Fatal("ungranted source accepted")
	}
	if n, u := j.facts(t); n != 0 || u != 0 {
		t.Fatal(n, u)
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(8 * time.Second):
		t.Fatal("fixture wait exceeded bound")
		var zero T
		return zero
	}
}

func TestNativePreOperationHandlerSurvivesEmptyListsAndIngressSeal(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "journal.db"), true)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var writes, reads atomic.Int64
	native, nc, _ := mtls(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && (r.URL.Path == "/1.0/instances" || r.URL.Path == "/1.0/operations") {
			reads.Add(1)
			io.WriteString(w, "[]")
			return
		}
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		close(entered)
		<-release
		writes.Add(1)
		w.WriteHeader(200)
	}))
	p := &proxy{journal: j, upstream: native.URL, client: nc}
	s, c, leaf := mtls(t, p)
	p.leaf = leaf
	t.Cleanup(unblock)
	result := make(chan int, 1)
	go func() { result <- post(t, c, s.URL+"/create", intent{"native-accepted", 1, "r", 2}) }()
	receive(t, entered)
	if _, e := j.seal(); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		for _, route := range []string{"/1.0/instances", "/1.0/operations"} {
			res, e := nc.Get(native.URL + route)
			if e != nil {
				t.Fatal(e)
			}
			body, e := io.ReadAll(res.Body)
			res.Body.Close()
			if e != nil || string(body) != "[]" {
				t.Fatal(e, string(body))
			}
		}
	}
	if writes.Load() != 0 || reads.Load() != 4 {
		t.Fatal("fixture order")
	}
	if n, u := j.facts(t); n != 1 || u != 2 {
		t.Fatal("empty reads discarded accepted request", n, u)
	}
	unblock()
	if receive(t, result) != 202 || writes.Load() != 1 {
		t.Fatal("pre-operation write disappeared")
	}
	if n, u := j.facts(t); n != 1 || u != 2 {
		t.Fatal(n, u)
	}
}

func TestRequestStartedBeforeSealButNotYetAcceptedIsDenied(t *testing.T) {
	j := openJournal(t, filepath.Join(t.TempDir(), "journal.db"), true)
	var writes atomic.Int64
	native, nc, _ := mtls(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writes.Add(1); w.WriteHeader(200) }))
	entered := make(chan struct{})
	p := &proxy{journal: j, upstream: native.URL, client: nc, beforeDecode: func() { close(entered) }}
	s, c, leaf := mtls(t, p)
	p.leaf = leaf
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	result := make(chan int, 1)
	go func() {
		res, e := c.Post(s.URL+"/create", "application/json", reader)
		if e != nil {
			result <- 0
			return
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		result <- res.StatusCode
	}()
	if _, e := writer.Write([]byte(`{"id":"queued",`)); e != nil {
		t.Fatal(e)
	}
	receive(t, entered)
	if _, e := j.seal(); e != nil {
		t.Fatal(e)
	}
	if _, e := writer.Write([]byte(`"epoch":1,"resource":"r","units":1}`)); e != nil {
		t.Fatal(e)
	}
	writer.Close()
	if receive(t, result) != 409 || writes.Load() != 0 {
		t.Fatal("preaccept handler crossed sealed frontier")
	}
	if n, u := j.facts(t); n != 0 || u != 0 {
		t.Fatal(n, u)
	}
}
