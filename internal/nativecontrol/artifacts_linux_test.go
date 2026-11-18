//go:build linux

package nativecontrol

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeartifacts"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactsRefuseUnqualifiedAndNoncanonical(t *testing.T) {
	nonce := strings.Repeat("a", 64)
	for _, tc := range []struct {
		method, query string
		status        int
	}{
		{"GET", "nonce=" + nonce, 503},
		{"POST", "nonce=" + nonce, 400},
		{"GET", "", 400},
		{"GET", "nonce=" + nonce + "&extra=1", 400},
		{"GET", "nonce=" + nonce + "&nonce=" + nonce, 400},
		{"GET", "nonce=" + strings.Repeat("A", 64), 400},
		{"GET", "nonce=%61" + nonce[1:], 400},
	} {
		r := httptest.NewRequest(tc.method, nativeartifacts.Path+"?"+tc.query, nil)
		w := httptest.NewRecorder()
		serveArtifacts(w, r, nil, "", "")
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
		if strings.Contains(w.Body.String(), `"quota_bytes"`) {
			t.Fatal("unqualified proof emitted")
		}
	}
}
