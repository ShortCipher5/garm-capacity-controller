package admission

import (
	"strings"
	"testing"
	"time"
)

func TestRecentUsageHistoryUsesChronologicalIndexForReverseOrder(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	for _, tc := range []struct {
		filter string
		args   []any
	}{
		{"", []any{"a", 1000}},
		{sqliteUsageHistorySince, []any{"a", "2026-01-01T00:00:00.1Z", 1000}},
	} {
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+sqliteUsageHistorySelect+tc.filter+sqliteUsageHistoryRecentOrder, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			details = append(details, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(details, "\n")
		if !strings.Contains(plan, "SEARCH usage_samples USING INDEX usage_samples_chronological") ||
			strings.Contains(plan, "TEMP B-TREE") ||
			tc.filter != "" && !strings.Contains(plan, "<expr>>?") {
			t.Fatalf("expected indexed host/time search, without sorting history: %s", plan)
		}
	}
}
