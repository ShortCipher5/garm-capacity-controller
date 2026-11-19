package brokerinput

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// StockMaximumPreview is an optional authenticated advisory comparison. It
// exposes no job identities, setting write fingerprints, or runner authority.
type StockMaximumPreview struct {
	State     string                 `json:"state"`
	ExpiresAt time.Time              `json:"expires_at"`
	Proposals []StockMaximumProposal `json:"proposals"`
}

type scalingPreviewSample struct {
	StockScalingSample
	scope [sha256.Size]byte
}

type scalingPreviewHistory struct {
	prior scalingPreviewSample
}

func sampleStockMaximumPreview(state admission.BrokerGrantState, snapshot capacitybroker.Snapshot, observation garmapi.DemandObservation, source string, opt capacitybroker.Options, pressure []PressureStatus) (scalingPreviewSample, error) {
	// Pressure is strictly a veto. Refuse preview when any mapped host
	// has a missing, stale, high or incomplete sample; do not change the
	// separate advisory result or treat partial low pressure as headroom.
	pressureExpiry := stockMaximumPressureExpiry(snapshot, pressure, opt.Now)
	if pressureExpiry.IsZero() {
		return scalingPreviewSample{}, capacitybroker.ErrSnapshot
	}
	plan, err := stockPlan(snapshot, state.Policy, observation, opt, true, true)
	if err != nil {
		return scalingPreviewSample{}, err
	}
	sample, err := stockScalingSample(state, snapshot, observation, plan, source, opt)
	if err != nil {
		return scalingPreviewSample{}, err
	}
	if pressureExpiry.Before(sample.expires) {
		sample.expires = pressureExpiry
	}
	scope, err := scalePreviewScope(state, snapshot)
	if err != nil {
		return scalingPreviewSample{}, err
	}
	return scalingPreviewSample{StockScalingSample: sample, scope: scope}, nil
}

func stockMaximumPressureExpiry(snapshot capacitybroker.Snapshot, pressure []PressureStatus, now time.Time) time.Time {
	if len(pressure) == 0 || len(pressure) != len(snapshot.Hosts) {
		return time.Time{}
	}
	var expiry time.Time
	seen := map[string]bool{}
	for _, status := range pressure {
		deadline := status.SampledAt.Add(pressureAge)
		if _, known := snapshot.Hosts[status.Host]; !known || seen[status.Host] || status.State != "partial_sample_not_headroom" ||
			status.SampledAt.IsZero() || status.SampledAt.After(now) || !deadline.After(now) {
			return time.Time{}
		}
		seen[status.Host] = true
		if expiry.IsZero() || deadline.Before(expiry) {
			expiry = deadline
		}
	}
	return expiry
}

// History cannot confirm a recommendation across effective configuration or
// enrollment changes. Frequent observation timestamps and ordinary ledger
// sequence advances are not policy edits.
func scalePreviewScope(state admission.BrokerGrantState, snapshot capacitybroker.Snapshot) ([sha256.Size]byte, error) {
	bindings, profiles := map[string]int64{}, map[string]int64{}
	for host := range snapshot.Hosts {
		bindings[host] = state.HostBindings[host].Sequence
	}
	for pool, profile := range state.PoolProfiles {
		profiles[pool] = profile.Sequence
	}
	raw, err := json.Marshal(struct {
		Global             capacitybroker.Resources
		Hosts              map[string]capacitybroker.Host
		Pools              map[string]admission.Pool
		Bindings, Profiles map[string]int64
		MaxAge             time.Duration
	}{snapshot.Global, snapshot.Hosts, state.Config.Pools, bindings, profiles, state.Config.MaxObservationAge})
	if err != nil {
		return [sha256.Size]byte{}, capacitybroker.ErrSnapshot
	}
	return sha256.Sum256(raw), nil
}

func (h *scalingPreviewHistory) reset() {
	h.prior = scalingPreviewSample{}
}

// The reader holds its existing observation mutex and has already reread the
// exact ledger revision and source after collecting the current comparison.
func (h *scalingPreviewHistory) accept(current scalingPreviewSample, err error, now, ordinaryExpiry time.Time) *StockMaximumPreview {
	out := &StockMaximumPreview{State: "unavailable", ExpiresAt: ordinaryExpiry, Proposals: []StockMaximumProposal{}}
	if err != nil || current.source == "" || current.incomplete || !current.expires.After(now) {
		h.reset()
		return out
	}
	out.State = "no_confirmed_proposals"
	var changes []StockMaximumProposal
	if h.prior.scope == current.scope {
		changes = ProposeStockMaximums(h.prior.StockScalingSample, current.StockScalingSample, now)
	}
	if len(changes) != 0 {
		out.State = "proposed"
		out.Proposals = changes
		if h.prior.expires.Before(out.ExpiresAt) {
			out.ExpiresAt = h.prior.expires
		}
	}
	if current.expires.Before(out.ExpiresAt) {
		out.ExpiresAt = current.expires
	}
	h.prior = current
	return out
}
