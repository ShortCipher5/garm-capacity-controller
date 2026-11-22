package demandrepair

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestCIRunEmptyTailRequiresPrecedingPageAndRepositoryPin(t *testing.T) {
	for _, tt := range []struct {
		name                            string
		page, prior, total, lists, pins int
		want                            bool
	}{
		{name: "ordinary empty first page", page: 1, lists: 1, pins: 1, want: true},
		{name: "anchored exhausted tail", page: 2, prior: CIPerPage + 1, total: CIPerPage, lists: 1, pins: 1, want: true},
		{name: "anchored emptied query", page: 2, prior: CIPerPage + 1, lists: 1, pins: 1, want: true},
		{name: "arbitrary later page", page: 2, lists: 1},
		{name: "preceding page could not offer next", page: 2, prior: CIPerPage},
		{name: "still has rows", page: 2, prior: CIPerPage + 2, total: CIPerPage + 1, lists: 1},
		{name: "negative total", page: 2, prior: CIPerPage + 1, total: -1, lists: 1},
		{name: "unexpected anchor on first page", page: 1, prior: CIPerPage + 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var lists, pins atomic.Int32
			source := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/actions/runs":
					lists.Add(1)
					q := r.URL.Query()
					if q.Get("status") != "queued" || q.Get("page") != fmt.Sprint(tt.page) || len(q) != 3 {
						t.Errorf("unexpected upstream search: %v", q)
					}
					fmt.Fprintf(w, `{"total_count":%d,"workflow_runs":[]}`, tt.total)
				case "/repos/owner/repo":
					pins.Add(1)
					fmt.Fprintf(w, `{"id":%d,"full_name":"owner/repo"}`, int64(ciRepoID))
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			got, err := source.ListCIRuns(t.Context(), repo.GarmID, CIRunQuery{Page: tt.page, Status: "queued", PreviousTotal: tt.prior})
			if int(lists.Load()) != tt.lists || int(pins.Load()) != tt.pins {
				t.Fatalf("unexpected upstream calls: list=%d repo=%d", lists.Load(), pins.Load())
			}
			if tt.want {
				if err != nil || got.Runs == nil || len(got.Runs) != 0 || got.TotalCount != tt.total || got.NextPage != 0 || got.ObservedAt.IsZero() {
					t.Fatalf("expected verified empty page, got %+v %v", got, err)
				}
			} else if err != ErrObservation || got.Runs != nil || !got.ObservedAt.IsZero() {
				t.Fatalf("invalid empty page was accepted: %+v, %v", got, err)
			}
		})
	}
}
