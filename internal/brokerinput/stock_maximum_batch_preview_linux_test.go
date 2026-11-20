package brokerinput

import (
	"errors"
	"testing"
	"time"
)

func TestStockMaximumBatchPreviewReusesObservedCadenceAndOrdinaryProposals(t *testing.T) {
	samples := maximumCycleHistory(4, 7)
	var ordinary scalingPreviewHistory
	var batch stockMaximumBatchHistory
	for i, s := range samples {
		v := ordinary.accept(s, nil, s.finish, s.expires)
		got := batch.accept(s, nil, s.finish, s.expires)
		if i == 1 && len(v.Proposals) != 3 {
			t.Fatalf("ordinary preview now waits for the batch or lost a row: %+v", v)
		}
		if i < 3 && (got.Reason != "awaiting_cadence_window" || len(got.Proposals) != 0) {
			t.Fatalf("immature observed span produced recommendations: %+v", got)
		}
		if i == 3 {
			if got.State != "proposed" || len(got.Proposals) != 2 || got.Proposals[0].PoolID != "z" || got.Proposals[1].PoolID != "a" ||
				!got.ExpiresAt.Equal(samples[i-1].expires) || !got.ExpiresAt.Equal(v.ExpiresAt) || len(v.Proposals) != 3 {
				t.Fatalf("batch=%+v ordinary=%+v", got, v)
			}
		}
	}
}

func TestStockMaximumBatchPreviewRestartsOnDiscontinuity(t *testing.T) {
	for name, disturb := range map[string]func(*scalingPreviewSample, *time.Time) error{
		"failed evidence": func(_ *scalingPreviewSample, _ *time.Time) error { return errors.New("not available") },
		"stale previous":  func(_ *scalingPreviewSample, at *time.Time) error { *at = at.Add(39 * time.Second); return nil },
		"scope change":    func(s *scalingPreviewSample, _ *time.Time) error { s.scope[0]++; return nil },
		"source change":   func(s *scalingPreviewSample, _ *time.Time) error { s.source += "-new"; return nil },
		"settings change": func(s *scalingPreviewSample, _ *time.Time) error {
			p := s.pools["empty"]
			p.fingerprint = "changed"
			s.pools["empty"] = p
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			samples := maximumCycleHistory(5, 7)
			var history stockMaximumBatchHistory
			for _, s := range samples[:3] {
				history.accept(s, nil, s.finish, s.expires)
			}
			s := samples[3]
			at := s.finish
			err := disturb(&s, &at)
			if got := history.accept(s, err, at, s.expires); len(got.Proposals) != 0 || len(history.samples) > 1 {
				t.Fatalf("discontinuity reused prior history: %+v", got)
			}
			next := samples[4]
			if got := history.accept(next, nil, next.finish, next.expires); len(got.Proposals) != 0 {
				t.Fatalf("history before interruption leaked into next cycle: %+v", got)
			}
		})
	}
}

func TestStockMaximumBatchHistoryAndCacheAreBounded(t *testing.T) {
	samples := maximumCycleHistory(20, 7)
	var history stockMaximumBatchHistory
	var preview *StockMaximumPreview
	for _, s := range samples {
		preview = history.accept(s, nil, s.finish, s.expires)
	}
	if len(history.samples) != stockMaximumBatchHistoryLimit || preview.State != "proposed" {
		t.Fatalf("history bound or warmup failed: %d %+v", len(history.samples), preview)
	}
	plan := ReadPlan{StockMaximumBatchPreview: preview}
	clone := cloneReadPlan(plan)
	*clone.StockMaximumBatchPreview.Proposals[0].ObservedMinIdle = 999
	if *plan.StockMaximumBatchPreview.Proposals[0].ObservedMinIdle == 999 {
		t.Fatal("mutated batch cache evidence")
	}
}
