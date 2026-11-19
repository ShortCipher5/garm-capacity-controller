package brokerinput

import "time"

// These are descriptive caller inputs, not acknowledgment or permission types.
// An uncertain historic outcome cannot become known merely because a later
// snapshot happens to contain the intended setting.
type stockInverseOutcome uint8

const (
	stockInverseOutcomeUnknown stockInverseOutcome = iota
	stockInverseOutcomeObserved
)

type stockInverseAdvice struct {
	Reason string
	Plan   *StockChangePlan
}

// assessStockInverseAdvice never restores a historical value on its own. A
// previous recommendation is retained only for correlation: fresh private
// preview samples must independently justify the opposite one-step change
// under CURRENT constraints via the canonical change-plan validator. The
// configured enrollment scope must remain identical. DiscoveryFingerprint is
// deliberately refreshed: it includes the mutable maximum, not only identity. Even then
// the result remains non-actionable. No actuator, policy, durable state, retry,
// stop/resume switch, or physical-capacity claim is implemented here.
func assessStockInverseAdvice(old StockChangePlan, outcome stockInverseOutcome, previous, current scalingPreviewSample, options stockChangePlanOptions, now time.Time) stockInverseAdvice {
	refuse := func(reason string) stockInverseAdvice { return stockInverseAdvice{Reason: reason} }
	if old.Actionable || old.PoolID == "" || old.RepositoryID == "" || old.Source == "" || old.Fingerprint == "" || old.Scope == ([32]byte{}) || old.Observed < 1 || old.Observed > 1000000 || old.Suggested < 1 || old.Suggested > 1000000 || (old.Delta != 1 && old.Delta != -1) || old.Suggested-old.Observed != old.Delta || old.Sequence < 0 || old.Revision < 0 {
		return refuse("invalid_history")
	}
	if outcome != stockInverseOutcomeObserved {
		return refuse("outcome_uncertain")
	}
	pool, ok := current.pools[old.PoolID]
	if !ok || old.Scope != current.scope || current.source != old.Source || pool.repo != old.RepositoryID || pool.current != old.Suggested || current.sequence < old.Sequence || current.revision < old.Revision {
		return refuse("current_state_changed")
	}
	request := stockChangePlanRequest{PoolID: old.PoolID, RepositoryID: old.RepositoryID, Source: current.source, Fingerprint: pool.fingerprint, Scope: current.scope, Sequence: current.sequence, Revision: current.revision, Observed: pool.current, Delta: -old.Delta}
	fresh, err := validateStockChangePlan(previous, current, request, options, now)
	if err != nil || fresh.Actionable || fresh.Suggested != old.Observed {
		return refuse("fresh_inverse_unavailable")
	}
	return stockInverseAdvice{Reason: "fresh_inverse_advice_only", Plan: &fresh}
}
