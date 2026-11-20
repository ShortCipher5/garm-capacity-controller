package cihealthsource

import (
	"encoding/json"
	"errors"
	"net/http"
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

func TestReadCycleRejectsMovingTotalBeforeAttemptRead(t *testing.T) {
	from, to := relativeWindow()
	repo := cycleRepos()[0]
	var calls atomic.Int32
	s := fixtureRepos(t, []demandrepair.CIRepository{repo}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/repos/fixture/runners/actions/runs" || r.URL.Query().Get("status") != "queued" {
			t.Error("moving total did not stop this scan immediately")
			w.WriteHeader(404)
			return
		}
		total := demandrepair.CIPerPage + 1
		var rows []map[string]any
		switch r.URL.Query().Get("page") {
		case "1":
			total++
			for i := demandrepair.CIPerPage - 1; i >= 0; i-- {
				rows = append(rows, cycleRun(repo, runID+int64(i), 2, "queued", from))
			}
		case "2":
			rows = append(rows, cycleRun(repo, runID+int64(demandrepair.CIPerPage), 2, "queued", from))
		default:
			t.Error("unexpected run page")
		}
		runPage(w, rows, total)
	})
	got, err := ReadCycle(t.Context(), s, []string{repo.GarmID}, from, to)
	if !errors.Is(err, ErrCycleObservation) || got.State() != CycleUnavailable || got.Facts() != nil || calls.Load() != 2 {
		t.Fatalf("moving discovery total exposed facts or proceeded: %v (%d calls)", err, calls.Load())
	}
}
