package brokerinput

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStockMaximumReviewPreservesObservedIdleAndCacheOwnership(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, idle := range []int64{0, 2} {
		sample := func(start time.Time) StockScalingSample {
			return StockScalingSample{source: "source", revision: 1, sequence: 1, budget: 5,
				start: start, finish: start, expires: now.Add(time.Minute),
				pools:    map[string]scalingPool{"pool": {repo: "repo", enabled: true, current: 4, idle: idle, idleExplicit: true, cap: 5, target: 3}},
				repoCaps: map[string]int64{"repo": 5}}
		}
		prior, current := sample(now), sample(now.Add(20*time.Second))
		rows := ProposeStockMaximums(prior, current, current.finish)
		if len(rows) != 1 || rows[0].ObservedMinIdle == nil || *rows[0].ObservedMinIdle != idle || rows[0].Observed != 4 || rows[0].Suggested != 3 {
			t.Fatalf("unexpected review: %+v", rows)
		}
		raw, err := json.Marshal(rows[0])
		if err != nil || !strings.Contains(string(raw), `"observed_min_idle":`) || strings.Contains(string(raw), "suggested_min_idle") {
			t.Fatalf("metadata missing or recommends idle: %s %v", raw, err)
		}
		original := ReadPlan{StockMaximumPreview: &StockMaximumPreview{Proposals: rows}}
		copied := cloneReadPlan(original)
		*copied.StockMaximumPreview.Proposals[0].ObservedMinIdle = 999
		if *original.StockMaximumPreview.Proposals[0].ObservedMinIdle != idle || current.pools["pool"].idle != idle {
			t.Fatal("review metadata aliases source/cache")
		}
		p := current.pools["pool"]
		p.idle++
		current.pools["pool"] = p
		if len(ProposeStockMaximums(prior, current, current.finish)) != 0 {
			t.Fatal("idle drift exposed confirmed recommendation")
		}
	}
	raw, err := json.Marshal(StockMaximumProposal{PoolID: "legacy", Observed: 1, Suggested: 2})
	if err != nil || strings.Contains(string(raw), "observed_min_idle") {
		t.Fatalf("absent old metadata invented: %s %v", raw, err)
	}
}
