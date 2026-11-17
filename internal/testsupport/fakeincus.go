// Package testsupport supplies disposable loopback credentials for tests only.
// No application executable imports this package or contains these generated keys.
package testsupport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// FakeIncusCreateOperation and FakeIncusDeleteOperation are the fixed operation
// identities the fake daemon reports. They are fixture constants, never live
// infrastructure identifiers.
const FakeIncusCreateOperation = "12345678-1234-1234-1234-123456789abc"
const FakeIncusDeleteOperation = "87654321-4321-4321-a321-cba987654321"

const fakeInstanceUUID = "11111111-1111-4111-8111-111111111111"
const fakeGeneration = "22222222-2222-4222-8222-222222222222"

// FakeIncus is a stateful fake Incus 6.0.4 daemon for tests only. It serves
// the narrow route set the verified transport exercises (root, create, wait,
// named identity read, recursive inventories, force delete, delete-operation
// polling) and never leaves the test process. It simulates faults; it is not a
// behavioral model of a real daemon.
type FakeIncus struct {
	Server *httptest.Server
	Client *incusapi.Client

	mu               sync.Mutex
	configs          map[string]map[string]string
	createdAt        map[string]string
	guestVisible     bool
	deleteFaulty     bool
	waitFaulty       bool
	absenceLying     bool
	incarnationDrift bool
	deleteSettled    bool

	CreatePosts   atomic.Int32
	Deletes       atomic.Int32
	DeletePolls   atomic.Int32
	IdentityReads atomic.Int32
	AbsenceReads  atomic.Int32
}

// NewFakeIncus starts a mutually-authenticated fake daemon and returns it with
// an already-constructed verified client (project "runners").
func NewFakeIncus(t testing.TB, serverName string) *FakeIncus {
	t.Helper()
	fake := &FakeIncus{configs: map[string]map[string]string{}, createdAt: map[string]string{}, guestVisible: true}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable-fake-incus-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(authority)
	certificate := func(serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "disposable-fake-incus"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, authority, &private.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: private}
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(fake.serve))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate(2, x509.ExtKeyUsageServerAuth)}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	fake.Server = server
	fake.Client = incusClient(t, server, certificate(3, x509.ExtKeyUsageClientAuth), roots)
	return fake
}

func incusClient(t testing.TB, server *httptest.Server, certificate tls.Certificate, roots *x509.CertPool) *incusapi.Client {
	t.Helper()
	client, err := incusapi.New(server.URL, "runners", &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// SetGuestVisible controls whether the guest still appears in named and
// recursive reads after the delete fault knobs are exercised.
func (f *FakeIncus) SetGuestVisible(visible bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guestVisible = visible
}

// SetDeleteFaulty makes the DELETE route fail without removing the guest.
func (f *FakeIncus) SetDeleteFaulty(faulty bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteFaulty = faulty
}

// SetWaitFaulty makes the delete operation report a terminal failure.
func (f *FakeIncus) SetWaitFaulty(faulty bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitFaulty = faulty
}

// SetAbsenceLying keeps the guest visible in project-scoped reads even after
// an accepted delete, simulating an inventory that still lists the guest.
func (f *FakeIncus) SetAbsenceLying(lying bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.absenceLying = lying
}

// SetIncarnationDrift serves a different immutable incarnation identity on the
// named read, simulating delete/recreate or restore interference.
func (f *FakeIncus) SetIncarnationDrift(drift bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.incarnationDrift = drift
}

func (f *FakeIncus) writeSync(w http.ResponseWriter, metadata any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status_code": 200, "metadata": metadata})
}

func (f *FakeIncus) guestRow(name string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	config, ok := f.configs[name]
	if !ok || !f.guestVisible {
		return nil, false
	}
	expanded := map[string]any{"volatile.uuid": fakeInstanceUUID, "volatile.uuid.generation": fakeGeneration}
	if f.incarnationDrift {
		expanded["volatile.uuid.generation"] = "33333333-3333-4333-8333-333333333333"
	}
	for k, v := range config {
		expanded[k] = v
	}
	return map[string]any{"name": name, "status_code": 103, "created_at": f.createdAt[name], "expanded_config": expanded}, true
}

func (f *FakeIncus) serve(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("project") != "runners" {
		http.Error(w, "wrong project", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/1.0/instances" {
		f.CreatePosts.Add(1)
		var body struct {
			Name   string            `json:"name"`
			Config map[string]string `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, "bad create", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.configs[body.Name] = body.Config
		f.createdAt[body.Name] = time.Now().UTC().Format(time.RFC3339Nano)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + FakeIncusCreateOperation + "?project=runners", "metadata": map[string]any{"id": FakeIncusCreateOperation}})
		return
	}
	if r.Method == http.MethodDelete && len(r.URL.Path) > len("/1.0/instances/") {
		name := r.URL.Path[len("/1.0/instances/"):]
		f.mu.Lock()
		if _, ok := f.configs[name]; !ok {
			f.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		if f.deleteFaulty {
			f.mu.Unlock()
			http.Error(w, "private-delete-fault", http.StatusBadGateway)
			return
		}
		if !f.waitFaulty && !f.absenceLying {
			f.guestVisible = false
		}
		f.mu.Unlock()
		f.Deletes.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + FakeIncusDeleteOperation + "?project=runners", "metadata": map[string]any{"id": FakeIncusDeleteOperation}})
		return
	}
	switch r.URL.Path {
	case "/1.0":
		f.writeSync(w, f.rootMetadata())
	case "/1.0/instances":
		if r.URL.Query().Get("recursion") != "1" {
			http.Error(w, "incomplete inventory query", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		names := make([]string, 0, len(f.configs))
		for name := range f.configs {
			names = append(names, name)
		}
		visible := f.guestVisible
		f.mu.Unlock()
		rows := []any{}
		if visible {
			for _, name := range names {
				if row, ok := f.guestRow(name); ok {
					rows = append(rows, row)
				}
			}
		}
		f.AbsenceReads.Add(1)
		f.writeSync(w, rows)
	case "/1.0/operations":
		if r.URL.Query().Get("recursion") != "1" {
			http.Error(w, "incomplete operations query", http.StatusBadRequest)
			return
		}
		f.AbsenceReads.Add(1)
		f.writeSync(w, map[string]any{})
	case "/1.0/operations/" + FakeIncusCreateOperation + "/wait":
		f.writeSync(w, map[string]any{"id": FakeIncusCreateOperation, "status_code": 200, "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
	case "/1.0/operations/" + FakeIncusCreateOperation:
		f.writeSync(w, map[string]any{"id": FakeIncusCreateOperation, "class": "task", "description": "Creating instance", "status_code": 200, "status": "Success", "err": "", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}, "metadata": map[string]any{"private": "private-operation-metadata"}})
	case "/1.0/operations/" + FakeIncusDeleteOperation:
		f.DeletePolls.Add(1)
		f.mu.Lock()
		faulty, settled := f.waitFaulty, f.deleteSettled
		if !faulty && !settled {
			f.deleteSettled = true
		}
		f.mu.Unlock()
		if faulty {
			f.writeSync(w, map[string]any{"id": FakeIncusDeleteOperation, "class": "task", "description": "Deleting instance", "status_code": 400, "status": "Failure", "err": "private-delete-failure", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:01Z", "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
			return
		}
		status := 200
		if !settled {
			status = 103
		}
		f.writeSync(w, map[string]any{"id": FakeIncusDeleteOperation, "class": "task", "description": "Deleting instance", "status_code": status, "status": "Success", "err": "", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:01Z", "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
	default:
		if len(r.URL.Path) > len("/1.0/instances/") && strings.HasSuffix(r.URL.Path, "/state") {
			name := r.URL.Path[len("/1.0/instances/") : len(r.URL.Path)-len("/state")]
			if _, ok := f.guestRow(name); ok {
				f.writeSync(w, map[string]any{"status_code": 103})
				return
			}
			http.NotFound(w, r)
			return
		}
		if len(r.URL.Path) > len("/1.0/instances/") {
			name := r.URL.Path[len("/1.0/instances/"):]
			f.IdentityReads.Add(1)
			if row, ok := f.guestRow(name); ok {
				f.writeSync(w, row)
				return
			}
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

func (f *FakeIncus) rootMetadata() map[string]any {
	leaf := f.Server.Certificate()
	digest := sha256.Sum256(leaf.Raw)
	return map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}, "environment": map[string]any{"server_clustered": false, "server_name": "fake-incus-daemon", "server_version": "6.0.4", "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})), "certificate_fingerprint": fmt.Sprintf("%x", digest)}}
}
