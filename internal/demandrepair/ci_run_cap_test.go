package demandrepair

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilteredCIRunCapRequiresValidPositivePage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    string
		total     int
		rows      int
		page      int
		parseBad  string
		httpError int
		atCap     bool
		success   bool
	}{
		{name: "exact filtered ceiling first page", total: 1000, rows: 50, page: 1, atCap: true},
		{name: "exact filtered ceiling last page", total: 1000, rows: 50, page: 20, atCap: true},
		{name: "below ceiling", total: 999, rows: 50, page: 1, success: true},
		{name: "active query is not a window", status: "in_progress", total: 1000, rows: 50, page: 1},
		{name: "above supported count is not cap proof", total: 1001, rows: 50, page: 1},
		{name: "short", total: 1000, rows: 49, page: 1},
		{name: "empty", total: 1000, rows: 0, page: 1},
		{name: "foreign ID", total: 1000, rows: 50, page: 1, parseBad: "repo"},
		{name: "wrong creation time", total: 1000, rows: 50, page: 1, parseBad: "time"},
		{name: "duplicate run", total: 1000, rows: 50, page: 1, parseBad: "duplicate"},
		{name: "malformed body", total: 1000, rows: 50, page: 1, parseBad: "body"},
		{name: "rate limit", total: 1000, rows: 50, page: 1, httpError: 429},
		{name: "upstream unavailable", total: 1000, rows: 50, page: 1, httpError: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			to := time.Now().UTC().Truncate(time.Second)
			from := to.Add(-time.Hour)
			var calls atomic.Int32
			source := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/repos/owner/repo/actions/runs" || (r.URL.Query().Get("created") != "") == (tc.status != "") {
					t.Error("query or source changed")
				}
				if tc.httpError != 0 {
					w.WriteHeader(tc.httpError)
					return
				}
				if tc.parseBad == "body" {
					_, _ = w.Write([]byte(`{"total_count":1000,"workflow_runs":[`))
					return
				}
				runs := make([]any, 0, tc.rows)
				for i := 0; i < tc.rows; i++ {
					id, repoID, created := int64(ciRunID)+int64(i), int64(ciRepoID), from.Add(time.Minute)
					if i == tc.rows-1 {
						switch tc.parseBad {
						case "repo":
							repoID--
						case "time":
							created = from.Add(-time.Second)
						case "duplicate":
							id = ciRunID
						}
					}
					runs = append(runs, map[string]any{
						"id": id, "workflow_id": 10, "run_attempt": 2, "status": "in_progress",
						"created_at": created, "repository": map[string]any{"id": repoID, "full_name": "owner/repo"},
					})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"total_count": tc.total, "workflow_runs": runs})
			})
			q := CIRunQuery{Page: tc.page, Status: tc.status}
			if q.Status == "" {
				q.From, q.To = from, to
			}
			got, err := source.ListCIRuns(t.Context(), repo.GarmID, q)
			if calls.Load() != 1 {
				t.Fatalf("got %d source calls, want 1", calls.Load())
			}
			if tc.success {
				if err != nil || len(got.Runs) != tc.rows || got.NextPage != 2 {
					t.Fatalf("normal read changed: %+v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrObservation) || errors.Is(err, ErrCIRunWindowAtCap) != tc.atCap ||
				got.Runs != nil || !got.ObservedAt.IsZero() || got.TotalCount != 0 || got.NextPage != 0 {
				t.Fatalf("unexpected error or page: %v / %+v", err, got)
			}
		})
	}
}
