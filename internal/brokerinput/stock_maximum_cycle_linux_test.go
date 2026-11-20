package brokerinput

import (
	"errors"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

func maximumCycleHistory(count int, budget int64) []scalingPreviewSample {
	base := time.Unix(1700000000, 0)
	out := make([]scalingPreviewSample, 0, count)
	for i := range count {
		start := base.Add(time.Duration(i) * 20 * time.Second)
		out = append(out, scalingPreviewSample{scope: [32]byte{1}, StockScalingSample: StockScalingSample{
			source: "pinned", revision: 2, sequence: int64(i + 3), budget: budget,
			start: start, finish: start.Add(time.Second), expires: start.Add(time.Minute),
			pools: map[string]scalingPool{
				"a":     {repo: "repo-a", fingerprint: "a-settings", enabled: true, current: 1, cap: 4, target: 2},
				"z":     {repo: "repo-z", fingerprint: "z-settings", enabled: true, current: 1, cap: 4, target: 2},
				"empty": {repo: "repo-a", fingerprint: "empty-settings", enabled: true, current: 3, cap: 4, target: 1},
			},
			repoCaps:             map[string]int64{"repo-a": 5, "repo-z": 4},
			order:                []string{"z", "a"},
			configuredMaximumFit: roomyMaximumFit("empty", "a", "z"),
		}})
	}
	return out
}

func TestStockMaximumCycleBoundsChurnAndKeepsGrowthOrdered(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget int64
		limit  int
		want   []string
	}{
		{"growth-before-reduction", 7, 1, []string{"z"}},
		{"weighted-order", 7, 2, []string{"z", "a"}},
		{"no-borrowing", 6, 2, []string{"z", "empty"}},
		{"reductions-only-when-full", 5, 2, []string{"empty"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := maximumCycleHistory(4, tc.budget)
			opts := stockMaximumCycleOptions{StableFor: 59 * time.Second, MaxChanges: tc.limit}
			got, err := planStockMaximumCycle(h, opts, h[3].finish)
			if err != nil || len(got.proposals) != len(tc.want) || !got.expiresAt.Equal(h[2].expires) {
				t.Fatalf("unexpected batch: %+v %v", got, err)
			}
			for i, p := range got.proposals {
				wantStep := int64(1)
				if p.PoolID == "empty" {
					wantStep = -1
				}
				if p.PoolID != tc.want[i] || p.Suggested-p.Observed != wantStep {
					t.Fatalf("unexpected recommendation %d: %+v", i, p)
				}
			}
			opts.StableFor += time.Nanosecond
			if early, err := planStockMaximumCycle(h, opts, h[3].finish); err != nil || len(early.proposals) != 0 {
				t.Fatalf("approved before observed stability boundary: %+v %v", early, err)
			}
		})
	}
}

func TestStockMaximumCycleRestartsAfterObservedSettingDrift(t *testing.T) {
	h := maximumCycleHistory(6, 7)
	// Even another pool's fingerprint change restarts the scope's stability
	// period; there is no advisory bypass by selecting a different pool.
	for i := 2; i < len(h); i++ {
		p := h[i].pools["empty"]
		p.fingerprint = "changed-settings"
		h[i].pools["empty"] = p
	}
	opt := stockMaximumCycleOptions{StableFor: 40 * time.Second, MaxChanges: 1}
	if immature, err := planStockMaximumCycle(h[:5], opt, h[4].finish); err != nil || len(immature.proposals) != 0 {
		t.Fatalf("settings drift did not restart stability: %+v %v", immature, err)
	}
	if mature, err := planStockMaximumCycle(h, opt, h[5].finish); err != nil || len(mature.proposals) != 1 || mature.proposals[0].PoolID != "z" {
		t.Fatalf("stable subsequent observations did not recover: %+v %v", mature, err)
	}
	// An urgent or reversed latest target still needs two agreeing polls.
	p := h[4].pools["z"]
	p.target = p.current
	h[4].pools["z"] = p
	if got, err := planStockMaximumCycle(h, opt, h[5].finish); err != nil || len(got.proposals) != 1 || got.proposals[0].PoolID != "a" {
		t.Fatalf("bypassed latest direction confirmation: %+v %v", got, err)
	}
}

func TestStockMaximumCycleRejectsInvalidEvidence(t *testing.T) {
	for name, edit := range map[string]func(*[]scalingPreviewSample, *stockMaximumCycleOptions, *time.Time){
		"zero defaults": func(_ *[]scalingPreviewSample, o *stockMaximumCycleOptions, _ *time.Time) {
			*o = stockMaximumCycleOptions{}
		},
		"oversized batch": func(_ *[]scalingPreviewSample, o *stockMaximumCycleOptions, _ *time.Time) { o.MaxChanges = 1001 },
		"short history":   func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) { *h = (*h)[:1] },
		"oversized history": func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) {
			*h = maximumCycleHistory(33, 7)
		},
		"scope drift":   func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) { (*h)[2].scope[0]++ },
		"source drift":  func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) { (*h)[2].source += "-other" },
		"ledger rewind": func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) { (*h)[2].sequence = 0 },
		"policy drift":  func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) { (*h)[2].revision++ },
		"overlap": func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) {
			(*h)[2].start = (*h)[1].finish
		},
		"expired gap": func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) {
			(*h)[0].expires = (*h)[1].finish
		},
		"expired confirmation": func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, now *time.Time) { *now = (*h)[2].expires },
		"future": func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, now *time.Time) {
			*now = (*h)[3].finish.Add(-time.Nanosecond)
		},
		"invalid lifetime": func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) {
			(*h)[1].expires = (*h)[1].expires.Add(time.Nanosecond)
		},
		"incomplete": func(h *[]scalingPreviewSample, _ *stockMaximumCycleOptions, _ *time.Time) { (*h)[0].incomplete = true },
	} {
		t.Run(name, func(t *testing.T) {
			h := maximumCycleHistory(4, 7)
			now := h[3].finish
			opt := stockMaximumCycleOptions{StableFor: 40 * time.Second, MaxChanges: 1}
			edit(&h, &opt, &now)
			if got, err := planStockMaximumCycle(h, opt, now); !errors.Is(err, capacitybroker.ErrSnapshot) ||
				len(got.proposals) != 0 || !got.expiresAt.IsZero() {
				t.Fatalf("invalid evidence was not closed: %+v %v", got, err)
			}
		})
	}
}
