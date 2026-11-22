package admission

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUsageHistoryOrdersWithinSecondBeforeApplyingLimit(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	offsets := []time.Duration{0, 100 * time.Millisecond, 100*time.Millisecond + time.Nanosecond, 101 * time.Millisecond, time.Second}
	for i := len(offsets) - 1; i >= 0; i-- {
		if err := s.RecordUsageSample(ctx, "a", base.Add(offsets[i]), int64(i), 0, 0, "probe"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordUsageSample(ctx, "b", base, 42, 0, 0, "probe"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		since time.Time
		first int
	}{
		{"without cutoff", time.Time{}, 0},
		{"whole second cutoff", base, 0},
		{"short fractional cutoff in caller zone", base.Add(offsets[1]).In(time.FixedZone("test", 8*60*60)), 1},
		{"nanosecond cutoff", base.Add(offsets[2]), 2},
		{"next whole second", base.Add(time.Second), 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.UsageHistory(ctx, "a", tc.since, 1)
			if err != nil || len(got) != 1 || got[0].Host != "a" || !got[0].SampledAt.Equal(base.Add(offsets[tc.first])) || got[0].CPUUsedMillis != int64(tc.first) {
				t.Fatalf("wrong cutoff/order before limit: %+v %v", got, err)
			}
			got, err = s.UsageHistory(ctx, "a", tc.since, 10)
			if err != nil || len(got) != len(offsets)-tc.first {
				t.Fatalf("wrong full range: %+v %v", got, err)
			}
			for i, sample := range got {
				if !sample.SampledAt.Equal(base.Add(offsets[tc.first+i])) {
					t.Fatalf("out of order: %+v", got)
				}
			}
		})
	}
}

func TestUsageHistoryIndexMigrationPreservesLegacyRowsAndDedupe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := OpenSQLite(path, fixture(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	early := base.Add(100 * time.Millisecond)
	// Construct the old table state with precisely the old stored UTC strings.
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS usage_samples_chronological;
INSERT INTO usage_samples VALUES ('a','2026-01-01T00:00:00.1Z',10,20,30,'legacy'), ('a','2026-01-01T00:00:00Z',1,2,3,'legacy');
PRAGMA user_version=28;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path, Config{})
	if err != nil {
		t.Fatalf("v28 migration refused: %v", err)
	}
	defer s.Close()

	if err := s.RecordUsageSample(ctx, "a", early, 99, 99, 99, "duplicate"); err != nil {
		t.Fatal(err)
	}
	got, err := s.UsageHistory(ctx, "a", time.Time{}, 10)
	if err != nil || len(got) != 2 || !got[0].SampledAt.Equal(base) || !got[1].SampledAt.Equal(early) || got[1].CPUUsedMillis != 10 || got[1].Source != "legacy" {
		t.Fatalf("legacy samples or dedupe lost: %+v %v", got, err)
	}
}

func TestUsageHistoryUsesChronologicalIndexForSeekAndOrder(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	for _, tc := range []struct {
		filter string
		args   []any
	}{
		{"", []any{"a", 1000}},
		{sqliteUsageHistorySince, []any{"a", "2026-01-01T00:00:00.1Z", 1000}},
	} {
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+sqliteUsageHistorySelect+tc.filter+sqliteUsageHistoryOrder, tc.args...)
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
