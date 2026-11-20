package cihealthsource

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

const (
	repoID, workflowID, runID, jobID int64 = 9007199254740993, 9007199254740994, 9007199254740995, 9007199254740997
	scope                                  = "fixture-repo"
	attemptPath                            = "/repos/fixture/runners/actions/runs/9007199254740995/attempts/2"
)

func fixture(t *testing.T, h http.HandlerFunc) *demandrepair.CIHealthSource {
	t.Helper()
	repo := demandrepair.Repository{GarmID: scope, Host: "github.example", Owner: "fixture", Name: "runners"}
	return fixtureRepos(t, []demandrepair.CIRepository{{Repository: repo, GitHubID: repoID}}, h)
}

func fixtureRepos(t *testing.T, repos []demandrepair.CIRepository, h http.HandlerFunc) *demandrepair.CIHealthSource {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Errorf("unexpected method or credential scope")
			w.WriteHeader(401)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	rawRepos := make([]demandrepair.Repository, len(repos))
	for i := range repos {
		rawRepos[i] = repos[i].Repository
	}
	client, err := demandrepair.NewGitHub(s.URL, repos[0].Host, "fixture-ref", func(context.Context, string) (string, error) {
		return "synthetic-token", nil
	}, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, rawRepos)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	source, err := demandrepair.NewCIHealthSource(client, repos)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func runResponse(w http.ResponseWriter, observedRepo int64, status, conclusion string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": runID, "workflow_id": workflowID, "run_attempt": 2,
		"repository": map[string]any{"id": observedRepo, "full_name": "fixture/runners"},
		"name":       "ignored-private-workflow-label", "status": status, "conclusion": conclusion,
		"run_started_at": "2026-01-02T00:00:00Z", "created_at": "2025-01-01T00:00:00Z", "updated_at": "2026-01-03T00:00:00Z",
	})
}

func job(id int64, status string) map[string]any {
	return map[string]any{
		"id": id, "run_id": runID, "name": "ignored-matrix-label", "status": status,
		"runner_id": 10, "runner_name": "ignored-runner-identity",
		"started_at": "2026-01-02T01:00:00Z", "created_at": "2025-01-01T00:00:00Z",
	}
}

func TestReadAttemptKeepsExactAncestryAndPageTiming(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path+"?"+r.URL.Query().Get("page"))
		mu.Unlock()
		switch r.URL.Path {
		case attemptPath:
			runResponse(w, repoID, "in_progress", "")
		case attemptPath + "/jobs":
			q := r.URL.Query()
			if q.Get("per_page") != strconv.Itoa(demandrepair.CIPerPage) || q.Has("filter") {
				t.Error("unbounded or different job query")
			}
			jobs := []map[string]any{}
			switch q.Get("page") {
			case "1":
				for i := range demandrepair.CIPerPage {
					jobs = append(jobs, job(jobID+int64(i), "in_progress"))
				}
				jobs[0]["steps"] = []map[string]any{
					{"number": 8, "name": "Run ignored-command", "status": "completed", "conclusion": "success", "started_at": "2026-01-02T01:02:00Z", "completed_at": "2026-01-02T01:02:12Z"},
					{"number": 11, "status": "completed", "conclusion": "skipped"},
				}
			case "2":
				jobs = append(jobs, job(jobID+int64(demandrepair.CIPerPage), "in_progress"))
			default:
				t.Error("unexpected page")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": demandrepair.CIPerPage + 1, "jobs": jobs})
		default:
			t.Error("unexpected request path")
			w.WriteHeader(404)
		}
	})
	obs, err := ReadAttempt(t.Context(), s, scope, runID, 2)
	if err != nil || len(obs.Timing.Jobs) != demandrepair.CIPerPage+1 ||
		obs.Timing.Key != (cihealth.AttemptKey{Source: s.SourceID(), RepositoryID: repoID, WorkflowID: workflowID, RunID: runID, Attempt: 2}) {
		t.Fatalf("unexpected attempt: %+v / %v", obs, err)
	}
	mu.Lock()
	gotRequests := strings.Join(requests, ",")
	mu.Unlock()
	if want := strings.Join([]string{attemptPath + "?", attemptPath + "/jobs?1", attemptPath + "/jobs?2"}, ","); gotRequests != want {
		t.Fatalf("unexpected read sequence: %q", gotRequests)
	}
	start := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
	first, last := obs.Timing.Jobs[0], obs.Timing.Jobs[len(obs.Timing.Jobs)-1]
	if first.Key.JobID != jobID || last.Key.JobID != jobID+int64(demandrepair.CIPerPage) ||
		first.Key.Attempt != obs.Timing.Key || last.Key.Attempt != obs.Timing.Key ||
		first.Active.Basis != cihealth.ObservedEndpoints || last.Active.Duration != obs.Timing.AsOf.Sub(start) ||
		first.Active.Duration >= last.Active.Duration || first.Queue != (cihealth.Span{}) || first.ActiveQueue != (cihealth.Span{}) {
		t.Fatalf("page attribution lost: first=%+v last=%+v", first, last)
	}
	if first.Steps[0].Key != (cihealth.StepKey{Job: first.Key, Number: 8}) || first.Steps[0].Duration.Duration != 12*time.Second ||
		first.Steps[1].Phase != cihealth.Skipped || first.Steps[1].Duration != (cihealth.Span{}) ||
		obs.Timing.Wall != (cihealth.Span{}) || obs.Timing.Active.Basis != cihealth.ObservedEndpoints ||
		obs.Timing.Active.Duration >= obs.Timing.AsOf.Sub(start.Add(-time.Hour)) {
		t.Fatalf("wrong execution clocks: %+v", obs)
	}
	if out := fmt.Sprintf("%+v", obs); strings.Contains(out, "ignored-") || strings.Contains(out, "github.example") || strings.Contains(out, "synthetic-token") {
		t.Fatal("source internals escaped projection")
	}
}

func TestReadAttemptCompletedButUnknownWallAndQueue(t *testing.T) {
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == attemptPath {
			runResponse(w, repoID, "completed", "success")
			return
		}
		if r.URL.Path != attemptPath+"/jobs" {
			t.Error("unexpected request")
			w.WriteHeader(404)
			return
		}
		j := job(jobID, "completed")
		j["completed_at"] = "2026-01-02T01:02:00Z"
		j["conclusion"] = "success"
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "jobs": []any{j}})
	})
	got, err := ReadAttempt(t.Context(), s, scope, runID, 2)
	if err != nil || got.Conclusion != "success" || got.Timing.Phase != cihealth.Completed ||
		got.Timing.Wall != (cihealth.Span{}) || got.Timing.Active != (cihealth.Span{}) || len(got.Timing.Jobs) != 1 ||
		got.Timing.Jobs[0].Queue != (cihealth.Span{}) ||
		got.Timing.Jobs[0].Execution != (cihealth.Span{Basis: cihealth.ObservedEndpoints, Duration: 2 * time.Minute}) {
		t.Fatalf("completion inferred from aggregate timestamps: %+v / %v", got, err)
	}
}

func TestReadAttemptRejectsWrongRepositoryOrCrossPageDuplicate(t *testing.T) {
	for _, badRepo := range []bool{false, true} {
		t.Run(fmt.Sprint(badRepo), func(t *testing.T) {
			s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case attemptPath:
					if badRepo {
						runResponse(w, repoID+1, "in_progress", "")
					} else {
						runResponse(w, repoID, "in_progress", "")
					}
				case attemptPath + "/jobs":
					if badRepo {
						t.Error("foreign proof reached job pagination")
					}
					jobs := []map[string]any{job(jobID, "in_progress")}
					if r.URL.Query().Get("page") == "1" {
						for i := 1; i < demandrepair.CIPerPage; i++ {
							jobs = append(jobs, job(jobID+int64(i), "in_progress"))
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"total_count": demandrepair.CIPerPage + 1, "jobs": jobs})
				default:
					t.Error("unexpected request")
					w.WriteHeader(404)
				}
			})
			got, err := ReadAttempt(t.Context(), s, scope, runID, 2)
			if !errors.Is(err, ErrAttemptObservation) || len(got.Timing.Jobs) != 0 || got.Timing.Key != (cihealth.AttemptKey{}) {
				t.Fatalf("partial or wrong scope escaped: %+v / %v", got, err)
			}
		})
	}
}
