package garmapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagedInventoryUsesMTLSWithoutStockToken(t *testing.T) {
	called := false
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
			t.Error("missing client identity")
		}
		if r.Header.Get("Authorization") != "" || r.Method != "GET" || r.URL.Path != "/api/v1/instances" {
			t.Error("credential or route escape")
		}
		w.Write([]byte("[]"))
	}))
	// Reuse test server identity as the test client certificate; actual writer
	// authenticates its independently pinned client leaf.
	s.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS13}
	s.StartTLS()
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	sum := sha256.Sum256(s.Certificate().RawSubjectPublicKeyInfo)
	trust := &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{s.TLS.Certificates[0]}}
	c, err := NewManagedInventory(s.URL, hex.EncodeToString(sum[:]), trust, []string{"owned"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Instances(context.Background()); err != nil || !called {
		t.Fatalf("inventory %v", err)
	}
	r, _ := http.NewRequest("GET", s.URL+"/api/v1/auth/login", nil)
	if _, err = c.http.Do(r); err == nil {
		t.Fatal("non-inventory route escaped")
	}
}
