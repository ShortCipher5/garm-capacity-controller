//go:build linux

package nativecontrol

import (
	"net/http/httptest"
	"testing"
)

func TestCacheDiagnosticNeverBypassesOwnerBoundary(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", cachePath, 503}, {"POST", cachePath, 400}, {"GET", cachePath + "?path=/etc/passwd", 400}} {
		w := httptest.NewRecorder()
		serveCache(w, httptest.NewRequest(tc.method, tc.path, nil), nil)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code)
		}
	}
}
