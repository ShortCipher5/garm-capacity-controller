package managedread

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestIsolatedStockReadsBindControllerAndUseOnlyGet(t *testing.T) {
	const controller = "00000000-0000-4000-8000-000000000091"
	var markerReads, poolReads atomic.Int64
	var drift atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer fixture-only" {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.URL.Path {
		case "/api/v1/controller":
			markerReads.Add(1)
			if drift.Load() && poolReads.Load() == 2 {
				fmt.Fprint(w, `{"controller_id":"00000000-0000-4000-8000-000000000092"}`)
			} else {
				fmt.Fprintf(w, `{"controller_id":%q}`, controller)
			}
		case "/api/v1/pools":
			poolReads.Add(1)
			fmt.Fprint(w, "[]")
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	pin := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	trust := &tls.Config{RootCAs: roots}
	source, err := newIsolatedStockReads(context.Background(), server.URL, controller, hex.EncodeToString(pin[:]), "fixture-only", trust)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := source.Pools(context.Background()); err != nil || string(got) != "[]" || markerReads.Load() != 3 {
		t.Fatalf("safely bound read not obtained: %q, %v", got, err)
	}
	drift.Store(true)
	if got, err := source.Pools(context.Background()); got != nil || err == nil || source.Identity() != "" {
		t.Fatalf("changed controller returned inventory: %q, %v", got, err)
	}
	if _, err := source.Pools(context.Background()); err == nil || poolReads.Load() != 2 {
		t.Fatal("revoked source made another pool request")
	}
}

func TestIsolatedStockSourceRejectsUnboundDestinations(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:99", "https://8.8.8.8", "https://example.com",
		"https://127.0.0.1:99/api/v1", "https://127.0.0.1:99/?"} {
		if source, err := newIsolatedStockReads(context.Background(), endpoint,
			"00000000-0000-4000-8000-000000000091", "", "fixture-only", &tls.Config{RootCAs: x509.NewCertPool()}); source != nil || err == nil {
			t.Fatalf("invalid endpoint allowed: %s", endpoint)
		}
	}
}

func TestIsolatedStockCredentialRotationRequiresFreshSource(t *testing.T) {
	const controller = "00000000-0000-4000-8000-000000000091"
	var rotated atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		want := "Bearer fixture-first"
		if rotated.Load() {
			want = "Bearer fixture-replacement"
		}
		if req.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/api/v1/controller":
			fmt.Fprintf(w, `{"controller_id":%q}`, controller)
		case "/api/v1/instances":
			fmt.Fprint(w, "[]")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	cert := server.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	trust := &tls.Config{RootCAs: roots}
	if len(cert.DNSNames) != 0 {
		trust.ServerName = cert.DNSNames[0]
	}
	newSource := func(token string, cfg *tls.Config) (*isolatedStockReads, error) {
		return newIsolatedStockReads(context.Background(), server.URL, controller, hex.EncodeToString(pin[:]), token, cfg)
	}
	badName := trust.Clone()
	badName.ServerName = "not-in-fixture.invalid"
	if _, err := newSource("fixture-first", badName); err == nil {
		t.Fatal("trusted key accepted under a different TLS name")
	}
	first, err := newSource("fixture-first", trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Instances(context.Background()); err != nil {
		t.Fatal(err)
	}
	rotated.Store(true)
	if raw, err := first.Instances(context.Background()); raw != nil || err == nil || first.Identity() != "" {
		t.Fatalf("expired bearer returned data: %q %v", raw, err)
	}
	second, err := newSource("fixture-replacement", trust)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := second.Instances(context.Background()); string(raw) != "[]" || err != nil {
		t.Fatalf("fresh source did not recover: %q %v", raw, err)
	}
	if first.Identity() != "" {
		t.Fatal("revoked source revived")
	}
}
