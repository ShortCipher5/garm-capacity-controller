//go:build linux

package nativecontrol

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnforcementRefusesUnqualifiedAndNoncanonical(t *testing.T) {
	nonce := strings.Repeat("a", 64)
	for _, tc := range []struct {
		query  string
		status int
	}{{"nonce=" + nonce, 503}, {"", 400}, {"nonce=" + nonce + "&extra=1", 400}, {"nonce=" + nonce + "&nonce=" + nonce, 400}, {"nonce=" + strings.Repeat("A", 64), 400}} {
		r := httptest.NewRequest("GET", nativeenforcement.Path+"?"+tc.query, nil)
		w := httptest.NewRecorder()
		serveEnforcement(w, r, nil, "", "")
		if w.Code != tc.status {
			t.Fatalf("query %q status %d", tc.query, w.Code)
		}
	}
}
