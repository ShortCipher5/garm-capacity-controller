package managedwriter

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"math/big"
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
	reads int
}

func (*readOnlyRemote) ManagementIdentity() string { return strings.Repeat("a", 64) }
func (r *readOnlyRemote) Snapshot(context.Context) (garmapi.ManagementSnapshot, error) {
	r.reads++
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

// Real TLS exercises the same verified-chain/leaf requirement used by ServeHTTP.
// The stock backend remains a bounded fake; no host/session is minted by this test.
func TestReadOnlyOverAuthenticatedTLS(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
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
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := garmmanagement.InitializeJournal(filepath.Join(dir, "tls.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	remote := &readOnlyRemote{}
	manager, err := garmmanagement.New(remote, j)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{readOnly: true, leaf: sha256.Sum256(der), manager: manager, session: &managedgarm.Session{}}
	server := httptest.NewUnstartedServer(h)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}
	client.Transport = transport
	defer transport.CloseIdleConnections()
	response, err := client.Get(server.URL + "/v1/managed/settings")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(body), `"writes_enabled":false`) {
		t.Fatal(response.StatusCode, string(body))
	}
	response, err = client.Post(server.URL+"/v1/managed/pools/pool/pause", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal(response.StatusCode)
	}
	unauth := server.Client()
	unauth.Transport = server.Client().Transport.(*http.Transport).Clone()
	// Remove the client certificate to exercise real TLS authentication refusal.
	unauth.Transport.(*http.Transport).TLSClientConfig = transport.TLSClientConfig.Clone()
	unauth.Transport.(*http.Transport).TLSClientConfig.Certificates = nil
	defer unauth.CloseIdleConnections()
	if response, err = unauth.Get(server.URL + "/v1/managed/settings"); err == nil {
		response.Body.Close()
		t.Fatal("missing TLS client identity accepted")
	}
	if remote.reads != 1 {
		t.Fatal("denied request reached stock backend", remote.reads)
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
