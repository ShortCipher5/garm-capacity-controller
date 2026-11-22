package demandobserver

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func TestCIHealthSourceReusesOnlyEnrolledGitHubReadScope(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var blocked atomic.Bool
	var filteredResponse atomic.Int32
	created := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("unexpected or unauthenticated request")
		}
		switch r.URL.Path {
		case "/repos/team/a/actions/runs":
			if got := r.URL.Query().Get("created"); got != "" {
				if r.URL.Query().Get("page") != "1" || r.URL.Query().Has("status") {
					t.Error("wrong filtered query")
				}
				switch filteredResponse.Load() {
				case 1:
					runs := make([]any, 0, demandrepair.CIPerPage)
					for i := 1; i <= demandrepair.CIPerPage; i++ {
						runs = append(runs, map[string]any{
							"id": i, "workflow_id": 10, "run_attempt": 2, "status": "in_progress",
							"created_at": created,
							"repository": map[string]any{"id": int64(9007199254740993), "full_name": "team/a"},
						})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1000, "workflow_runs": runs})
				case 2:
					fmt.Fprint(w, `{"total_count":1000,"workflow_runs":[`)
				case 3:
					w.WriteHeader(http.StatusTooManyRequests)
				default:
					t.Error("unexpected filtered call")
				}
				return
			}
			if r.URL.Query().Get("status") != "queued" || r.URL.Query().Get("page") != "1" {
				t.Error("wrong query")
			}
			if blocked.Load() {
				close(entered)
				<-r.Context().Done()
				close(cancelled)
				return
			}
			fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
		case "/repos/team/a":
			fmt.Fprint(w, `{"id":9007199254740993,"full_name":"team/a"}`)
		default:
			t.Error("expanded configured request", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	ca := filepath.Join(dir, "ca")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "rotating-token")
	a := demandrepair.Repository{GarmID: "fixtureA", Host: "github.example", Owner: "team", Name: "a"}
	b := demandrepair.Repository{GarmID: "fixtureB", Host: "github.example", Owner: "team", Name: "b"}
	// The GARM endpoint and paths are deliberately absent, proving CI does not
	// try to initialize the stock transport or load its credentials.
	cfg := Config{Enabled: true, GitHubEndpoint: server.URL, GitHubAuthority: "github.example", GitHubTokenFile: key, GitHubCAFile: ca,
		Repositories: []demandrepair.Repository{a, b}, CheckpointPath: filepath.Join(dir, "absent-checkpoint"),
		InitialFrom: time.Now().Add(-time.Hour), IntervalSeconds: 60, MaxBackoffSeconds: 90}
	pin := CIHealthRepositoryPin{a.GarmID, 9007199254740993}
	in := CIHealthInput{Enabled: true, Repositories: []CIHealthRepositoryPin{pin}}
	source, err := NewCIHealthSource(cfg, in)
	if err != nil {
		t.Fatal("startup requires unavailable token or Garm paths", err)
	}
	defer source.Close()
	if calls.Load() != 0 {
		t.Fatal("startup made a request")
	}
	q := demandrepair.CIRunQuery{Status: "queued", Page: 1}
	if _, err = source.ListCIRuns(t.Context(), a.GarmID, q); !errors.Is(err, ErrObserver) || calls.Load() != 0 {
		t.Fatal("missing accepted credential reached HTTP", err)
	}
	if err := os.WriteFile(key, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Repositories[0] = b
	in.Repositories[0].GitHubID = 1
	scope := source.Repositories()
	if source.SourceID() == "" || len(scope) != 1 || scope[0].Repository != a || scope[0].GitHubID != pin.GitHubID {
		t.Fatal("pins were not frozen", scope)
	}
	scope[0].GitHubID = 42
	if _, err = source.ListCIRuns(t.Context(), b.GarmID, q); !errors.Is(err, ErrObserver) || calls.Load() != 0 {
		t.Fatal("unenrolled repository reached HTTP", err)
	}
	if _, err = source.ListCIRuns(t.Context(), a.GarmID, demandrepair.CIRunQuery{Status: "queued", Page: 0}); !errors.Is(err, ErrObserver) || calls.Load() != 0 {
		t.Fatal("invalid page reached HTTP", err)
	}
	if got, err := source.ListCIRuns(t.Context(), a.GarmID, q); err != nil || got.Runs == nil || calls.Load() != 2 {
		t.Fatal("enrolled exact read rejected", err)
	}
	filtered := demandrepair.CIRunQuery{Page: 1, From: created.Add(-time.Minute), To: created.Add(time.Second)}
	for _, tc := range []struct {
		mode int32
		want error
	}{{1, demandrepair.ErrCIRunWindowAtCap}, {2, ErrObserver}} {
		filteredResponse.Store(tc.mode)
		got, err := source.ListCIRuns(t.Context(), a.GarmID, filtered)
		assertNoCIRunPage(t, got, err, tc.want)
	}
	blocked.Store(true)
	done := make(chan error, 1)
	go func() { _, err := source.ListCIRuns(t.Context(), a.GarmID, q); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("query did not reach exact server")
	}
	source.Close()
	source.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrObserver) {
			t.Fatal("closed read returned data", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not cancel query")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe cancellation")
	}
	if _, err = source.ListCIRuns(t.Context(), a.GarmID, q); !errors.Is(err, ErrObserver) || calls.Load() != 5 {
		t.Fatal("new request used closed source", err)
	}

	// A rate-limit response is still generic; use a separate reader so its
	// quota hold does not interfere with the cancellation fixture above.
	cfg.Repositories[0], in.Repositories[0] = a, pin
	limited, err := NewCIHealthSource(cfg, in)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	filteredResponse.Store(3)
	got, err := limited.ListCIRuns(t.Context(), a.GarmID, filtered)
	assertNoCIRunPage(t, got, err, ErrObserver)
	if calls.Load() != 6 {
		t.Fatalf("429 fixture did not reach the exact source: %d calls", calls.Load())
	}
}

func TestCIHealthFactoryDefaultOffAndRefusesConflictingScope(t *testing.T) {
	if source, err := NewCIHealthSource(Config{}, CIHealthInput{}); source != nil || err != nil {
		t.Fatal("default off initialized")
	}
	a := demandrepair.Repository{GarmID: "a", Host: "github.example", Owner: "team", Name: "a"}
	b := demandrepair.Repository{GarmID: "b", Host: "github.example", Owner: "team", Name: "b"}
	c := Config{Enabled: true, GitHubAuthority: "github.example", GitHubTokenFile: "not-opened", Repositories: []demandrepair.Repository{a, b},
		CheckpointPath: "not-opened", InitialFrom: time.Now().Add(-time.Hour), IntervalSeconds: 60, MaxBackoffSeconds: 60}
	for _, pins := range [][]CIHealthRepositoryPin{nil, {{"other", 3}}, {{"a", 0}}, {{"a", 1}, {"a", 3}}, {{"a", 1}, {"b", 1}}} {
		if source, err := NewCIHealthSource(c, CIHealthInput{Enabled: true, Repositories: pins}); source != nil || !errors.Is(err, ErrObserver) {
			t.Fatal("expanded invalid scope", pins, err)
		}
	}
}

func assertNoCIRunPage(t *testing.T, got demandrepair.CIRunPage, err, want error) {
	t.Helper()
	if err != want || got.Runs != nil || !got.ObservedAt.IsZero() || got.TotalCount != 0 || got.NextPage != 0 {
		t.Fatalf("got page %+v, error %v; want no page and %v", got, err, want)
	}
}

func TestCIHealthReadClassifiedLifetimeWinsOverReadResult(t *testing.T) {
	for _, tc := range []string{"active", "caller canceled", "reader closed"} {
		t.Run(tc, func(t *testing.T) {
			life, cancelLife := context.WithCancel(t.Context())
			defer cancelLife()
			s := &CIHealthSource{life: life}
			caller, cancelCaller := context.WithCancel(t.Context())
			defer cancelCaller()
			capErr := demandrepair.ErrCIRunWindowAtCap
			got, err := ciHealthReadClassified(s, caller, func(context.Context, *demandrepair.CIHealthSource) (demandrepair.CIRunPage, error) {
				switch tc {
				case "caller canceled":
					cancelCaller()
				case "reader closed":
					cancelLife()
				}
				return demandrepair.CIRunPage{ObservedAt: time.Now(), NextPage: 2, TotalCount: 1000,
					Runs: []demandrepair.CIRun{{RunID: 1}}}, capErr
			}, capErr)
			want := ErrObserver
			if tc == "active" {
				want = capErr
			}
			assertNoCIRunPage(t, got, err, want)
		})
	}
}
