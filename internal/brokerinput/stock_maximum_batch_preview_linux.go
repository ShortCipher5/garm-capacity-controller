package brokerinput

import "time"

const (
	stockMaximumBatchStableSpan   = 45 * time.Second
	stockMaximumBatchLimit        = 2
	stockMaximumBatchHistoryLimit = 8
)

// Sample history has no settings-application events and is never persisted.
// It is scoped to one reader and shares the ordinary maximum preview's poll.
type stockMaximumBatchHistory struct {
	samples []scalingPreviewSample
}

func (h *stockMaximumBatchHistory) reset() { h.samples = nil }

func (h *stockMaximumBatchHistory) accept(sample scalingPreviewSample, evidenceErr error, now, baseExpiry time.Time) *StockMaximumPreview {
	out := &StockMaximumPreview{State: "unavailable", Reason: "evidence_unavailable", ExpiresAt: baseExpiry, Proposals: []StockMaximumProposal{}}
	if evidenceErr != nil || sample.source == "" || sample.incomplete || !sample.expires.After(now) {
		h.reset()
		return out
	}
	if sample.expires.Before(out.ExpiresAt) {
		out.ExpiresAt = sample.expires
	}
	if n := len(h.samples); n != 0 {
		old := h.samples[n-1]
		// Malformed, changed or stale comparison evidence cannot extend the
		// sampled settings window. Re-prime from a new genuine poll.
		if old.scope != sample.scope || old.source != sample.source || old.revision != sample.revision ||
			old.budget != sample.budget || old.sequence > sample.sequence || !old.expires.After(now) ||
			!sample.start.After(old.finish) ||
			!sameStockMaximumSettings(old.StockScalingSample, sample.StockScalingSample) {
			h.reset()
		}
	}
	if len(h.samples) == stockMaximumBatchHistoryLimit {
		copy(h.samples, h.samples[1:])
		h.samples[len(h.samples)-1] = scalingPreviewSample{}
		h.samples = h.samples[:len(h.samples)-1]
	}
	sample.disk = ownStockMaximumPreviewDisk(sample.disk)
	h.samples = append(h.samples, sample)
	out.HostEvidence = "configured_only"
	out.State, out.Reason = "no_confirmed_proposals", "awaiting_cadence_window"
	if len(h.samples) < 2 {
		return out
	}
	cycle, err := planStockMaximumCycle(h.samples, stockMaximumCycleOptions{
		StableFor: stockMaximumBatchStableSpan, MaxChanges: stockMaximumBatchLimit,
	}, now)
	if err != nil {
		h.reset()
		out.State, out.Reason, out.HostEvidence = "unavailable", "evidence_unavailable", ""
		return out
	}
	if cycle.expiresAt.IsZero() {
		return out
	}
	if cycle.expiresAt.Before(out.ExpiresAt) {
		out.ExpiresAt = cycle.expiresAt
	}
	out.Reason = "no_confirmed_change"
	if len(cycle.proposals) > 0 {
		out.State, out.Reason, out.Proposals = "proposed", "", cycle.proposals
	}
	return out
}
