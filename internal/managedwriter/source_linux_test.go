package managedwriter

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceInspectorCannotUseWriterRole(t *testing.T) {
	writer := &x509.Certificate{Raw: []byte("writer")}
	inspector := &x509.Certificate{Raw: []byte("inspector")}
	h := &Handler{leaf: sha256.Sum256(writer.Raw)}
	pin := sha256.Sum256(inspector.Raw)
	if h.SetSourceInspector(hex.EncodeToString(h.leaf[:])) == nil {
		t.Fatal("shared role accepted")
	}
	if h.SetSourceInspector(hex.EncodeToString(pin[:])) != nil {
		t.Fatal("separate role rejected")
	}
	if h.SetSourceInspector(hex.EncodeToString(pin[:])) == nil {
		t.Fatal("role replaced")
	}
	for _, tc := range []struct {
		cert         *x509.Certificate
		method, path string
		code         int
	}{{writer, "GET", managedsource.SourcePath, 403}, {inspector, "POST", managedsource.SourcePath, 403}, {inspector, "GET", "/api/v1/instances", 403}, {inspector, "POST", "/v1/managed/pools/pool/pause", 403}, {inspector, "GET", managedsource.SourcePath, 503}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set(managedsource.NonceHeader, strings.Repeat("a", 64))
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tc.cert}, VerifiedChains: [][]*x509.Certificate{{tc.cert}}}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
		if w.Header().Get(managedsource.SourceHeader) != "" {
			t.Fatal("source minted without live session")
		}
	}
}
