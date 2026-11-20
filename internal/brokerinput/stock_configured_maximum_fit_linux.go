package brokerinput

import "github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"

type stockPoolMaximumFit struct {
	size, remaining capacitybroker.Resources
}

type stockConfiguredMaximumFit struct {
	global capacitybroker.Resources
	pools  map[string]stockPoolMaximumFit
}

// captureStockConfiguredMaximumFit models one hypothetical scenario: all
// currently enabled maxima run their enrolled sizes. This is an independent
// veto, not part of ledger accounting. Adding existing charges again would
// double count possibly overlapping runners. Instead the existing charged
// planner independently tests every actual charge, unknown and overhead.
// Passing both separate tests does not prove shared physical headroom.
func captureStockConfiguredMaximumFit(snapshot capacitybroker.Snapshot, pools map[string]scalingPool) *stockConfiguredMaximumFit {
	fit := &stockConfiguredMaximumFit{global: snapshot.Global, pools: make(map[string]stockPoolMaximumFit, len(pools))}
	for d := 0; d < 3; d++ {
		if fit.global[d] < 0 {
			return nil
		}
	}
	for id, row := range pools {
		pool, ok := snapshot.Pools[id]
		if !ok || pool.Budget == nil || pool.Repository != row.repo || row.current < 0 || row.current > 1000000 {
			return nil
		}
		p := stockPoolMaximumFit{size: pool.Size, remaining: *pool.Budget}
		for d := 0; d < 3; d++ {
			if p.size[d] <= 0 || p.remaining[d] < 0 {
				return nil
			}
			if row.enabled {
				p.remaining[d] = subtractMaximumExposure(p.remaining[d], row.current, p.size[d])
				fit.global[d] = subtractMaximumExposure(fit.global[d], row.current, p.size[d])
			}
		}
		fit.pools[id] = p
	}
	return fit
}

func subtractMaximumExposure(budget, count, size int64) int64 {
	// Check with division so a very large modeled product cannot wrap into
	// artificial headroom. An already exceeded budget saturates at zero.
	if count > budget/size {
		return 0
	}
	return budget - count*size
}

type stockMaximumFitComparison struct {
	a, b         *stockConfiguredMaximumFit
	leftA, leftB capacitybroker.Resources
}

func compareStockMaximumFit(a, b *stockConfiguredMaximumFit) stockMaximumFitComparison {
	v := stockMaximumFitComparison{a: a, b: b}
	if a != nil && b != nil {
		v.leftA, v.leftB = a.global, b.global
	}
	return v
}

type stockMaximumFitResult uint8

const (
	stockMaximumFitNotChecked stockMaximumFitResult = iota
	stockMaximumFitAllowed
	stockMaximumFitVeto
	stockMaximumFitUnavailable
)

func (v *stockMaximumFitComparison) tryGrowth(id string) bool {
	return v.checkGrowth(id) == stockMaximumFitAllowed
}

// checkGrowth is local to this comparison. Only an accepted +1 spends quota.
// Reductions never participate, and the captured samples remain immutable.
func (v *stockMaximumFitComparison) checkGrowth(id string) stockMaximumFitResult {
	if v.a == nil || v.b == nil {
		return stockMaximumFitUnavailable
	}
	a, hasA := v.a.pools[id]
	b, hasB := v.b.pools[id]
	if !hasA || !hasB || a.size != b.size {
		return stockMaximumFitUnavailable
	}
	for d := 0; d < 3; d++ {
		if a.size[d] <= 0 {
			return stockMaximumFitUnavailable
		}
	}
	for d := 0; d < 3; d++ {
		if a.size[d] > min(a.remaining[d], b.remaining[d], v.leftA[d], v.leftB[d]) {
			return stockMaximumFitVeto
		}
	}
	for d := 0; d < 3; d++ {
		v.leftA[d] -= a.size[d]
		v.leftB[d] -= a.size[d]
	}
	return stockMaximumFitAllowed
}
