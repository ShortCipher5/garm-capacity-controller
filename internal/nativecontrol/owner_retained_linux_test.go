//go:build linux

package nativecontrol

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeretained"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetainedReadDoesNotSynthesizeEvidence(t *testing.T) {
	o := &executionOwner{}
	path := nativeretained.PathPrefix + strings.Repeat("a", 64) + "/evidence"
	nonce := strings.Repeat("b", 64)
	for _, tc := range []struct {
		method, query string
		status        int
	}{{"GET", "nonce=" + nonce, 409}, {"POST", "nonce=" + nonce, 400}, {"GET", "", 400}, {"GET", "nonce=" + nonce + "&nonce=" + nonce, 400}, {"GET", "nonce=" + nonce + "&path=x", 400}} {
		r := httptest.NewRequest(tc.method, path+"?"+tc.query, nil)
		w := httptest.NewRecorder()
		if !o.serveRetainedEvidence(w, r, "", "") || w.Code != tc.status {
			t.Fatal(w.Code)
		}
		if strings.Contains(w.Body.String(), `"namespace_terminated":true`) {
			t.Fatal("synthetic proof")
		}
	}
}
