package adminview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func ciJobLinkFacts(at time.Time) []cihealth.AttemptFacts {
	out := make([]cihealth.AttemptFacts, 0, 2)
	for _, attempt := range []int64{1, 2} {
		key := cihealth.AttemptKey{Source: "github", RepositoryID: 12, WorkflowID: 17, RunID: 9223372036854775807, Attempt: attempt}
		clock := cihealth.Clock{ObservedAt: at, Phase: cihealth.Queued}
		out = append(out, cihealth.AttemptFacts{Key: key, Clock: clock, AsOf: at,
			Jobs: []cihealth.JobFacts{{Key: cihealth.JobKey{Attempt: key, JobID: 9007199254740993 + attempt}, Clock: clock}}})
	}
	return out
}

func TestOwnerCIJobLookupIsScopedByGenerationAndSource(t *testing.T) {
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
	const dest = "https://github.com/synthetic/fixture/actions/runs/9223372036854775807/job/"
	selectLink := func(k cihealth.JobKey) (func() (string, bool), bool) {
		return func() (string, bool) { return dest + ciInt(k.JobID), alive.Load() }, true
	}
	const path = "https://127.0.0.1/admin/api/v1/ci-health"
	call := func(method, url, bearer, origin, body string, handles ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for _, handle := range handles {
			r.Header.Add(ciJobHandleHeader, handle)
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
		if err := s.publishTiming(now, ciJobLinkFacts(now), false, selectLink, nil); err != nil {
			t.Fatal(err)
		}
		w := call("GET", path, token, "", "")
		var snapshot struct{ Timing ciTimingView }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || len(snapshot.Timing.Attempts) != 2 ||
			strings.Contains(w.Body.String(), "https://") || strings.Contains(w.Body.String(), "/actions/runs/") {
			t.Fatal("snapshot lacked handles or carried destination data")
		}
		items := snapshot.Timing.Attempts
		if len(items[0].Jobs) != 1 || len(items[1].Jobs) != 1 {
			t.Fatal("unexpected fixture jobs")
		}
		a, b := items[0].Jobs[0].LinkHandle, items[1].Jobs[0].LinkHandle
		if !validCIJobHandle(a) || !validCIJobHandle(b) || a == b {
			t.Fatal("invalid or reused handle")
		}
		return a, b
	}
	refused := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code || strings.Contains(w.Body.String(), "https://") ||
			strings.Contains(w.Body.String(), "attempts") || strings.Contains(w.Body.String(), "job_link_handle") {
			t.Fatal("lookup returned destination or fell through to snapshot")
		}
	}
	a, b := publish()
	for i, handle := range []string{a, b} {
		w := call("GET", path, token, "https://127.0.0.1", "", handle)
		var got ciJobLinkResponse
		job := ciInt(9007199254740994 + int64(i))
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil ||
			got != (ciJobLinkResponse{dest + job, "github", "12", "17", "9223372036854775807", ciInt(int64(i + 1)), job}) {
			t.Fatal("did not resolve exact rerun job")
		}
	}
	refused(call("GET", path, "", "", "", a), http.StatusUnauthorized)
	refused(call("GET", path, strings.Repeat("b", 64), "", "", a), http.StatusUnauthorized)
	refused(call("POST", path, token, "", "", a), http.StatusMethodNotAllowed)
	refused(call("GET", path+"?handle="+a, token, "", "", a), http.StatusBadRequest)
	refused(call("GET", path+"?", token, "", "", a), http.StatusBadRequest)
	refused(call("GET", path, token, "https://foreign.invalid", "", a), http.StatusBadRequest)
	refused(call("GET", path, token, "", "", a, a), http.StatusBadRequest)
	refused(call("GET", path, token, "", "", "malformed"), http.StatusBadRequest)
	refused(call("GET", path, token, "", "body", a), http.StatusBadRequest)
	refused(call("GET", path, token, "", "", strings.Repeat("A", 43)), http.StatusNotFound)
	now = now.Add(time.Second)
	fresh, _ := publish()
	refused(call("GET", path, token, "", "", a), http.StatusNotFound)
	alive.Store(false)
	refused(call("GET", path, token, "", "", fresh), http.StatusNotFound)
	s.Withdraw()
	refused(call("GET", path, token, "", "", fresh), http.StatusNotFound)
	alive.Store(true)
	now = now.Add(time.Second)
	fresh, _ = publish()
	now = now.Add(ciDisplayLifetime)
	refused(call("GET", path, token, "", "", fresh), http.StatusNotFound)
}

func TestOwnerCIJobLookupLinearizesBeforeTerminalClose(t *testing.T) {
	s, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	resolver := func(cihealth.JobKey) (func() (string, bool), bool) {
		return func() (string, bool) {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return "https://github.com/synthetic/fixture/actions/runs/9223372036854775807/job/9007199254740994", true
		}, true
	}
	if err := s.publishTiming(at, ciJobLinkFacts(at)[:1], false, resolver, nil); err != nil {
		t.Fatal(err)
	}
	var handle string
	for handle = range s.jobLinks {
	}
	result := make(chan []byte, 1)
	go func() { result <- s.resolveCIJobLink(t.Context(), handle) }()
	<-entered
	stopping := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		close(stopping)
		s.Stop()
		close(stopped)
	}()
	<-stopping
	close(release)
	if <-result == nil {
		t.Fatal("already-authorized lookup disappeared before a competing terminal fence")
	}
	<-stopped
	next := time.Now()
	if s.resolveCIJobLink(t.Context(), handle) != nil ||
		s.publishTiming(next, ciJobLinkFacts(next), false, resolver, nil) == nil {
		t.Fatal("terminal stop permitted resolution or a new publication")
	}
}

func TestOwnerCIJobLookupRechecksSourceAndCallerBeforeAuthorization(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		s, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 12}})
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC().Add(-time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		var calls int
		if err := s.publishTiming(at, ciJobLinkFacts(at)[:1], false, func(cihealth.JobKey) (func() (string, bool), bool) {
			return func() (string, bool) {
				calls++
				if cancelCaller {
					cancel()
				}
				return "https://github.com/synthetic/fixture/actions/runs/9223372036854775807/job/9007199254740994", calls == 1
			}, true
		}, nil); err != nil {
			cancel()
			t.Fatal(err)
		}
		var handle string
		for handle = range s.jobLinks {
		}
		got := s.resolveCIJobLink(ctx, handle)
		cancel()
		if got != nil {
			t.Fatal("closed source or cancelled caller reached authorization")
		}
	}
}
