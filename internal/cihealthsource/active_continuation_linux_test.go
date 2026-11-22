package cihealthsource

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func TestActiveContinuationRechecksExactAttemptOnlyWithinSpareBudget(t *testing.T) {
	for _, mode := range []string{"spare", "saturated", "foreign", "expired", "rate limited", "wrong workflow", "already found", "still active", "newer rerun", "page budget"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			_, to := relativeWindow()
			from := to.Add(-24 * time.Hour)
			old, recent := to.Add(-28*time.Hour), to.Add(-time.Minute)
			allRepos := cycleRepos()
			repo := allRepos[0]
			selected := []string{repo.GarmID}
			const previousRun = runID + 100
			const base = "/repos/fixture/runners"
			var second atomic.Bool
			var oldRead, normalReads, freshJobPages atomic.Int32
			source, client := fixtureReposWithClient(t, allRepos, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == base:
					_ = json.NewEncoder(w).Encode(repoMetadata(repo))
				case r.URL.Path == base+"/actions/runs":
					status := r.URL.Query().Get("status")
					switch {
					case !second.Load() && status == "in_progress":
						if r.URL.Query().Has("created") {
							t.Error("original run age filtered out active rerun")
						}
						runPage(w, []map[string]any{cycleRun(repo, previousRun, 2, "in_progress", old)}, 1)
					case second.Load() && status == "in_progress" && (mode == "already found" || mode == "newer rerun"):
						attempt := int64(2)
						if mode == "newer rerun" {
							attempt = 3 // Independently discovered, not an inferred attempt.
						}
						runPage(w, []map[string]any{cycleRun(repo, previousRun, attempt, "in_progress", old)}, 1)
					case second.Load() && status == "":
						if !r.URL.Query().Has("created") {
							t.Error("normal recent filter removed")
						}
						n := 1
						if mode == "saturated" {
							n = maxCycleAttempts
						}
						rows := make([]map[string]any, 0, n)
						for i := 0; i < n; i++ {
							rows = append(rows, cycleRun(repo, runID+int64(i), 1, "completed", recent))
						}
						runPage(w, rows, n)
					default:
						runPage(w, nil, 0)
					}
				case strings.HasPrefix(r.URL.Path, base+"/actions/runs/"):
					parts := strings.Split(strings.TrimPrefix(r.URL.Path, base+"/actions/runs/"), "/")
					if (len(parts) != 3 && len(parts) != 4) || parts[1] != "attempts" || (len(parts) == 4 && parts[3] != "jobs") {
						t.Error("unexpected attempt request")
						w.WriteHeader(404)
						return
					}
					run, err := strconv.ParseInt(parts[0], 10, 64)
					attempt, attemptErr := strconv.ParseInt(parts[2], 10, 64)
					isPrevious := run == previousRun && attempt == 2
					isRerun := run == previousRun && attempt == 3 && mode == "newer rerun" && second.Load()
					if err != nil || attemptErr != nil || (!isPrevious && !isRerun && (run < runID || run >= runID+maxCycleAttempts || attempt != 1 || !second.Load())) {
						t.Error("unobserved exact attempt was requested")
						w.WriteHeader(404)
						return
					}
					status, created := "completed", recent
					if isPrevious || isRerun {
						created = old
						if !second.Load() || mode == "already found" || mode == "still active" || isRerun {
							status = "in_progress"
						}
					}
					if len(parts) == 3 {
						if second.Load() {
							if isPrevious {
								oldRead.Add(1)
								if mode == "rate limited" {
									w.WriteHeader(http.StatusTooManyRequests)
									return
								}
							} else {
								normalReads.Add(1)
							}
						}
						row := cycleRun(repo, run, attempt, status, created)
						if isPrevious && second.Load() && mode == "wrong workflow" {
							row["workflow_id"] = workflowID + 1
						}
						_ = json.NewEncoder(w).Encode(row)
					} else if second.Load() && isPrevious && mode == "page budget" {
						freshJobPages.Add(1)
						// A single full page genuinely exceeds this cycle's
						// step envelope. The omitted attempt cannot be stored.
						jobs := make([]map[string]any, 0, demandrepair.CIPerPage)
						for i := 0; i < demandrepair.CIPerPage; i++ {
							row := job(jobID+int64(i), "completed")
							row["run_id"], row["run_attempt"], row["conclusion"] = run, attempt, "success"
							steps := make([]map[string]any, 0, 21)
							for j := 1; j <= 21; j++ {
								steps = append(steps, map[string]any{"number": j, "status": "completed", "conclusion": "success"})
							}
							row["steps"] = steps
							jobs = append(jobs, row)
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs, "total_count": len(jobs)})
					} else {
						if isPrevious && second.Load() {
							freshJobPages.Add(1)
						}
						jobPage(w, run, attempt, status)
					}
				default:
					t.Error("unapproved endpoint")
					w.WriteHeader(404)
				}
			})
			first, err := ReadCycle(ctx, source, selected, from, to)
			if err != nil || first.State() != CycleObserved || len(first.Facts()) != 1 || first.Facts()[0].Clock.Phase != cihealth.Running {
				t.Fatalf("initial attempt not genuinely observed: %v, %+v", err, first.Facts())
			}
			hint := first.ActiveContinuation()
			if hint == nil {
				t.Fatal("clean active pass did not mint bounded hint")
			}
			exact := source
			if mode == "foreign" {
				exact, err = demandrepair.NewCIHealthSource(client, allRepos)
				if err != nil || exact.SourceID() != source.SourceID() {
					t.Fatal("foreign equal-label source fixture failed", err)
				}
			}
			if mode == "expired" {
				hint.cell.sealedAt = hint.cell.sealedAt.Add(-activeContinuationLifetime)
			}
			second.Store(true)
			next, err := ReadCycleContinuing(ctx, exact, selected, from, to, hint)
			if mode == "rate limited" || mode == "wrong workflow" {
				if err != ErrCycleObservation || next.State() != CycleUnavailable || len(next.Facts()) != 0 || oldRead.Load() != 1 {
					t.Fatalf("source failure was softened: %v, %+v", err, next.Facts())
				}
				return
			}
			wantNormal, wantOld, wantState := int32(1), int32(0), CycleObserved
			switch mode {
			case "saturated":
				wantNormal = maxCycleAttempts
			case "spare", "already found", "still active":
				wantOld = 1
			case "newer rerun":
				wantNormal, wantOld = 2, 1
			case "page budget":
				wantOld, wantState = 1, CyclePartial
			}
			wantFacts := int(wantNormal + wantOld)
			if mode == "page budget" {
				wantFacts--
			}
			if err != nil || next.State() != wantState || len(next.Facts()) != wantFacts || oldRead.Load() != wantOld || normalReads.Load() != wantNormal || freshJobPages.Load() != wantOld {
				t.Fatalf("incorrect observation: err=%v state=%v facts=%d fresh attempts=%d old attempts=%d jobs=%d",
					err, next.State(), len(next.Facts()), normalReads.Load(), oldRead.Load(), freshJobPages.Load())
			}
			if hint.consume(exact, allRepos[:1]) != nil {
				t.Fatal("reused consumed hint")
			}
			if mode == "still active" && next.ActiveContinuation() != nil {
				t.Fatal("a continued active row cannot renew the one-shot locator")
			}
			if mode != "spare" && mode != "newer rerun" {
				return
			}
			facts := next.Facts()
			prior := first.Facts()[0]
			settled := facts[len(facts)-1]
			if settled.Key != prior.Key || settled.Clock.Phase != cihealth.Completed || settled.Clock.Conclusion != cihealth.ConclusionSuccess ||
				!settled.AsOf.After(prior.AsOf) || len(settled.Jobs) != 1 || settled.Jobs[0].Key.Attempt != settled.Key {
				t.Fatalf("recheck did not prove exact fresh terminal result: %+v", settled)
			}
			if mode == "spare" && next.ActiveContinuation() != nil {
				t.Fatal("a continuation cannot be renewed by itself")
			}
			scope := cihealth.HistoryScope{Source: prior.Key.Source, RepositoryID: prior.Key.RepositoryID, WorkflowID: prior.Key.WorkflowID}
			history, err := NewCycleHistory(heldCycleHistory(t), source, []cihealth.HistoryScope{scope})
			if err != nil {
				t.Fatal(err)
			}
			initial, err := history.Persist(ctx, first, []HistoryExpectation{{Scope: scope}})
			if err != nil || len(initial.Commits) != 1 {
				t.Fatal("initial history failed", err)
			}
			if _, err := history.Persist(ctx, next, []HistoryExpectation{{Scope: scope, Cursor: initial.Commits[0].Cursor}}); err != nil {
				t.Fatal("exact attempt continuation failed history verification", err)
			}
			rows, err := history.Recent(ctx, scope, 8)
			if err != nil || !hasContinuationResult(rows, settled.Key) {
				t.Fatal("new exact terminal state not retained", err)
			}
		})
	}
}

func hasContinuationResult(rows []cihealth.AttemptFacts, key cihealth.AttemptKey) bool {
	for _, row := range rows {
		if row.Key == key && row.Clock.Phase == cihealth.Completed {
			return true
		}
	}
	return false
}
