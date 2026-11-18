package demandrepair

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fixture(t *testing.T, h http.HandlerFunc) *GitHubClient {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	c, e := NewGitHub(server.URL, repo.Host, "test-ref", func(_ context.Context, ref string) (string, error) {
		if ref != "test-ref" {
			t.Error("wrong reference")
		}
		return "synthetic-test-token", nil
	}, &tls.Config{RootCAs: roots}, []Repository{repo})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	return c
}
func readTest(c *GitHubClient) ([]Job, error) {
	now := time.Now()
	return c.Read(context.Background(), repo, now.Add(-time.Hour), now)
}
func TestGitHubReadPaginatesAllAttempts(t *testing.T) {
	calls := 0
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-test-token" || r.Header.Get("X-GitHub-Api-Version") == "" {
			t.Error("bad read")
		}
		if r.URL.Path == "/repos/owner/repo/actions/runs" {
			fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":7,"repository":{"full_name":"owner/repo"}}]}`)
			return
		}
		if r.URL.Path != "/repos/owner/repo/actions/runs/7/jobs" || r.URL.Query().Get("filter") != "all" {
			t.Error("wrong jobs route")
		}
		jobs := []map[string]any{}
		start, end := 1, 100
		if r.URL.Query().Get("page") == "2" {
			start, end = 101, 101
		}
		for i := start; i <= end; i++ {
			jobs = append(jobs, map[string]any{"id": i, "run_id": 7, "status": "queued", "labels": []string{"linux"}})
		}
		json.NewEncoder(w).Encode(map[string]any{"total_count": 101, "jobs": jobs})
	})
	got, e := readTest(c)
	if e != nil || len(got) != 101 || calls != 8 {
		t.Fatalf("%d %v calls %d", len(got), e, calls)
	}
}
func TestGitHubReadFailsClosed(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"missing collection": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"total_count":0}`) },
		"rate":               func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) },
		"retry": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "1")
			fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
		},
		"ceiling": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"total_count":1000,"workflow_runs":[]}`) },
		"truncated": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"total_count":2,"workflow_runs":[{"id":7,"repository":{"full_name":"owner/repo"}}]}`)
		},
		"wrong repo": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":7,"repository":{"full_name":"other/repo"}}]}`)
		},
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.invalid/secret", 302)
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			got, e := readTest(fixture(t, h))
			if e != ErrObservation || got != nil {
				t.Fatalf("partial result: %+v %v", got, e)
			}
		})
	}
}
func TestGitHubScopeBeforeCredential(t *testing.T) {
	c := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP") })
	c.resolve = func(context.Context, string) (string, error) { t.Error("unexpected credential lookup"); return "", nil }
	now := time.Now()
	if _, e := c.Read(context.Background(), Repository{}, now.Add(-time.Hour), now); e != ErrObservation {
		t.Fatal("accepted zero mapping")
	}
	other := repo
	other.Name = "else"
	if _, e := c.Read(context.Background(), other, now.Add(-time.Hour), now); e != ErrObservation {
		t.Fatal("accepted changed scope")
	}
}
func TestGitHubObservationRetainsAbsentBaseline(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`) })
	now := time.Now()
	got, e := ObserveGitHub(context.Background(), c, []Repository{repo}, []Baseline{{repo.GarmID, Job{ID: 1, Status: "queued"}}}, now.Add(-time.Hour), now)
	if e != nil || len(got.Items) != 1 || !got.Items[0].MissingFromReader || !got.EnumerationComplete {
		t.Fatalf("%+v %v", got, e)
	}
}

func TestGitHubRejectsMixedAuthoritiesBeforeCredentials(t *testing.T) {
	roots := x509.NewCertPool()
	called := false
	resolve := func(context.Context, string) (string, error) { called = true; return "synthetic", nil }
	other := repo
	other.GarmID = "second"
	other.Host = "other.example"
	if _, err := NewGitHub("https://api.github.example", repo.Host, "ref", resolve, &tls.Config{RootCAs: roots}, []Repository{repo, other}); err != ErrObservation {
		t.Fatal("accepted mixed web authorities")
	}
	if _, err := NewGitHub("https://api.github.example", "other.example", "ref", resolve, &tls.Config{RootCAs: roots}, []Repository{repo}); err != ErrObservation {
		t.Fatal("accepted mismatched authority")
	}
	if called {
		t.Fatal("resolved credential during rejected configuration")
	}
}

func TestGitHubReadsOldActiveRunWithoutCreatedFilter(t *testing.T) {
	jobsCalls := 0
	states := map[string]bool{}
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/actions/runs" {
			status := r.URL.Query().Get("status")
			states[status] = true
			if status != "" && r.URL.Query().Get("created") != "" {
				t.Fatal("active backlog limited by creation age")
			}
			if status == "queued" || status == "in_progress" {
				fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":9,"repository":{"full_name":"owner/repo"}}]}`)
			} else {
				fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
			}
			return
		}
		jobsCalls++
		if r.URL.Path != "/repos/owner/repo/actions/runs/9/jobs" {
			t.Error("wrong run")
		}
		fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":90,"run_id":9,"status":"queued","labels":["linux"]}]}`)
	})
	jobs, e := readTest(c)
	if e != nil || len(jobs) != 1 || jobs[0].ID != 90 || jobsCalls != 1 || len(states) != 6 {
		t.Fatalf("%+v %v calls%d states%v", jobs, e, jobsCalls, states)
	}
}

func TestLookupMissingHistoryConservativeAndScoped(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/actions/jobs/1":
			fmt.Fprint(w, `{"id":1,"run_id":7,"status":"completed","labels":["linux"]}`)
		case "/repos/owner/repo/actions/jobs/2":
			fmt.Fprint(w, `{"id":2,"run_id":7,"status":"completed","labels":["windows"]}`)
		case "/repos/owner/repo/actions/jobs/3":
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	items := []Item{{RepositoryID: repo.GarmID, Job: Job{ID: 1, Status: "queued", Labels: []string{"Linux"}}, MissingFromReader: true}, {RepositoryID: repo.GarmID, Job: Job{ID: 2, Status: "queued", Labels: []string{"linux"}}, MissingFromReader: true}, {RepositoryID: repo.GarmID, Job: Job{ID: 3, Status: "queued", Labels: []string{"linux"}}, MissingFromReader: true}, {RepositoryID: repo.GarmID, Job: Job{ID: 4, Status: "queued"}, MissingFromReader: true}}
	past := map[string]Item{}
	for _, item := range items {
		past[key(item.RepositoryID, item.Job.ID)] = item
	}
	current := map[string]bool{key(repo.GarmID, 4): true}
	lookupMissingHistory(context.Background(), c, []Repository{repo}, items, past, current, "")
	if items[0].MissingFromReader || items[0].ReaderStatus != "completed" {
		t.Fatalf("completion not observed: %+v", items[0])
	}
	if !items[1].MissingFromReader || !items[1].Conflict {
		t.Fatalf("mismatch not preserved: %+v", items[1])
	}
	if !items[2].MissingFromReader || !items[3].MissingFromReader {
		t.Fatal("missing or stock item changed")
	}
}
func TestLookupJobRejectsWrongIdentityBeforeCreds(t *testing.T) {
	c := fixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
	c.resolve = func(context.Context, string) (string, error) { t.Fatal("unexpected credential"); return "", nil }
	other := repo
	other.Name = "other"
	if _, err := c.lookupJob(context.Background(), other, 1); err != ErrObservation {
		t.Fatal("accepted changed scope")
	}
}

func TestMissingHistoryCursorVisitsAllEvenWithLongGaps(t *testing.T) {
	calls := map[string]int{}
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls[r.URL.Path]++; w.WriteHeader(404) })
	items := make([]Item, 64)
	past := map[string]Item{}
	for i := range items {
		items[i] = Item{RepositoryID: repo.GarmID, Job: Job{ID: int64(i + 1), Status: "queued"}, MissingFromReader: true}
		past[key(repo.GarmID, int64(i+1))] = items[i]
	}
	cursor := lookupMissingHistory(context.Background(), c, []Repository{repo}, items, past, nil, "")
	if len(calls) != 32 {
		t.Fatalf("expected cap of 32, got %d", len(calls))
	}
	cursor = lookupMissingHistory(context.Background(), c, []Repository{repo}, items, past, nil, cursor)
	if len(calls) != 64 {
		t.Fatalf("cursor starved entries: got %d", len(calls))
	}
	if cursor == "" {
		t.Fatal("missing cursor")
	}
}

func TestLookupCancellationRetainsCursorAndHistory(t *testing.T) {
	c := fixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("cancelled lookup contacted server") })
	items := []Item{{RepositoryID: repo.GarmID, Job: Job{ID: 1, Status: "queued"}, MissingFromReader: true}}
	past := map[string]Item{key(repo.GarmID, 1): items[0]}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := lookupMissingHistory(ctx, c, []Repository{repo}, items, past, nil, "old"); got != "old" || !items[0].MissingFromReader {
		t.Fatal("cancel changed observation")
	}
}
