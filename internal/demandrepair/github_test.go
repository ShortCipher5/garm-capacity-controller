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
	c, e := NewGitHub(server.URL, "test-ref", func(_ context.Context, ref string) (string, error) {
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
	if e != nil || len(got) != 101 || calls != 3 {
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
