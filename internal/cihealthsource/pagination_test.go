package cihealthsource

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

// The same IDs (in reverse order on page 1) are readable with a stable total.
// A changed total is a distinct conflict, not a duplicate-row detection.
func TestReadAttemptRejectsMovingTotalWithUniqueJobs(t *testing.T) {
	for _, moving := range []bool{false, true} {
		name := "stable"
		if moving {
			name = "changed"
		}
		t.Run(name, func(t *testing.T) {
			s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case attemptPath:
					runResponse(w, repoID, "in_progress", "")
				case attemptPath + "/jobs":
					total := demandrepair.CIPerPage + 1
					jobs := []map[string]any{}
					switch r.URL.Query().Get("page") {
					case "1":
						if moving {
							total++
						}
						for i := demandrepair.CIPerPage - 1; i >= 0; i-- {
							jobs = append(jobs, job(jobID+int64(i), "in_progress"))
						}
					case "2":
						jobs = append(jobs, job(jobID+int64(demandrepair.CIPerPage), "in_progress"))
					default:
						t.Error("unexpected job page")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"total_count": total, "jobs": jobs})
				default:
					t.Error("unexpected upstream read")
					w.WriteHeader(404)
				}
			})
			got, err := ReadAttempt(t.Context(), s, scope, runID, 2)
			if moving {
				if !errors.Is(err, ErrAttemptObservation) || !got.Facts().AsOf.IsZero() || len(got.Facts().Jobs) != 0 {
					t.Fatalf("moving total exposed facts: %v", err)
				}
			} else if err != nil || len(got.Facts().Jobs) != demandrepair.CIPerPage+1 {
				t.Fatalf("identical stable pages failed: %v", err)
			}
		})
	}
}

func TestReadCycleKeepsIndependentFactsOnValidDiscoveryCountDrift(t *testing.T) {
	for _, fault := range []string{"none", "duplicate", "wrong repository", "rate limit", "malformed page"} {
		t.Run(fault, func(t *testing.T) {
			from, to := relativeWindow()
			repos := cycleRepos()
			discarded, quiet := runID+int64(demandrepair.CIPerPage), runID+100
			var calls, detailReads, secondPages atomic.Int32
			s := fixtureRepos(t, repos, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
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
						runPage(w, []map[string]any{cycleRun(repo, quiet, 2, "queued", from)}, 1)
						return
					}
					switch q.Get("page") {
					case "1":
						rows := make([]map[string]any, 0, demandrepair.CIPerPage)
						for i := range demandrepair.CIPerPage {
							rows = append(rows, cycleRun(repo, runID+int64(i), 2, "queued", from))
						}
						runPage(w, rows, demandrepair.CIPerPage+2)
					case "2":
						secondPages.Add(1)
						switch fault {
						case "duplicate":
							runPage(w, []map[string]any{cycleRun(repo, runID, 2, "queued", from)}, demandrepair.CIPerPage+1)
						case "wrong repository":
							runPage(w, []map[string]any{cycleRun(repos[1], discarded, 2, "queued", from)}, demandrepair.CIPerPage+1)
						case "rate limit":
							w.WriteHeader(http.StatusTooManyRequests)
						case "malformed page":
							runPage(w, nil, demandrepair.CIPerPage+1)
						default:
							runPage(w, []map[string]any{cycleRun(repo, discarded, 2, "queued", from)}, demandrepair.CIPerPage+1)
						}
					default:
						t.Error("unexpected page")
					}
				case strings.HasPrefix(r.URL.Path, base+"/actions/runs/"):
					suffix := strings.Split(strings.TrimPrefix(r.URL.Path, base+"/actions/runs/"), "/")
					run, err := strconv.ParseInt(suffix[0], 10, 64)
					if err != nil || len(suffix) < 3 || suffix[1] != "attempts" || suffix[2] != "2" || run == discarded {
						t.Error("attempt read selected an unstable or nonexact row")
						w.WriteHeader(404)
						return
					}
					if len(suffix) == 3 {
						detailReads.Add(1)
						_ = json.NewEncoder(w).Encode(cycleRun(repo, run, 2, "queued", from))
					} else {
						jobPage(w, run, 2, "queued")
					}
				default:
					t.Error("unexpected upstream read")
					w.WriteHeader(404)
				}
			})
			got, err := ReadCycle(t.Context(), s, []string{repos[1].GarmID, repos[0].GarmID}, from, to)
			if fault != "none" {
				if !errors.Is(err, ErrCycleObservation) || got.State() != CycleUnavailable || got.Facts() != nil || detailReads.Load() != 0 {
					t.Fatalf("bad page did not fail closed: %v", err)
				}
				return
			}
			facts := got.Facts()
			if err != nil || got.State() != CyclePartial || len(facts) != maxCycleAttempts/2+1 ||
				secondPages.Load() != 1 || calls.Load() > maxCycleHTTPReads {
				t.Fatalf("valid drift lost the cycle or escaped bounds: %v, %d reads, %d rows", err, calls.Load(), len(facts))
			}
			if facts[0].Key.RunID != runID || facts[1].Key.RunID != quiet || facts[1].Key.RepositoryID != repos[1].GitHubID {
				t.Fatal("did not preserve trusted prefix and independently observed repository order")
			}
		})
	}
}
