package cihealthsource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func cycleRepos() []demandrepair.CIRepository {
	out := make([]demandrepair.CIRepository, 0, 2)
	for i, name := range []string{"runners", "another"} {
		out = append(out, demandrepair.CIRepository{
			Repository: demandrepair.Repository{GarmID: name, Host: "github.example", Owner: "fixture", Name: name},
			GitHubID:   repoID + int64(i),
		})
	}
	return out
}

func repoMetadata(repo demandrepair.CIRepository) map[string]any {
	return map[string]any{"id": repo.GitHubID, "full_name": repo.Owner + "/" + repo.Name}
}

func cycleRun(repo demandrepair.CIRepository, run, attempt int64, status string, date time.Time) map[string]any {
	out := map[string]any{
		"id": run, "workflow_id": workflowID, "run_attempt": attempt, "repository": repoMetadata(repo),
		"status": status, "created_at": date,
	}
	if status == "completed" {
		out["conclusion"] = "success"
	}
	return out
}

func runPage(w http.ResponseWriter, rows []map[string]any, total int) {
	if rows == nil {
		rows = []map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": rows, "total_count": total})
}

func jobPage(w http.ResponseWriter, run, attempt int64, status string) {
	row := map[string]any{"id": jobID, "run_id": run, "run_attempt": attempt, "status": status}
	if status == "completed" {
		row["conclusion"] = "success"
		row["started_at"] = "2026-01-02T01:00:00Z"
		row["completed_at"] = "2026-01-02T02:00:00Z"
		row["steps"] = []map[string]any{{"number": 9, "status": "completed", "conclusion": "success", "started_at": "2026-01-02T01:00:00Z", "completed_at": "2026-01-02T01:10:00Z"}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{row}, "total_count": 1})
}

func relativeWindow() (time.Time, time.Time) {
	to := time.Now().UTC().Truncate(time.Second)
	return to.Add(-time.Hour), to
}

func TestReadCycleSelectsOnlyEnrolledReposAndPreservesReruns(t *testing.T) {
	from, to := relativeWindow()
	repos := cycleRepos()
	created := to.Add(-time.Minute)
	older := from.Add(-48 * time.Hour)
	var onlyAnother atomic.Bool
	s := fixtureRepos(t, repos, func(w http.ResponseWriter, r *http.Request) {
		repo := repos[0]
		if strings.HasPrefix(r.URL.Path, "/repos/fixture/another") {
			repo = repos[1]
		}
		if onlyAnother.Load() && repo != repos[1] {
			t.Error("unselected enrolled repository was read")
		}
		base := "/repos/fixture/" + repo.Name
		run := runID + repo.GitHubID - repoID
		oldRun := run + 100
		switch {
		case r.URL.Path == base:
			_ = json.NewEncoder(w).Encode(repoMetadata(repo))
		case r.URL.Path == base+"/actions/runs":
			switch r.URL.Query().Get("status") {
			case "queued":
				if r.URL.Query().Has("created") {
					t.Error("old active reruns filtered by original run creation")
				}
				rows := []map[string]any{cycleRun(repo, run, 2, "queued", created)}
				if repo == repos[0] {
					rows = append(rows, cycleRun(repo, oldRun, 2, "queued", older))
				}
				runPage(w, rows, len(rows))
			case "pending":
				// A run can move between separately observed active filters:
				// the same attempt must be read once, not counted twice.
				runPage(w, []map[string]any{cycleRun(repo, run, 2, "pending", created)}, 1)
			case "":
				if !r.URL.Query().Has("created") {
					t.Error("missing bounded recent filter")
				}
				// Different exact attempt of the same run remains independent.
				runPage(w, []map[string]any{cycleRun(repo, run, 1, "completed", created)}, 1)
			default:
				runPage(w, nil, 0)
			}
		case strings.HasPrefix(r.URL.Path, base+"/actions/runs/"):
			suffix := strings.Split(strings.TrimPrefix(r.URL.Path, base+"/actions/runs/"), "/")
			if len(suffix) != 3 && len(suffix) != 4 {
				t.Error("not an exact attempt path")
				w.WriteHeader(404)
				return
			}
			target, err := strconv.ParseInt(suffix[0], 10, 64)
			if err != nil || target != run && (repo != repos[0] || target != oldRun) ||
				suffix[1] != "attempts" || suffix[2] != "1" && suffix[2] != "2" {
				t.Error("wrong exact attempt target")
			}
			attempt := int64(1)
			status := "completed"
			if suffix[2] == "2" {
				attempt, status = 2, "queued"
			}
			if len(suffix) == 3 {
				date := created
				if target == oldRun {
					date = older
				}
				_ = json.NewEncoder(w).Encode(cycleRun(repo, target, attempt, status, date))
			} else if suffix[3] == "jobs" {
				jobPage(w, target, attempt, status)
			} else {
				t.Error("not an exact job page")
			}
		default:
			t.Error("out of scope call")
			w.WriteHeader(404)
		}
	})
	got, err := ReadCycle(t.Context(), s, []string{"another", "runners"}, from, to)
	facts := got.Facts()
	if err != nil || got.State() != CycleObserved || len(facts) != 5 || got.ObservedAt().Before(to) || got.ObservedAt().After(time.Now()) {
		t.Fatalf("unexpected cycle state/rows: %v %d %v", got.State(), len(facts), err)
	}
	wantRepos := []int64{repoID, repoID + 1, repoID, repoID, repoID + 1}
	wantRuns := []int64{runID, runID + 1, runID + 100, runID, runID + 1}
	wantAttempts := []int64{2, 2, 2, 1, 1}
	for i, f := range facts {
		if f.Key.RepositoryID != wantRepos[i] || f.Key.Attempt != wantAttempts[i] ||
			f.Key.RunID != wantRuns[i] || len(f.Jobs) != 1 || f.AsOf.After(got.ObservedAt()) ||
			f.Jobs[0].Key.Attempt != f.Key || f.Jobs[0].Key.JobID != jobID ||
			!f.Jobs[0].QueuedAt.IsZero() || !f.Jobs[0].FirstQueuedObservedAt.IsZero() {
			t.Fatalf("wrong ordered fact: %v", f.Key)
		}
	}
	facts[3].Jobs[0].Steps[0].Key.Number = 400
	facts[3].Jobs[0].Key.JobID = 1
	if f := got.Facts()[3]; f.Jobs[0].Steps[0].Key.Number != 9 || f.Jobs[0].Key.JobID != jobID {
		t.Fatal("caller mutated owned snapshot")
	}
	onlyAnother.Store(true)
	one, err := ReadCycle(t.Context(), s, []string{"another"}, from, to)
	if err != nil || len(one.Facts()) != 2 || one.Facts()[0].Key.RepositoryID != repoID+1 {
		t.Fatalf("subset failed: %v", err)
	}
}

func TestReadCycleBoundsNoisyRepoAndFailsClosedOnFreshError(t *testing.T) {
	from, to := relativeWindow()
	repos := cycleRepos()
	var bad atomic.Bool
	var reads atomic.Int32
	var quietDetails atomic.Int32
	s := fixtureRepos(t, repos, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		repo := repos[0]
		if strings.HasPrefix(r.URL.Path, "/repos/fixture/another") {
			repo = repos[1]
		}
		base := "/repos/fixture/" + repo.Name
		switch {
		case r.URL.Path == base:
			_ = json.NewEncoder(w).Encode(repoMetadata(repo))
		case r.URL.Path == base+"/actions/runs":
			q := r.URL.Query()
			if q.Get("status") != "queued" {
				runPage(w, nil, 0)
				return
			}
			if repo == repos[1] {
				runPage(w, []map[string]any{cycleRun(repo, runID, 2, "queued", from)}, 1)
				return
			}
			// This continues beyond the second locally affordable page.
			page, _ := strconv.Atoi(q.Get("page"))
			rows := make([]map[string]any, 0, demandrepair.CIPerPage)
			for i := range demandrepair.CIPerPage {
				rows = append(rows, cycleRun(repo, runID+int64((page-1)*demandrepair.CIPerPage+i), 2, "queued", from))
			}
			runPage(w, rows, 2*demandrepair.CIPerPage+1)
		case strings.HasPrefix(r.URL.Path, base+"/actions/runs/"):
			suffix := strings.Split(strings.TrimPrefix(r.URL.Path, base+"/actions/runs/"), "/")
			run, _ := strconv.ParseInt(suffix[0], 10, 64)
			if len(suffix) == 3 {
				if repo == repos[1] {
					quietDetails.Add(1)
				}
				_ = json.NewEncoder(w).Encode(cycleRun(repo, run, 2, "queued", from))
			} else {
				if repo == repos[1] && bad.Load() {
					w.WriteHeader(429)
					return
				}
				jobPage(w, run, 2, "queued")
			}
		default:
			t.Error("out of scope read")
			w.WriteHeader(404)
		}
	})
	names := []string{"runners", "another"}
	partial, err := ReadCycle(t.Context(), s, names, from, to)
	if err != nil || partial.State() != CyclePartial || quietDetails.Load() != 1 ||
		len(partial.Facts()) != maxCycleAttempts/2+1 || reads.Load() > maxCycleHTTPReads {
		t.Fatalf("noise starved quiet repo or escaped budget: %d %d %d %v", partial.State(), len(partial.Facts()), reads.Load(), err)
	}
	bad.Store(true)
	got, err := ReadCycle(t.Context(), s, names, from, to)
	if !errors.Is(err, ErrCycleObservation) || got.State() != CycleUnavailable || got.Facts() != nil || !got.ObservedAt().IsZero() {
		t.Fatalf("failed pass exposed previous facts: %v %v", got.State(), err)
	}
	before := reads.Load()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tt := range []struct {
		ctx   context.Context
		names []string
	}{{t.Context(), []string{"foreign"}}, {t.Context(), []string{"runners", "runners"}}, {cancelled, names}} {
		got, err := ReadCycle(tt.ctx, s, tt.names, from, to)
		if !errors.Is(err, ErrCycleObservation) || got.State() != CycleUnavailable || reads.Load() != before {
			t.Fatalf("invalid request proceeded: %v", err)
		}
	}
}

func TestReadAttemptFactsIsOwnedCopy(t *testing.T) {
	original := Observation{facts: cihealth.AttemptFacts{Jobs: []cihealth.JobFacts{
		{Steps: []cihealth.StepFacts{{Key: cihealth.StepKey{Number: 7}}}},
	}}}
	f := original.Facts()
	f.Jobs[0].Steps[0].Key.Number = 100
	if original.Facts().Jobs[0].Steps[0].Key.Number != 7 {
		t.Fatal("caller changed bridge facts")
	}
}
