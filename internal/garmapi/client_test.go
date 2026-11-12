package garmapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixture(t *testing.T, handler http.Handler) (*httptest.Server, *tls.Config) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return server, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}
func TestCompleteScopedInventoryAndScalesetMapping(t *testing.T) {
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer disposable-fixture-token" {
			t.Error("incorrect inventory route/auth")
		}
		_ = json.NewEncoder(w).Encode([]any{
			map[string]any{"provider_name": "other-provider", "name": "unrelated", "provider_fault": "private-fault"},
			map[string]any{"provider_name": "capacity", "name": "runner-1", "provider_id": "runner-1", "pool_id": "pool-a", "status": "creating"},
			map[string]any{"provider_name": "capacity", "name": "runner-2", "scale_set_id": uint64(5), "status": "running", "job": map[string]any{"private": "do-not-retain"}},
		})
	}))
	client, err := New(server.URL, "disposable-fixture-token", trust, []string{"capacity"}, map[uint64]string{5: "scaleset-pool"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	rows, err := client.Instances(context.Background())
	if err != nil || len(rows) != 2 || rows[0].State != "creating" || rows[1].PoolID != "scaleset-pool" || !rows[1].Busy {
		t.Fatalf("inventory %+v %v", rows, err)
	}
	encoded, _ := json.Marshal(rows)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("private source fields escaped")
	}
}
func TestPartialMalformedOrUnauthorizedInventoriesFailClosed(t *testing.T) {
	cases := map[string]string{
		"null": `null`, "object": `{"instances":[]}`, "duplicate": `[{"provider_name":"capacity","name":"one","name":"two","pool_id":"pool","status":"running"}]`,
		"wrong-case":       `[{"Provider_Name":"capacity","name":"one","pool_id":"pool","status":"running"}]`,
		"missing-pool":     `[{"provider_name":"capacity","name":"one","status":"creating"}]`,
		"unknown-scaleset": `[{"provider_name":"capacity","name":"one","scale_set_id":9,"status":"creating"}]`,
		"conflicting-pool": `[{"provider_name":"capacity","name":"one","scale_set_id":5,"pool_id":"other","status":"creating"}]`,
		"null-state":       `[{"provider_name":"capacity","name":"one","pool_id":"pool","status":null}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			client, err := New(server.URL, "test", trust, []string{"capacity"}, map[uint64]string{5: "pool"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := client.Instances(context.Background()); err == nil {
				t.Fatal("partial inventory accepted")
			}
		})
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://other.invalid/token")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private-token-response"))
			}))
			client, err := New(server.URL, "test", trust, []string{"capacity"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			rows, err := client.Instances(context.Background())
			if err == nil || len(rows) != 0 || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe failed read")
			}
		})
	}
}
func TestTrustAndCredentialConfiguration(t *testing.T) {
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) }))
	for _, endpoint := range []string{"http://example.invalid", "https://user:pass@example.invalid", "https://example.invalid?token=secret", server.URL + "/api"} {
		if _, err := New(endpoint, "test", trust, []string{"capacity"}, nil); err == nil {
			t.Fatal("unsafe endpoint")
		}
	}
	bad := trust.Clone()
	bad.InsecureSkipVerify = true
	if _, err := New(server.URL, "test", bad, []string{"capacity"}, nil); err == nil {
		t.Fatal("unverified trust")
	}
	if _, err := New(server.URL, "test\r\ninjection", trust, []string{"capacity"}, nil); err == nil {
		t.Fatal("header injection")
	}
	if _, err := New(server.URL, "test", trust, nil, nil); err == nil {
		t.Fatal("unscoped provider inventory")
	}
}
