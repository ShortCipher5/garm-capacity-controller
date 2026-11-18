package managedwriter

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictPauseObject(t *testing.T) {
	for _, s := range []string{`{}`, `{"disable":true,"reason":"pause"}`} {
		if !singleObject([]byte(s)) {
			t.Fatal("valid object rejected")
		}
	}
	for _, s := range []string{`[]`, `{"disable":false,"disable":true}`, `{"reason":"x"} {}`, `{"Disable":true}`, `{"a":`} {
		if singleObject([]byte(s)) {
			t.Fatal("ambiguous body accepted", s)
		}
	}
}
func TestProxyRejectsUnverifiedRoleAndGeneralAdministration(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("test leaf")}
	hash := sha256.Sum256(cert.Raw)
	h := &Handler{leaf: hash}
	for _, mode := range []string{"no-tls", "no-chain", "wrong-role", "origin", "query", "auth-route", "raw-pool-write"} {
		t.Run(mode, func(t *testing.T) {
			path := "https://proxy/v1/managed/pools/11111111-1111-4111-8111-111111111111/pause"
			method := "POST"
			if mode == "auth-route" {
				path = "https://proxy/api/v1/auth/login"
			}
			if mode == "raw-pool-write" {
				path = "https://proxy/api/v1/pools/11111111-1111-4111-8111-111111111111"
				method = "PUT"
			}
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.URL.Scheme = ""
			r.URL.Host = ""
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
			switch mode {
			case "no-tls":
				r.TLS = nil
			case "no-chain":
				r.TLS.VerifiedChains = nil
			case "wrong-role":
				r.TLS.PeerCertificates = []*x509.Certificate{{Raw: []byte("other")}}
			case "origin":
				r.Header.Set("Origin", "https://evil.invalid")
			case "query":
				r.URL.RawQuery = "x=y"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 && w.Code != 404 {
				t.Fatal(w.Code)
			}
		})
	}
}
