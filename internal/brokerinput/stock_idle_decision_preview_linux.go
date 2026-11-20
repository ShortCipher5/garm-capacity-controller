package brokerinput

import "time"

// StockIdleDecisionPreview is a single shadow recommendation. A missing field
// means no decision was confirmed, not that no change is needed. It is never
// a settings mutation, evidence of released capacity or authority to start.
type StockIdleDecisionPreview struct {
	StockIdleProposal
	Direction    string    `json:"direction"`
	HostEvidence string    `json:"host_evidence"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type stockIdleDecisionHistory struct{ prior stockIdleDecisionSample }

func (h *stockIdleDecisionHistory) reset() { h.prior = stockIdleDecisionSample{} }

func (h *stockIdleDecisionHistory) accept(sample stockIdleDecisionSample, sampleErr error, now, baseExpiry time.Time) *StockIdleDecisionPreview {
	if sampleErr != nil || sample.source == "" || !sample.expiry.After(now) || !baseExpiry.After(now) {
		h.reset()
		return nil
	}
	prior := h.prior
	h.prior = sample
	advice, ok := proposeStockIdleDecision(prior, sample, now)
	if !ok {
		return nil
	}
	expiry := advice.ExpiresAt
	if baseExpiry.Before(expiry) {
		expiry = baseExpiry
	}
	if !expiry.After(now) {
		return nil
	}
	direction := "increase"
	if advice.Suggested < advice.Observed {
		direction = "decrease"
	}
	return &StockIdleDecisionPreview{
		StockIdleProposal: StockIdleProposal{
			PoolID: advice.PoolID, RepositoryID: advice.RepositoryID, ObservedMinIdle: advice.Observed,
			SuggestedMinIdle: advice.Suggested, UnchangedMaximum: advice.UnchangedMaximum,
		},
		Direction: direction, HostEvidence: "configured_only", ExpiresAt: expiry,
	}
}
