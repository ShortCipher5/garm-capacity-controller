package managedwriter

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReductionRouteRequiresExactRoleScopeAndBoundedBody(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("reduction-test")}
	leaf := sha256.Sum256(cert.Raw)
	pool := "11111111-1111-4111-8111-111111111111"
	h := &Handler{leaf: leaf, pools: map[string]bool{pool: true}}
	for _, c := range []struct {
		body string
		code int
	}{{`{"maximum":2}`, 503}, {`{"maximum":0}`, 400}, {`{"maximum":2,"min_idle_runners":0}`, 400}, {`{"maximum":2,"maximum":1}`, 400}, {`{"maximum":2,"enabled":true}`, 400}} {
		r := httptest.NewRequest("POST", "/v1/managed/pools/"+pool+"/reduce-maximum", strings.NewReader(c.body))
		r.Header.Set("Content-Type", "application/json")
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.code {
			t.Fatal(c, w.Code)
		}
	}
}
