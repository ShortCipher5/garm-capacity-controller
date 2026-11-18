package stockidentity

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

const pool = "11111111-1111-4111-8111-111111111111"
const repo = "22222222-2222-4222-8222-222222222222"

func rawFacts() map[string]any {
	return map[string]any{"id": pool, "repo_id": repo, "provider_name": "capacity", "flavor": "small", "image": "runner", "os_type": "linux", "os_arch": "amd64"}
}
func TestStableOwnershipProjectionIgnoresVolatileLimitsButDetectsProfileDrift(t *testing.T) {
	m := rawFacts()
	raw, _ := json.Marshal(m)
	first, err := ParseFacts(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"enabled", "max_runners", "min_idle_runners", "priority", "generation", "tags", "provider_specs"} {
		m[key] = "changed"
	}
	raw, _ = json.Marshal(m)
	second, err := ParseFacts(raw)
	if err != nil || first.Fingerprint() != second.Fingerprint() {
		t.Fatal("volatile settings changed ownership projection")
	}
	for _, key := range []string{"provider_name", "flavor", "image", "os_arch", "repo_id"} {
		n := rawFacts()
		if key == "os_arch" {
			n[key] = "arm64"
		} else if key == "repo_id" {
			n[key] = "33333333-3333-4333-8333-333333333333"
		} else {
			n[key] = "different"
		}
		raw, _ = json.Marshal(n)
		changed, err := ParseFacts(raw)
		if err != nil || changed.Fingerprint() == first.Fingerprint() {
			t.Fatal("identity drift omitted", key, err)
		}
	}
}
func TestOwnershipRealTLSScopedReadRejectsFailuresAndZeroObservation(t *testing.T) {
	if (Observation{}).Valid() {
		t.Fatal("zero observation trusted")
	}
	failed := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/pools/"+pool || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("wrong scoped request")
			w.WriteHeader(400)
			return
		}
		if failed {
			w.Header().Set("Location", "/unrequested")
			w.WriteHeader(302)
			return
		}
		json.NewEncoder(w).Encode(rawFacts())
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	config := Configuration{Endpoint: server.URL, Token: "test", Provider: "capacity", Pool: pool, TLS: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	observed, err := Observe(context.Background(), config)
	if err != nil || !observed.Valid() || observed.Facts().Repository != repo || len(observed.SourceIdentity()) != 64 {
		t.Fatal("missing real observation", err)
	}
	failed = true
	if o, err := Observe(context.Background(), config); err == nil || o.Valid() {
		t.Fatal("redirect minted observation")
	}
	config.TLS.InsecureSkipVerify = true
	if _, err := Observe(context.Background(), config); err == nil {
		t.Fatal("unverified TLS accepted")
	}
}
func TestOwnershipRejectsDuplicateAliasesAndNonRepositoryOwnership(t *testing.T) {
	raw, _ := json.Marshal(rawFacts())
	for _, body := range []string{
		strings.TrimSuffix(string(raw), "}") + ",\"id\":\"" + pool + "\"}",
		strings.TrimSuffix(string(raw), "}") + ",\"Repo_ID\":\"" + repo + "\"}",
		strings.TrimSuffix(string(raw), "}") + ",\"org_id\":\"" + repo + "\"}",
		"null", string(raw) + " trailing",
	} {
		if _, err := ParseFacts([]byte(body)); err == nil {
			t.Fatal("ambiguous ownership accepted")
		}
	}
}
