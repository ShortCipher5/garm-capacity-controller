package adminview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

// Exact run navigation resolves a logical run destination only through the
// owner-authenticated handle route, only while the observed locator is still
// current. Unsupported or expired records are unavailable, never inferred:
// the snapshot carries no URL, and a dead locator resolves to nothing.
func TestOwnerCIRunLookupResolvesExactRunAndRefusesUnsupportedOrExpired(t *testing.T) {
	h, _, token := fixture(t)
	s, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil || h.SetCIHealthSnapshot(s) != nil {
		t.Fatal("attach configured snapshot", err)
	}
	at := time.Date(2026, 1, 3, 0, 0, 1, 0, time.UTC)
	now := at
	s.clock = func() time.Time { return now }
	var alive atomic.Bool
	alive.Store(true)
	const dest = "https://github.com/synthetic/fixture/actions/runs/9223372036854775807"
	selectRun := func(k cihealth.AttemptKey) (func() (string, bool), bool) {
		return func() (string, bool) { return dest, alive.Load() }, true
	}
	const path = "https://127.0.0.1/admin/api/v1/ci-health"
	call := func(method, url, bearer, origin string, handles ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, url, strings.NewReader(""))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for _, handle := range handles {
			r.Header.Add(ciRunHandleHeader, handle)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("owner route lost privacy baseline")
		}
		return w
	}
	publish := func() (string, string) {
		t.Helper()
		if err := s.publishTiming(now, ciJobLinkFacts(now), false, nil, selectRun); err != nil {
			t.Fatal(err)
		}
		w := call("GET", path, token, "")
		var snapshot struct{ Timing ciTimingView }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || len(snapshot.Timing.Attempts) != 2 ||
			strings.Contains(w.Body.String(), "https://") || strings.Contains(w.Body.String(), "/actions/runs/") {
			t.Fatal("snapshot carried destination data")
		}
		a, b := snapshot.Timing.Attempts[0].RunLinkHandle, snapshot.Timing.Attempts[1].RunLinkHandle
		if !validCIJobHandle(a) || !validCIJobHandle(b) || a == b {
			t.Fatal("invalid or reused handle")
		}
		return a, b
	}
	refused := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code || strings.Contains(w.Body.String(), "https://") ||
			strings.Contains(w.Body.String(), "attempts") || strings.Contains(w.Body.String(), "run_link_handle") {
			t.Fatal("lookup returned destination or fell through to snapshot")
		}
	}
	// The logical run destination is shared by every retry: both attempt
	// handles resolve to the same canonical run URL with exact identity.
	a, b := publish()
	for _, handle := range []string{a, b} {
		w := call("GET", path, token, "https://127.0.0.1", handle)
		var got ciRunLinkResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil ||
			got != (ciRunLinkResponse{dest, "github", "12", "17", "9223372036854775807", got.Attempt}) ||
			(got.Attempt != "1" && got.Attempt != "2") {
			t.Fatal("did not resolve the exact observed run")
		}
	}
	refused(call("GET", path, "", "", a), http.StatusUnauthorized)
	refused(call("GET", path, token, "https://foreign.invalid", "", a), http.StatusBadRequest)
	refused(call("GET", path, token, "", a, a), http.StatusBadRequest)
	// A superseded generation and a dead observed locator are unavailable.
	now = now.Add(time.Second)
	fresh, _ := publish()
	refused(call("GET", path, token, "", a), http.StatusNotFound)
	alive.Store(false)
	refused(call("GET", path, token, "", fresh), http.StatusNotFound)
	s.Withdraw()
	refused(call("GET", path, token, "", fresh), http.StatusNotFound)
	// Attempts whose reader supplied no locator never receive a handle: the
	// navigation is unavailable rather than inferred.
	alive.Store(true)
	now = now.Add(time.Second)
	if err := s.publishTiming(now, ciJobLinkFacts(now), false, nil, nil); err != nil {
		t.Fatal(err)
	}
	w := call("GET", path, token, "")
	if strings.Contains(w.Body.String(), "run_link_handle") || len(s.runLinks) != 0 {
		t.Fatal("unavailable run navigation fabricated a handle")
	}
}

// Handles minted for one attempt can never resolve another attempt's run or
// fall through to the snapshot body.
func TestOwnerCIRunLookupRejectsUnknownHandles(t *testing.T) {
	h, _, token := fixture(t)
	s, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil || h.SetCIHealthSnapshot(s) != nil {
		t.Fatal("attach configured snapshot", err)
	}
	at := time.Date(2026, 1, 3, 0, 0, 1, 0, time.UTC)
	s.clock = func() time.Time { return at }
	if err := s.publishTiming(at, ciJobLinkFacts(at), false, nil, func(k cihealth.AttemptKey) (func() (string, bool), bool) {
		return func() (string, bool) {
			return "https://github.com/synthetic/fixture/actions/runs/9223372036854775807", true
		}, true
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		code   int
		handle string
	}{
		{http.StatusBadRequest, "malformed"},
		{http.StatusNotFound, strings.Repeat("A", 43)},
		{http.StatusNotFound, strings.Repeat("A", 42) + "E"},
	} {
		r := httptest.NewRequest("GET", "https://127.0.0.1/admin/api/v1/ci-health", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set(ciRunHandleHeader, tc.handle)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code || strings.Contains(w.Body.String(), "https://") {
			t.Fatal("unknown run handle resolved or leaked", w.Code)
		}
	}
}
