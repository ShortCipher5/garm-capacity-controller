package managedwriter

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
)

type readOnlyRemote struct {
	garmmanagement.Remote
	reads   int
	entered chan struct{}
	release chan struct{}
}

func (*readOnlyRemote) ManagementIdentity() string { return strings.Repeat("a", 64) }
func (r *readOnlyRemote) Snapshot(ctx context.Context) (garmapi.ManagementSnapshot, error) {
	r.reads++
	if r.entered != nil && r.reads == 2 {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return garmapi.ManagementSnapshot{}, ctx.Err()
		}
	}
	return garmapi.ManagementSnapshot{WritesEnabled: true}, nil
}

func TestReadOnlyDeniesMutationsBeforeBodyJournalOrUpstream(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("readonly-role")}
	// Nil manager and session deliberately make any accidental effect path fail.
	h := &Handler{readOnly: true, leaf: sha256.Sum256(cert.Raw)}
	for _, path := range []string{"/v1/managed/metadata/11111111-1111-4111-8111-111111111111/runner/credentials", "/v1/managed/pools/11111111-1111-4111-8111-111111111111/pause", "/v1/managed/pools/11111111-1111-4111-8111-111111111111/reduce-maximum", "/api/v1/pools/11111111-1111-4111-8111-111111111111", "/v1/managed/new-mutation"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			r := httptest.NewRequest(method, path, panicBody{})
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("%s %s: %d", method, path, w.Code)
			}
		}
	}
}

type panicBody struct{}

func (panicBody) Read([]byte) (int, error) { panic("denied request body read") }

func TestReadOnlyAuthenticatedSettingsRemainReadableWithoutWriteAdvertisement(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := garmmanagement.InitializeJournal(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	remote := &readOnlyRemote{}
	manager, err := garmmanagement.New(remote, j)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{Raw: []byte("readonly-role")}
	for _, readonly := range []bool{false, true} {
		h := &Handler{readOnly: readonly, leaf: sha256.Sum256(cert.Raw), manager: manager, session: &managedgarm.Session{}}
		for _, auth := range []string{"missing", "wrong", "valid"} {
			r := httptest.NewRequest("GET", "/v1/managed/settings", nil)
			if auth != "missing" {
				c := cert
				if auth == "wrong" {
					c = &x509.Certificate{Raw: []byte("other")}
				}
				r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{c}, VerifiedChains: [][]*x509.Certificate{{c}}}
			}
			before := remote.reads
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if auth != "valid" {
				if w.Code != 403 || remote.reads != before {
					t.Fatal("authentication bypass", auth, w.Code)
				}
				continue
			}
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var v garmmanagement.Snapshot
			if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.WritesEnabled == readonly {
				t.Fatal("incorrect write advertisement", w.Body.String())
			}
		}
	}
	pending, err := j.Pending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatal("read created intent", err, pending)
	}
}

func readOnlyTLSLeaf(t *testing.T, role x509.ExtKeyUsage) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{role}}
	if role == x509.ExtKeyUsageServerAuth {
		template.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1)}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

// The real read-only Handler and Service own the TLS listener. Only the
// settings backend and session placeholder are synthetic. Successful protected
// Resume requires a generated installation and existing bootstrap record.
func TestReadOnlyOverAuthenticatedTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := garmmanagement.InitializeJournal(filepath.Join(dir, "tls.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	remote := &readOnlyRemote{entered: make(chan struct{}), release: make(chan struct{})}
	manager, err := garmmanagement.New(remote, j)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, clientRoots := readOnlyTLSLeaf(t, x509.ExtKeyUsageClientAuth)
	serverCert, serverRoots := readOnlyTLSLeaf(t, x509.ExtKeyUsageServerAuth)
	h := &Handler{readOnly: true, leaf: sha256.Sum256(clientCert.Certificate[0]), manager: manager, session: &managedgarm.Session{}}
	service := &Service{options: Options{ReadOnly: true}, journal: j, terminal: make(chan error, 1),
		trust: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots}}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service.serve(listener, h.session, h)
	defer service.Close()
	url := "https://" + listener.Addr().String()
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: serverRoots, Certificates: []tls.Certificate{clientCert}}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	response, err := client.Get(url + "/v1/managed/settings")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(body), `"writes_enabled":false`) {
		t.Fatal(response.StatusCode, string(body))
	}
	response, err = client.Post(url+"/v1/managed/pools/pool/pause", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal(response.StatusCode)
	}
	unauthTransport := transport.Clone()
	unauthTransport.TLSClientConfig.Certificates = nil
	defer unauthTransport.CloseIdleConnections()
	unauth := &http.Client{Transport: unauthTransport, Timeout: 5 * time.Second}
	if response, err = unauth.Get(url + "/v1/managed/settings"); err == nil {
		response.Body.Close()
		t.Fatal("missing TLS client identity accepted")
	}
	if remote.reads != 1 {
		t.Fatal("denied request reached stock backend", remote.reads)
	}
	lineage := j.Lineage()
	pending, err := j.Pending(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("unexpected initial intents: %v %v", pending, err)
	}
	done := make(chan error, 1)
	go func() {
		res, err := client.Get(url + "/v1/managed/settings")
		if err == nil {
			defer res.Body.Close()
			var got garmmanagement.Snapshot
			if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&got) != nil || got.WritesEnabled {
				err = errors.New("drained GET failed to return read-only settings")
			}
		}
		done <- err
	}()
	select {
	case <-remote.entered:
	case <-ctx.Done():
		t.Fatal("authenticated settings handler failed to enter")
	}
	expiring, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	err = service.Suspend(expiring)
	stop()
	if err == nil || service.stopped || service.session != h.session {
		t.Fatal("active read was reported as drained")
	}
	close(remote.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("entered read failed to finish")
	}
	if err := service.Suspend(ctx); err != nil || !service.stopped || service.session != nil || service.server != nil {
		t.Fatal("service failed to drain and clear old session", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if service.Resume(canceled) == nil || !service.stopped || service.server != nil || service.session != nil {
		t.Fatal("canceled resume published a listener or session")
	}
	if response, err := client.Get(url + "/v1/managed/settings"); err == nil {
		response.Body.Close()
		t.Fatal("old listener accepted another request")
	}
	pending, err = service.journal.Pending(ctx)
	if err != nil || len(pending) != 0 || service.journal.Lineage() != lineage || !service.options.ReadOnly || remote.reads != 2 {
		t.Fatal("suspension or canceled resume changed the protected read-only state", err)
	}
}

func TestDefaultMetadataRouteRetainsExistingScopeValidation(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("metadata-role")}
	h := &Handler{leaf: sha256.Sum256(cert.Raw)}
	r := httptest.NewRequest("POST", "/v1/managed/metadata/unconfigured/runner/credentials", panicBody{})
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	// Default mode still enters the existing metadata scope validator; it must not
	// be globally disabled or bypass that validator to reach a missing session.
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
