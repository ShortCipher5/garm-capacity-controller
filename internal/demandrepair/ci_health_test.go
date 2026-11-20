package demandrepair

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const ciRepoID = 9007199254740993
const ciRunID = 9007199254740995
const ciFirstJobID = 9007199254740997
const ciFixtureAttempt = "/repos/owner/repo/actions/runs/9007199254740995/attempts/2"

func ciTestSource(t *testing.T, h http.HandlerFunc) *CIHealthSource {
	t.Helper()
	c := fixture(t, h)
	s, err := NewCIHealthSource(c, []CIRepository{{Repository: repo, GitHubID: ciRepoID}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func ciRunJSON(repoID int64) string {
	return fmt.Sprintf(`{"id":%d,"workflow_id":10,"run_attempt":2,"repository":{"id":%d,"full_name":"owner/repo"},"head_repository":{"id":57,"full_name":"different/fork"},"name":"Build","status":"in_progress","conclusion":null,"run_started_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T01:00:00Z","html_url":"https://example.invalid/not-followed"}`, ciRunID, repoID)
}
func TestCIHealthReadExactAttemptAndJobPages(t *testing.T) {
	ctx := context.Background()
	s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-test-token" ||
			r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("bad read")
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/repos/owner/repo/actions/runs":
			if q.Get("page") != "1" || q.Get("per_page") != "50" || q.Get("status") != "in_progress" || q.Has("created") {
				t.Errorf("bad active query: %s", r.URL)
			}
			fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[%s]}`, ciRunJSON(ciRepoID))
		case ciFixtureAttempt:
			io.WriteString(w, ciRunJSON(ciRepoID))
		case ciFixtureAttempt + "/jobs":
			if q.Get("per_page") != "50" || q.Has("filter") {
				t.Errorf("bad attempt jobs query: %s", r.URL)
			}
			if q.Get("page") == "1" {
				jobs := []map[string]any{}
				for i := range 50 {
					jobs = append(jobs, map[string]any{"id": int64(ciFirstJobID) + int64(i), "run_id": int64(ciRunID), "name": "Matrix name collision", "status": "queued", "runner_id": nil})
				}
				json.NewEncoder(w).Encode(map[string]any{"total_count": 51, "jobs": jobs})
			} else if q.Get("page") == "2" {
				fmt.Fprintf(w, `{"total_count":51,"jobs":[{"id":%d,"run_id":%d,"run_attempt":2,"name":"Matrix name collision","status":"completed","conclusion":"skipped","runner_id":0,"started_at":null,"completed_at":null,"steps":[{"number":7,"name":"Run echo fixture-command-should-not-appear","status":"completed","conclusion":"skipped"},{"number":2,"name":"Named step","status":"completed","conclusion":"success","started_at":"2026-01-01T01:02:00+01:00","completed_at":"2026-01-01T00:02:15Z"}],"log":"fixture-log-should-not-appear"}]}`, ciFirstJobID+50, ciRunID)
			} else {
				t.Error("unexpected page")
			}
		default:
			t.Errorf("unexpected read: %s", r.URL)
			http.NotFound(w, r)
		}
	})
	list, err := s.ListCIRuns(ctx, repo.GarmID, CIRunQuery{Status: "in_progress", Page: 1})
	if err != nil || len(list.Runs) != 1 || list.NextPage != 0 || list.ObservedAt.IsZero() {
		t.Fatalf("run list: %+v %v", list, err)
	}
	run := list.Runs[0]
	if run.RunID != ciRunID || run.Attempt != 2 || run.StartedAt == nil || run.SourceURL != "https://"+repo.Host+"/owner/repo/actions/runs/9007199254740995/attempts/2" {
		t.Fatalf("run scope/link: %+v", run)
	}
	a, err := s.ReadCIAttempt(ctx, repo.GarmID, run.RunID, run.Attempt)
	if err != nil || a.RepositoryID() != ciRepoID || a.ObservedAt().IsZero() {
		t.Fatalf("attempt: %+v %v", a, err)
	}
	*a.Run().StartedAt = time.Time{}
	if a.Run().StartedAt.IsZero() {
		t.Fatal("caller modified minted attempt")
	}
	one, err := s.ListCIAttemptJobs(ctx, a, 1)
	if err != nil || len(one.Jobs) != 50 || one.NextPage != 2 || one.Jobs[0].ID != ciFirstJobID ||
		one.Jobs[0].RunAttempt != 2 || one.Jobs[0].Name != one.Jobs[1].Name || one.Jobs[0].RunnerID != 0 || one.Jobs[0].StartedAt != nil {
		t.Fatalf("first page: jobs=%d next=%d err=%v", len(one.Jobs), one.NextPage, err)
	}
	two, err := s.ListCIAttemptJobs(ctx, a, 2)
	if err != nil || len(two.Jobs) != 1 || two.NextPage != 0 || two.ObservedAt.Before(a.ObservedAt()) {
		t.Fatalf("second page: %+v %v", two, err)
	}
	job := two.Jobs[0]
	if job.ID != ciFirstJobID+50 || job.RunAttempt != 2 || job.Conclusion != "skipped" || job.RunnerID != 0 || job.StartedAt != nil || len(job.Steps) != 2 ||
		job.Steps[0].Number != 2 || job.Steps[0].StartedAt == nil || job.Steps[0].CompletedAt.Sub(*job.Steps[0].StartedAt) != 15*time.Second ||
		job.Steps[1].Number != 7 || job.Steps[1].StartedAt != nil || job.Steps[1].Name != "Run step (name withheld)" {
		t.Fatalf("job: %+v", job)
	}
	if got := fmt.Sprintf("%+v", job); strings.Contains(got, "fixture-log") || strings.Contains(got, "fixture-command") || strings.Contains(got, "example.invalid") {
		t.Fatalf("unexpected data retained: %v", got)
	}
}

func TestCIHealthFailsClosedOnScopeAndAmbiguousRemoteResponses(t *testing.T) {
	ctx := context.Background()
	good := ciRunJSON(ciRepoID)
	for _, test := range []struct {
		name  string
		code  int
		reply string
	}{
		{name: "same slug wrong immutable repo", reply: ciRunJSON(ciRepoID + 1)},
		{name: "wrong slug", reply: strings.Replace(good, "owner/repo", "other/repo", 1)},
		{name: "wrong attempt", reply: strings.Replace(good, `"run_attempt":2`, `"run_attempt":3`, 1)},
		{name: "duplicate id", reply: strings.Replace(good, "{", fmt.Sprintf(`{"ID":%d,`, ciRunID), 1)},
		{name: "case-aliased id", reply: strings.Replace(good, `"id"`, `"ID"`, 1)},
		{name: "Unicode aliased status", reply: strings.Replace(good, `"status"`, `"\u017ftatus"`, 1)},
		{name: "Unicode duplicate extension key", reply: strings.Replace(good, "{", `{"stub":"x","ſtub":"y",`, 1)},
		{name: "invalid UTF8 in ignored field", reply: strings.Replace(good, "{", "{\"ignored\":\"\xff\",", 1)},
		{name: "nested duplicate id", reply: strings.Replace(good, `"repository":{`, `"repository":{"id":1,`, 1)},
		{name: "missing scope", reply: strings.Replace(good, `"repository"`, `"repository_elsewhere"`, 1)},
		{name: "not authorized", code: http.StatusForbidden},
		{name: "too many requests", code: http.StatusTooManyRequests},
		{name: "redirect", code: http.StatusFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != ciFixtureAttempt {
					t.Error("unexpected or redirected read", r.URL.Path)
				}
				if test.code != 0 {
					w.Header().Set("Location", "/unexpected")
					w.WriteHeader(test.code)
				}
				io.WriteString(w, test.reply)
			})
			if got, err := s.ReadCIAttempt(ctx, repo.GarmID, ciRunID, 2); err != ErrObservation || got.RepositoryID() != 0 {
				t.Fatalf("returned partial attempt %+v, %v", got, err)
			}
		})
	}
}

func TestCIEmptyRunPageRequiresCurrentRepositoryPin(t *testing.T) {
	ctx := context.Background()
	for _, current := range []int64{ciRepoID, ciRepoID + 1} {
		s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/owner/repo/actions/runs":
				io.WriteString(w, `{"total_count":0,"workflow_runs":[]}`)
			case "/repos/owner/repo":
				fmt.Fprintf(w, `{"id":%d,"full_name":"owner/repo"}`, current)
			default:
				t.Error("unexpected read", r.URL.Path)
			}
		})
		out, err := s.ListCIRuns(ctx, repo.GarmID, CIRunQuery{Status: "queued", Page: 1})
		if current != ciRepoID {
			if err != ErrObservation || !out.ObservedAt.IsZero() {
				t.Fatalf("replacement repo confirmed empty: %+v %v", out, err)
			}
		} else if err != nil || out.Runs == nil || len(out.Runs) != 0 || out.ObservedAt.IsZero() {
			t.Fatalf("enrolled repo empty: %+v %v", out, err)
		}
	}
}
func TestCIAttemptCannotWidenScopeAndExpires(t *testing.T) {
	ctx := context.Background()
	s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ciFixtureAttempt {
			t.Error("unexpected read", r.URL.Path)
		}
		io.WriteString(w, ciRunJSON(ciRepoID))
	})
	a, err := s.ReadCIAttempt(ctx, repo.GarmID, ciRunID, 2)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := NewCIHealthSource(s.client, s.Repositories())
	if err != nil {
		t.Fatal(err)
	}
	s.client.resolve = func(context.Context, string) (string, error) {
		t.Error("credential requested for unqualified operation")
		return "", ErrObservation
	}
	wrong := repo
	wrong.Name = "not-enrolled"
	if _, err := NewCIHealthSource(s.client, []CIRepository{{Repository: wrong, GitHubID: ciRepoID}}); err != ErrObservation {
		t.Fatal("allowed a changed tuple")
	}
	if _, err := shadow.ListCIAttemptJobs(ctx, a, 1); err != ErrObservation {
		t.Fatal("source-crossed attempt")
	}
	if _, err := s.ListCIAttemptJobs(ctx, CIAttempt{}, 1); err != ErrObservation {
		t.Fatal("unminted attempt")
	}
	a.at = time.Now().Add(-ciAttemptLifetime)
	if _, err := s.ListCIAttemptJobs(ctx, a, 1); err != ErrObservation {
		t.Fatal("stale attempt")
	}
	if _, err := s.ReadCIAttempt(ctx, "unknown", ciRunID, 2); err != ErrObservation {
		t.Fatal("unknown repo")
	}
	if _, err := s.ListCIRuns(ctx, repo.GarmID, CIRunQuery{Page: CIMaxPages + 1, Status: "queued"}); err != ErrObservation {
		t.Fatal("oversized page")
	}
}
func TestCIRunPageBoundsAndRepoOrdering(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/actions/runs" || r.URL.Query().Get("created") == "" {
			t.Error("unexpected path or missing time window", r.URL)
		}
		if r.URL.Query().Get("page") == "1" {
			io.WriteString(w, `{"total_count":1000,"workflow_runs":[]}`)
		} else {
			for range 1000 {
				io.WriteString(w, strings.Repeat(" ", 5<<10))
			}
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	repo2 := repo
	repo2.GarmID, repo2.Name = "other-garm-id", "repo-two"
	c, err := NewGitHub(server.URL, repo.Host, "fixture", func(context.Context, string) (string, error) {
		return "test", nil
	}, &tls.Config{RootCAs: roots}, []Repository{repo, repo2})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	scope := []CIRepository{{Repository: repo, GitHubID: ciRepoID}, {Repository: repo2, GitHubID: 1}}
	if _, err := NewCIHealthSource(c, []CIRepository{scope[0], {Repository: repo, GitHubID: 1}}); err != ErrObservation {
		t.Fatal("duplicate Garm locator with different immutable pins")
	}
	if _, err := NewCIHealthSource(c, []CIRepository{scope[0], {Repository: repo2, GitHubID: ciRepoID}}); err != ErrObservation {
		t.Fatal("duplicate immutable pin with different Garm locators")
	}
	s, err := NewCIHealthSource(c, scope)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Repositories()
	if len(got) != 2 || got[0].GitHubID != 1 || got[1].GitHubID != ciRepoID || s.SourceID() == "" {
		t.Fatalf("scope ordering: %+v", got)
	}
	got[0].GitHubID = 10
	scope[0].GitHubID = 10
	if check := s.Repositories(); check[0].GitHubID != 1 || check[1].GitHubID != ciRepoID {
		t.Fatalf("scope mutation: %+v", check)
	}
	now := time.Now().UTC()
	for _, page := range []int{1, 2} {
		out, err := s.ListCIRuns(ctx, repo.GarmID, CIRunQuery{Page: page, From: now.Add(-time.Hour), To: now})
		if err != ErrObservation || out.Runs != nil || !out.ObservedAt.IsZero() {
			t.Fatalf("partial capped/oversized page: %+v %v", out, err)
		}
	}
}

func TestCIRunPageValidatesExactReturnedFilter(t *testing.T) {
	ctx := context.Background()
	from := time.Date(2026, 1, 1, 8, 30, 1, 900_000_000, time.FixedZone("fixture", 8*3600))
	to := from.Add(2*time.Second + 90*time.Millisecond)
	const wantWindow = "2026-01-01T00:30:01Z..2026-01-01T00:30:03Z"
	good := ciRunJSON(ciRepoID)
	withCreation := func(at string) string {
		return strings.Replace(good, "{", fmt.Sprintf(`{"created_at":%q,`, at), 1)
	}
	for _, tc := range []struct {
		name, active, response string
		valid                  bool
	}{
		{name: "active exact match", active: "in_progress", response: good, valid: true},
		{name: "active status moved", active: "queued", response: good},
		{name: "historical start second", response: withCreation("2026-01-01T08:30:01+08:00"), valid: true},
		{name: "historical end second", response: withCreation("2026-01-01T00:30:03.999999999Z"), valid: true},
		{name: "historical before", response: withCreation("2026-01-01T00:30:00.999999999Z")},
		{name: "historical after", response: withCreation("2026-01-01T00:30:04Z")},
		{name: "historical time absent", response: good},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/actions/runs" {
					t.Error("unexpected request path", r.URL.Path)
				}
				q := r.URL.Query()
				if tc.active != "" {
					if q.Get("status") != tc.active || q.Has("created") {
						t.Error("unexpected active query", r.URL)
					}
				} else if q.Get("created") != wantWindow || q.Has("status") {
					t.Error("unexpected historical query", r.URL)
				}
				fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[%s]}`, tc.response)
			})
			q := CIRunQuery{Page: 1, Status: tc.active}
			if tc.active == "" {
				q.From, q.To = from, to
			}
			got, err := s.ListCIRuns(ctx, repo.GarmID, q)
			if tc.valid {
				if err != nil || len(got.Runs) != 1 || got.ObservedAt.IsZero() {
					t.Fatalf("valid result denied: %+v %v", got, err)
				}
			} else if err != ErrObservation || got.Runs != nil || !got.ObservedAt.IsZero() {
				t.Fatalf("out of scope result credited: %+v %v", got, err)
			}
		})
	}
}
func TestCIJobPageRejectsCrossAttemptRunDuplicateAndPartial(t *testing.T) {
	ctx := context.Background()
	for _, jobPayload := range []string{
		`{"total_count":2,"jobs":[{"id":1,"run_id":9007199254740995,"run_attempt":2,"status":"queued"}]}`,
		`{"total_count":1,"jobs":[{"id":1,"run_id":9007199254740995,"run_attempt":1,"status":"queued"}]}`,
		`{"total_count":1,"jobs":[{"id":1,"run_id":9007199254740994,"run_attempt":2,"status":"queued"}]}`,
		`{"total_count":2,"jobs":[{"id":1,"run_id":9007199254740995,"status":"queued"},{"id":1,"run_id":9007199254740995,"status":"queued"}]}`,
		`{"total_count":1,"jobs":[{"id":1,"run_id":9007199254740995,"status":"queued","steps":[{"number":4,"status":"queued"},{"number":4,"status":"queued"}]}]}`,
	} {
		s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == ciFixtureAttempt {
				io.WriteString(w, ciRunJSON(ciRepoID))
				return
			}
			io.WriteString(w, jobPayload)
		})
		a, err := s.ReadCIAttempt(ctx, repo.GarmID, ciRunID, 2)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ListCIAttemptJobs(ctx, a, 1)
		if err != ErrObservation || got.Jobs != nil {
			t.Fatalf("invalid page accepted: %+v %v", got, err)
		}
	}
}
