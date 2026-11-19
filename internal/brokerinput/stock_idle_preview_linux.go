package brokerinput

import (
	"errors"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"sort"
	"time"
)

// StockIdlePreview is a manual comparison, never idle credit or release proof.
type StockIdlePreview struct {
	State        string              `json:"state"`
	Reason       string              `json:"reason,omitempty"`
	ExpiresAt    time.Time           `json:"expires_at"`
	HostEvidence string              `json:"host_evidence,omitempty"`
	Proposals    []StockIdleProposal `json:"proposals"`
}
type StockIdleProposal struct {
	PoolID           string `json:"pool_id"`
	RepositoryID     string `json:"repository_id"`
	ObservedMinIdle  int64  `json:"observed_min_idle"`
	SuggestedMinIdle int64  `json:"suggested_min_idle"`
	UnchangedMaximum int64  `json:"unchanged_maximum"`
}
type stockIdleHistory struct{ prior map[string]stockIdleSample }

func (h *stockIdleHistory) reset() { h.prior = nil }

// Preserve snapshot classification while exposing only the closed scope-limit reason.
var errStockIdleScopeLimit = fmt.Errorf("stock idle preview scope limit exceeded: %w", capacitybroker.ErrSnapshot)

// Bounds expensive canonical/hash checks independently of installation size.
// Every target uses the SAME authenticated capture; this adds no API reads.
func sampleStockIdlePreview(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, observation garmapi.DemandObservation, source string, opt capacitybroker.Options) (map[string]stockIdleSample, error) {
	if len(state.Config.Pools) > 8 || len(state.Policy.Repositories) > 8 || len(observation.Pools) > 8 {
		return nil, errStockIdleScopeLimit
	}
	if len(state.Config.Pools) == 0 || len(state.Policy.Repositories) == 0 {
		return nil, capacitybroker.ErrSnapshot
	}
	out := map[string]stockIdleSample{}
	for id := range state.Config.Pools {
		sample, err := sampleStockIdleAdvice(state, charges, observation, source, id, opt)
		if err == nil {
			out[id] = sample
		}
	}
	return out, nil
}
func (h *stockIdleHistory) accept(samples map[string]stockIdleSample, err error, now, expiry time.Time) *StockIdlePreview {
	out := &StockIdlePreview{State: "unavailable", Reason: "evidence_unavailable", ExpiresAt: expiry, Proposals: []StockIdleProposal{}}
	if err != nil || len(samples) == 0 || len(samples) > 8 {
		if errors.Is(err, errStockIdleScopeLimit) {
			out.Reason = "scope_limit_exceeded"
		}
		h.reset()
		return out
	}
	out.State = "no_confirmed_proposals"
	out.Reason = "awaiting_matching_sample"
	out.HostEvidence = "configured_only"
	ids := make([]string, 0, len(samples))
	for id := range samples {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	next := map[string]stockIdleSample{}
	for _, id := range ids {
		cur := samples[id]
		if !cur.idleScopeValid(now) {
			continue
		}
		next[id] = cur
		if cur.expiry.Before(out.ExpiresAt) {
			out.ExpiresAt = cur.expiry
		}
		old, exists := h.prior[id]
		if !exists || cur.observation.StartedAt.Before(old.observation.FinishedAt.Add(15*time.Second)) {
			continue
		}
		advice, ok := proposeStockIdleDecrease(old, cur, now)
		if !ok {
			continue
		}
		out.Proposals = append(out.Proposals, StockIdleProposal{advice.PoolID, advice.RepositoryID, advice.Observed, advice.Suggested, advice.UnchangedMaximum})
		if advice.ExpiresAt.Before(out.ExpiresAt) {
			out.ExpiresAt = advice.ExpiresAt
		}
	}
	h.prior = next
	if len(next) == 0 {
		h.reset()
		out.State = "unavailable"
		out.Reason = "evidence_unavailable"
		out.HostEvidence = ""
		return out
	}
	if len(out.Proposals) > 0 {
		out.State = "proposed"
		out.Reason = ""
	}
	return out
}
