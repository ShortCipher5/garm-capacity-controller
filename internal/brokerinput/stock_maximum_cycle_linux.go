package brokerinput

import (
	"crypto/sha256"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

type stockMaximumCycleOptions struct {
	StableFor  time.Duration
	MaxChanges int
}

type stockMaximumCycle struct {
	proposals []StockMaximumProposal
	expiresAt time.Time
}

// planStockMaximumCycle is a read-only batch selector for a future scaling
// cycle. It uses a bounded, ordered series of actual scoped observations, not
// an asserted last-write timestamp. Observed settings drift restarts the
// stability interval for the whole scope. This does not prove that no writer
// changed and reverted settings between observations or survive a restart
// without supplied history. Neither the batch nor its expiry grants authority.
func planStockMaximumCycle(samples []scalingPreviewSample, opt stockMaximumCycleOptions, now time.Time) (stockMaximumCycle, error) {
	fail := func() (stockMaximumCycle, error) { return stockMaximumCycle{}, capacitybroker.ErrSnapshot }
	if now.IsZero() || len(samples) < 2 || len(samples) > 32 || opt.StableFor < 15*time.Second || opt.StableFor > 5*time.Minute ||
		opt.MaxChanges < 1 || opt.MaxChanges > 1000 {
		return fail()
	}
	var stableSince time.Time
	for i, current := range samples {
		if current.scope == ([sha256.Size]byte{}) || current.source == "" || current.incomplete ||
			current.start.IsZero() || current.finish.Before(current.start) || current.finish.After(now) ||
			!current.expires.After(current.finish) || current.expires.After(current.start.Add(time.Minute)) ||
			current.sequence < 0 || current.revision < 0 || current.budget < 0 ||
			len(current.pools) == 0 || len(current.pools) > 1000 || len(current.repoCaps) == 0 || len(current.repoCaps) > 1000 {
			return fail()
		}
		if i == 0 {
			stableSince = current.finish
			continue
		}
		previous := samples[i-1]
		if current.scope != previous.scope || current.source != previous.source || current.revision != previous.revision ||
			current.sequence < previous.sequence || current.budget != previous.budget ||
			!current.start.After(previous.finish) || !previous.expires.After(current.finish) {
			return fail()
		}
		if !sameStockMaximumSettings(previous.StockScalingSample, current.StockScalingSample) {
			stableSince = current.finish
		}
	}
	previous, current := samples[len(samples)-2], samples[len(samples)-1]
	if !previous.expires.After(now) || !current.expires.After(now) {
		return fail()
	}
	if current.start.Sub(stableSince) < opt.StableFor {
		return stockMaximumCycle{}, nil
	}
	// Reuse the ordinary two-sample rule and all configured cap accounting.
	// Prefer confirmed increases in existing weighted order over reductions
	// when truncating. This cannot spend headroom from a proposed reduction.
	proposed := ProposeStockMaximums(previous.StockScalingSample, current.StockScalingSample, now)
	selected := make([]StockMaximumProposal, 0, min(opt.MaxChanges, len(proposed)))
	for _, growth := range []bool{true, false} {
		for _, change := range proposed {
			if len(selected) < opt.MaxChanges && (change.Suggested > change.Observed) == growth {
				selected = append(selected, change)
			}
		}
	}
	expiry := previous.expires
	if current.expires.Before(expiry) {
		expiry = current.expires
	}
	return stockMaximumCycle{proposals: selected, expiresAt: expiry}, nil
}
