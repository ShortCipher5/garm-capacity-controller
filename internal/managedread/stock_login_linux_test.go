package managedread

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestStockLoginReauthenticatesFromPrivateFile(t *testing.T) {
	const controller = "00000000-0000-4000-8000-000000000091"
	var generation, logins atomic.Int64
	generation.Store(1)
	path := filepath.Join(t.TempDir(), "password")
	writeSecret := func(g int64) {
		t.Helper()
		if err := os.WriteFile(path, []byte(fmt.Sprintf("synthetic-%d\n", g)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSecret(1)
	expiry := time.Now().Add(time.Hour).Unix()
	token := func(g int64) string {
		return "fixture." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"gen":%d}`, expiry, g))) + ".fixture"
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := generation.Load()
		if r.URL.Path == "/api/v1/auth/login" {
			logins.Add(1)
			var in struct{ Username, Password string }
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" || json.NewDecoder(r.Body).Decode(&in) != nil ||
				in.Username != "fixtureadmin" || in.Password != fmt.Sprintf("synthetic-%d", g) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": token(g)})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token(g) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			t.Error("inventory unexpectedly used non-GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/api/v1/controller":
			fmt.Fprintf(w, `{"controller_id":%q}`, controller)
		case "/api/v1/pools":
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
	ctx := context.Background()
	source, err := newStockReads(ctx, server.URL, controller, hex.EncodeToString(pin[:]), "", "fixtureadmin", path, &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := source.Pools(ctx); string(raw) != "[]" || err != nil || logins.Load() != 1 {
		t.Fatal("valid token was not reused")
	}
	writeSecret(2)
	generation.Store(2)
	if raw, err := source.Pools(ctx); string(raw) != "[]" || err != nil || logins.Load() != 2 {
		t.Fatal("reader did not reauthenticate with a single attempt")
	}
	generation.Store(3)
	for range 4 {
		if raw, err := source.Pools(ctx); raw != nil || err == nil {
			t.Fatal("returned inventory after failed authentication")
		}
	}
	if logins.Load() != 3 {
		t.Fatal("failed authentication was retried without a cooldown")
	}
}
