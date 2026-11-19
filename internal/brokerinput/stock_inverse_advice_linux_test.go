package brokerinput

import (
	"testing"
	"time"
)

func inverseFixture() (StockChangePlan, scalingPreviewSample, scalingPreviewSample, stockChangePlanOptions, time.Time) {
	now := time.Unix(1700000000, 0)
	sample := func(start time.Time) scalingPreviewSample {
		return scalingPreviewSample{scope: [32]byte{1}, StockScalingSample: StockScalingSample{source: "source", sequence: 10, revision: 3, budget: 8, start: start, finish: start.Add(time.Second), expires: start.Add(time.Minute), pools: map[string]scalingPool{"pool": {repo: "repo", fingerprint: "new-settings", enabled: true, current: 3, target: 2, idle: 0, used: 1, cap: 8}}, repoCaps: map[string]int64{"repo": 8}, order: []string{"pool"}}}
	}
	old := StockChangePlan{PoolID: "pool", RepositoryID: "repo", Source: "source", Fingerprint: "original-settings", Scope: [32]byte{1}, Observed: 2, Suggested: 3, Delta: 1, Sequence: 9, Revision: 2, ExpiresAt: now.Add(-time.Hour)}
	a, b := sample(now), sample(now.Add(20*time.Second))
	return old, a, b, stockChangePlanOptions{EnableValidation: true, MaxStep: 1, Minimum: 1, Maximum: 8}, b.finish
}
func TestInverseUsesFreshConstraintsNotHistoricExpiryOrPolicy(t *testing.T) {
	old, a, b, opt, now := inverseFixture()
	result := assessStockInverseAdvice(old, stockInverseOutcomeObserved, a, b, opt, now)
	if result.Reason != "fresh_inverse_advice_only" || result.Plan == nil || result.Plan.Actionable || result.Plan.Suggested != 2 || result.Plan.Revision != 3 || result.Plan.Sequence != 10 || result.Plan.Fingerprint != "new-settings" || !result.Plan.ExpiresAt.After(now) {
		t.Fatal(result)
	}
	// Historic target is not a permission to violate a new minimum.
	opt.Minimum = 3
	if got := assessStockInverseAdvice(old, stockInverseOutcomeObserved, a, b, opt, now); got.Plan != nil || got.Reason != "fresh_inverse_unavailable" {
		t.Fatal(got)
	}
}
func TestInverseUncertainNeverClearsFromMatchingObservedMaximum(t *testing.T) {
	old, a, b, opt, now := inverseFixture()
	for _, outcome := range []stockInverseOutcome{stockInverseOutcomeUnknown, 99} {
		got := assessStockInverseAdvice(old, outcome, a, b, opt, now)
		if got.Plan != nil || got.Reason != "outcome_uncertain" {
			t.Fatal(got)
		}
	}
}
func TestInverseRefusesPauseStaleDriftAndInsufficientCurrentAdvice(t *testing.T) {
	for name, mutate := range map[string]func(*StockChangePlan, *scalingPreviewSample, *scalingPreviewSample, *stockChangePlanOptions){
		"disabled": func(_ *StockChangePlan, _ *scalingPreviewSample, _ *scalingPreviewSample, o *stockChangePlanOptions) {
			o.EnableValidation = false
		},
		"stale": func(_ *StockChangePlan, a *scalingPreviewSample, b *scalingPreviewSample, _ *stockChangePlanOptions) {
			a.expires = b.finish
		},
		"scope": func(_ *StockChangePlan, a *scalingPreviewSample, _ *scalingPreviewSample, _ *stockChangePlanOptions) {
			a.scope = [32]byte{2}
		},
		"changed source": func(_ *StockChangePlan, _ *scalingPreviewSample, b *scalingPreviewSample, _ *stockChangePlanOptions) {
			b.source = "other"
		},
		"changed max": func(_ *StockChangePlan, _ *scalingPreviewSample, b *scalingPreviewSample, _ *stockChangePlanOptions) {
			p := b.pools["pool"]
			p.current = 4
			b.pools["pool"] = p
		},
		"charged floor": func(_ *StockChangePlan, a *scalingPreviewSample, b *scalingPreviewSample, _ *stockChangePlanOptions) {
			for _, s := range []*scalingPreviewSample{a, b} {
				p := s.pools["pool"]
				p.used = 3
				s.pools["pool"] = p
			}
		},
		"direction": func(_ *StockChangePlan, a *scalingPreviewSample, b *scalingPreviewSample, _ *stockChangePlanOptions) {
			for _, s := range []*scalingPreviewSample{a, b} {
				p := s.pools["pool"]
				p.target = 4
				s.pools["pool"] = p
			}
		},
		"changed historic scope": func(old *StockChangePlan, _ *scalingPreviewSample, _ *scalingPreviewSample, _ *stockChangePlanOptions) {
			old.Scope = [32]byte{2}
		},
		"missing historic scope": func(old *StockChangePlan, _ *scalingPreviewSample, _ *scalingPreviewSample, _ *stockChangePlanOptions) {
			old.Scope = [32]byte{}
		},
		"missing historic fingerprint": func(old *StockChangePlan, _ *scalingPreviewSample, _ *scalingPreviewSample, _ *stockChangePlanOptions) {
			old.Fingerprint = ""
		},
		"historic actionable": func(old *StockChangePlan, _ *scalingPreviewSample, _ *scalingPreviewSample, _ *stockChangePlanOptions) {
			old.Actionable = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			old, a, b, opt, now := inverseFixture()
			mutate(&old, &a, &b, &opt)
			if got := assessStockInverseAdvice(old, stockInverseOutcomeObserved, a, b, opt, now); got.Plan != nil {
				t.Fatal(got)
			}
		})
	}
}

func TestInverseIncreaseRequiresFreshHeadroomAndCap(t *testing.T) {
	old, a, b, opt, now := inverseFixture()
	old.Observed, old.Suggested, old.Delta = 3, 2, -1
	for _, s := range []*scalingPreviewSample{&a, &b} {
		p := s.pools["pool"]
		p.current = 2
		p.target = 3
		s.pools["pool"] = p
	}
	got := assessStockInverseAdvice(old, stockInverseOutcomeObserved, a, b, opt, now)
	if got.Plan == nil || got.Plan.Suggested != 3 || got.Plan.Actionable {
		t.Fatal(got)
	}
	for _, s := range []*scalingPreviewSample{&a, &b} {
		s.budget = 2
	}
	if got := assessStockInverseAdvice(old, stockInverseOutcomeObserved, a, b, opt, now); got.Plan != nil {
		t.Fatal("historic maximum bypassed current global limit")
	}
	for _, s := range []*scalingPreviewSample{&a, &b} {
		s.budget = 8
		p := s.pools["pool"]
		p.cap = 2
		s.pools["pool"] = p
	}
	if got := assessStockInverseAdvice(old, stockInverseOutcomeObserved, a, b, opt, now); got.Plan != nil {
		t.Fatal("historic maximum bypassed current pool cap")
	}
}
