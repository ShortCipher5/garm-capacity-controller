package managedread

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
)

const controllerID = "11111111-1111-4111-8111-111111111111"
const poolID = "22222222-2222-4222-8222-222222222222"
const repoID = "33333333-3333-4333-8333-333333333333"
const foreignID = "44444444-4444-4444-8444-444444444444"

type sourceFixture struct {
	calls, reads        atomic.Int32
	drift, changeOnRead atomic.Bool
}

func (s *sourceFixture) Identity() string {
	s.calls.Add(1)
	if s.drift.Load() {
		return ""
	}
	return strings.Repeat("a", 64)
}
func (s *sourceFixture) ControllerID() string { s.calls.Add(1); return controllerID }
func (s *sourceFixture) read(raw string) ([]byte, error) {
	s.reads.Add(1)
	if s.changeOnRead.Load() {
		s.drift.Store(true)
	}
	return []byte(raw), nil
}
func (s *sourceFixture) Instances(context.Context) ([]byte, error) { return s.read(`[]`) }
func (s *sourceFixture) Pools(context.Context) ([]byte, error) {
	return s.read(`[{"id":"` + poolID + `","provider_name":"owned","repo_id":"` + repoID + `","generation":1,"max_runners":4,"enabled":true,"tags":[{"name":"linux","secret":"hidden"}],"secret":"hidden"},{"id":"` + foreignID + `","provider_name":"foreign","repo_id":"` + repoID + `"}]`)
}
func (s *sourceFixture) Jobs(context.Context) ([]byte, error) {
	return s.read(`[{"workflow_job_id":1,"repo_id":"` + repoID + `","status":"queued","labels":["linux"],"created_at":"` + time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) + `","locked_by":"00000000-0000-0000-0000-000000000000","secret":"hidden"},{"repo_id":"` + foreignID + `","secret":"hidden"}]`)
}
func (s *sourceFixture) Pool(_ context.Context, id string) ([]byte, error) {
	return s.read(`{"id":"` + id + `","provider_name":"owned","repo_id":"` + repoID + `","generation":1,"max_runners":4,"enabled":true,"tags":[],"secret":"hidden"}`)
}

func tlsFixture(t *testing.T) (tls.Certificate, tls.Certificate, tls.Certificate, *x509.CertPool) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(parent)
	leaf := func(serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		p, k, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		d, e := x509.CreateCertificate(rand.Reader, template, parent, p, key)
		if e != nil {
			t.Fatal(e)
		}
		b, e := x509.MarshalPKCS8PrivateKey(k)
		if e != nil {
			t.Fatal(e)
		}
		pair, e := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
		if e != nil {
			t.Fatal(e)
		}
		return pair
	}
	return leaf(2, x509.ExtKeyUsageServerAuth), leaf(3, x509.ExtKeyUsageClientAuth), leaf(4, x509.ExtKeyUsageClientAuth), roots
}
func fingerprint(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }

func TestActualManagedClientsOverPinnedMTLS(t *testing.T) {
	serverKey, clientKey, wrongKey, roots := tlsFixture(t)
	source := &sourceFixture{}
	h, err := New(source, fingerprint(clientKey.Certificate[0]), []string{"owned"}, []string{poolID}, []string{repoID})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(h)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverKey}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()
	cert, _ := x509.ParseCertificate(serverKey.Certificate[0])
	pin := fingerprint(cert.RawSubjectPublicKeyInfo)
	trust := &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{clientKey}, MinVersion: tls.VersionTLS13}
	inventory, err := garmapi.NewManagedInventory(server.URL, pin, trust, []string{"owned"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer inventory.Close()
	if rows, err := inventory.Instances(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("inventory: %v", err)
	}
	demand, err := garmapi.NewManagedDemand(server.URL, pin, controllerID, trust, []string{"owned"}, []string{repoID})
	if err != nil {
		t.Fatal(err)
	}
	defer demand.Close()
	observation, err := demand.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Jobs) != 1 || observation.SourceIdentity() == "" || demand.SourceIdentity() != observation.SourceIdentity() {
		t.Fatal("missing demand provenance")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: trust}}
	defer client.CloseIdleConnections()
	res, err := client.Get(server.URL + "/api/v1/pools/" + poolID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || strings.Contains(string(raw), "hidden") || res.Header.Get(managedsource.SourceHeader) != strings.Repeat("a", 64) || res.Header.Get(managedsource.ControllerHeader) != controllerID {
		t.Fatal("unscoped pool or provenance")
	}
	// A CA-valid but different client leaf must not reach the typed source.
	before := source.reads.Load()
	wrong := trust.Clone()
	wrong.Certificates = []tls.Certificate{wrongKey}
	denied := &http.Client{Transport: &http.Transport{TLSClientConfig: wrong}}
	defer denied.CloseIdleConnections()
	res, err = denied.Get(server.URL + "/api/v1/instances")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 || source.reads.Load() != before {
		t.Fatal("foreign role reached source")
	}
	source.changeOnRead.Store(true)
	if _, err := inventory.Instances(context.Background()); err == nil {
		t.Fatal("source change accepted")
	}
}

type unreadableBody struct{ t *testing.T }

func (b unreadableBody) Read([]byte) (int, error) { b.t.Fatal("denied body read"); return 0, io.EOF }
func (b unreadableBody) Close() error             { return nil }
func TestRejectBeforeBodyOrSourceAndFreezeScope(t *testing.T) {
	_, clientKey, _, _ := tlsFixture(t)
	source := &sourceFixture{}
	providers := []string{"owned"}
	pools := []string{poolID}
	repos := []string{repoID}
	h, err := New(source, fingerprint(clientKey.Certificate[0]), providers, pools, repos)
	if err != nil {
		t.Fatal(err)
	}
	providers[0] = "foreign"
	pools[0] = foreignID
	repos[0] = foreignID
	leaf, _ := x509.ParseCertificate(clientKey.Certificate[0])
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	baseline := source.calls.Load()
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT"} {
		r := httptest.NewRequest(method, "/api/v1/instances", nil)
		r.Body = unreadableBody{t}
		r.TLS = state
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(method, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/instances?", "/api/v1/instances?x=y", "/api/v1/pools/" + foreignID, "/api/v1/pools/../jobs", "/v1/managed/settings", "/api/v1/auth/login", "/api/v1/repositories/" + repoID, "/api/v1/pools/%32"} {
		r := httptest.NewRequest("GET", path, nil)
		r.TLS = state
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatal(path)
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/instances", nil)
	r.TLS = state
	r.Body = unreadableBody{t}
	h.ServeHTTP(httptest.NewRecorder(), r)
	if source.calls.Load() != baseline || source.reads.Load() != 0 {
		t.Fatal("denied request touched source")
	}
	r = httptest.NewRequest("GET", "/api/v1/pools", nil)
	r.TLS = state
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var rows []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 || rows[0]["id"] != poolID {
		t.Fatal("scope not frozen", w.Body.String())
	}
	source.drift.Store(true)
	before := source.reads.Load()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || source.reads.Load() != before || w.Header().Get(managedsource.SourceHeader) != "" {
		t.Fatal("stale source advertised")
	}
}

func TestConfigurationBoundsAndChangedSourceCannotRevive(t *testing.T) {
	source := &sourceFixture{}
	for _, leaf := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := New(source, leaf, []string{"owned"}, nil, nil); err == nil {
			t.Fatal("invalid role accepted")
		}
	}
	for _, providers := range [][]string{nil, {"owned", "owned"}, {"/path"}} {
		if _, err := New(source, strings.Repeat("b", 64), providers, nil, nil); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	h, err := New(source, strings.Repeat("b", 64), []string{"owned"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	source.drift.Store(true)
	if h.sameSource() {
		t.Fatal("changed source accepted")
	}
	source.drift.Store(false)
	if h.sameSource() {
		t.Fatal("revoked source revived")
	}
}
