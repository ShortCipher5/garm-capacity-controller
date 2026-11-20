package brokerinput

import (
	"crypto/sha256"
	"reflect"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

type stockIdleDecisionSample struct {
	observation          garmapi.DemandObservation
	scope                [sha256.Size]byte
	source               string
	revision, sequence   int64
	expiry, growthExpiry time.Time
	maxAge               time.Duration
	repos                []string
	settings             map[string]string
	cuts                 map[string]stockIdleSample
	growth               map[string]StockIdleProposal
	growthOrder          []string
}

// sampleStockIdleDecision is an offline input adapter. It obtains no new data,
// performs no operations, and uses only enrolled and scoped observations. If
// separately applied, a higher floor can cause stock to schedule warm work
// within the unchanged maximum. This advice is not authority or headroom.
func sampleStockIdleDecision(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, observation garmapi.DemandObservation, source string, opt capacitybroker.Options, pressure []PressureStatus) (stockIdleDecisionSample, error) {
	fail := func() (stockIdleDecisionSample, error) { return stockIdleDecisionSample{}, capacitybroker.ErrSnapshot }
	snapshot, _, err := enrolledPlanningSnapshot(state, charges, observation, source, opt)
	if err != nil {
		return fail()
	}
	cuts, err := sampleStockIdlePreview(state, charges, observation, source, opt)
	if err != nil {
		return fail()
	}
	return sampleStockIdleDecisionFromCapture(state, snapshot, observation, source, opt, pressure, cuts)
}

// Only use the exact snapshot and cut samples already validated for this
// capture. The read-only plan reader owns them until publication.
func sampleStockIdleDecisionFromCapture(state admission.BrokerGrantState, snapshot capacitybroker.Snapshot, observation garmapi.DemandObservation, source string, opt capacitybroker.Options, pressure []PressureStatus, cuts map[string]stockIdleSample) (stockIdleDecisionSample, error) {
	fail := func() (stockIdleDecisionSample, error) { return stockIdleDecisionSample{}, capacitybroker.ErrSnapshot }
	if source == "" || observation.SourceIdentity() != source || opt.Now.IsZero() ||
		opt.MaxAge <= 0 || opt.MaxAge > time.Minute || len(state.Config.Pools) == 0 ||
		len(state.Config.Pools) > 8 || len(state.Policy.Repositories) == 0 ||
		len(state.Policy.Repositories) > 8 || len(observation.Pools) > 8 {
		return fail()
	}
	scope, err := scalePreviewScope(state, snapshot)
	if err != nil || scope == ([sha256.Size]byte{}) {
		return fail()
	}
	expiry := projectReadPlan(state, observation, capacitybroker.JobPlan{}, opt.Now, opt.MaxAge).ExpiresAt
	if !expiry.After(opt.Now) {
		return fail()
	}
	out := stockIdleDecisionSample{
		observation: observation, scope: scope, source: source, sequence: state.Sequence, revision: state.Policy.Revision,
		expiry: expiry, maxAge: opt.MaxAge, settings: map[string]string{}, cuts: cuts,
	}
	for _, r := range state.Policy.Repositories {
		out.repos = append(out.repos, r.ID)
	}
	for _, p := range observation.Pools {
		if p.DiscoveryFingerprint == "" {
			return fail()
		}
		out.settings[p.ID] = p.DiscoveryFingerprint
	}
	for id, cut := range cuts {
		if id != cut.pool || cut.source != source || cut.scope != scope || cut.revision != out.revision ||
			cut.sequence != out.sequence || cut.maxAge != opt.MaxAge || !cut.expiry.Equal(expiry) ||
			cut.observation.Digest != observation.Digest || !cut.observation.StartedAt.Equal(observation.StartedAt) ||
			!cut.observation.FinishedAt.Equal(observation.FinishedAt) {
			return fail()
		}
	}
	out.growthExpiry = stockMaximumPressureExpiry(snapshot, pressure, opt.Now)
	if out.growthExpiry.After(expiry) {
		out.growthExpiry = expiry
	}
	if out.growthExpiry.After(opt.Now) && out.quietScope(opt.Now) {
		out.growth, out.growthOrder = planStockIdleGrowth(snapshot, state.Policy, observation, opt)
	}
	return out, nil
}

func (s stockIdleDecisionSample) quietScope(now time.Time) bool {
	if s.source == "" || s.source != s.observation.SourceIdentity() || len(s.observation.Jobs) != 0 {
		return false
	}
	// A quiet subset is not enough for an increase: scoped scale-set
	// rows may be excluded from ordinary Jobs but block classification
	// for another configured source repository.
	scopes := s.observation.ExactRepositoryIdleScopes(s.repos, now, s.maxAge)
	if len(s.repos) == 0 || len(scopes) != len(s.repos) {
		return false
	}
	for _, repo := range s.repos {
		if !scopes[repo].Classified() {
			return false
		}
	}
	return true
}

// Plan configured minimum maintenance and at most one extra warm slot per
// eligible pool together. Existing floors and charged work must fit before a
// new floor can recommend filling a slot. Stock and policy limits both apply.
func planStockIdleGrowth(snapshot capacitybroker.Snapshot, policy admission.BrokerPolicy, observation garmapi.DemandObservation, opt capacitybroker.Options) (map[string]StockIdleProposal, []string) {
	if len(observation.Jobs) != 0 {
		return nil, nil
	}
	remote := map[string]garmapi.DiscoveredPool{}
	supply := map[string]garmapi.RunnerSupply{}
	for _, p := range observation.Pools {
		remote[p.ID] = p
	}
	for _, s := range observation.Supply {
		supply[s.PoolID] = s
	}
	targets := map[string]StockIdleProposal{}
	baseline := map[string]int64{}
	needed := map[string]int64{}
	modeled := policy
	modeled.Repositories = append([]admission.BrokerRepositoryPolicy(nil), policy.Repositories...)
	global := snapshot.Global
	for i, r := range modeled.Repositories {
		modeled.Repositories[i].Pools = append([]admission.BrokerPoolPolicy(nil), r.Pools...)
		repoLeft := r.MaxRunners
		for j, pool := range modeled.Repositories[i].Pools {
			p, seen := remote[pool.ID]
			local, exists := snapshot.Pools[pool.ID]
			if !seen || !exists || p.RepositoryID != r.ID || local.Budget == nil || p.MaxRunners > 1000000 || p.MinIdleRunners > p.MaxRunners {
				return nil, nil
			}
			delete(remote, pool.ID)
			if !p.Enabled {
				continue // stockPlan keeps all charges but vetoes new work.
			}
			s, haveSupply := supply[pool.ID]
			cap, floor := int64(p.MaxRunners), int64(p.MinIdleRunners)
			if !haveSupply || s.Idle < 0 || s.Busy < 0 || s.Creating != 0 || s.Other != 0 || local.PendingWarm != 0 || cap == 0 ||
				cap > repoLeft || !subtractStockMaximumModel(&global, *local.Budget, local.Size, cap) {
				return nil, nil
			}
			repoLeft -= cap
			model := &modeled.Repositories[i].Pools[j]
			model.MaxRunners = min(pool.MaxRunners, cap)
			if floor > model.MaxRunners {
				return nil, nil
			}
			model.WarmIdle = floor
			baseline[p.ID] = max(0, floor-s.Idle)
			if floor < min(pool.WarmIdle, model.MaxRunners) && s.Idle <= floor {
				model.WarmIdle++
				needed[p.ID] = floor + 1 - s.Idle
				targets[p.ID] = StockIdleProposal{PoolID: p.ID, RepositoryID: r.ID, ObservedMinIdle: floor, SuggestedMinIdle: floor + 1, UnchangedMaximum: cap}
			}
		}
	}
	for _, p := range remote {
		if p.Enabled { // No optimistic growth with incomplete configured exposure.
			return nil, nil
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	plan, err := stockPlan(snapshot, modeled, observation, opt, true, true)
	if err != nil || len(plan.RemainingJobs) != 0 {
		return nil, nil
	}
	counts := map[string]int64{}
	selected := map[string]StockIdleProposal{}
	var order []string
	for _, a := range plan.Assignments {
		if !a.Warm {
			return nil, nil
		}
		counts[a.Pool]++
		if n, ok := needed[a.Pool]; ok && counts[a.Pool] == n {
			selected[a.Pool] = targets[a.Pool]
			order = append(order, a.Pool)
		}
	}
	for id, n := range baseline {
		if counts[id] < n {
			return nil, nil
		}
	}
	return selected, order
}

// Independent configured-maxima veto. It is not combined with actual charges:
// stockPlan independently preserves those exact charges and tests the warm fit.
func subtractStockMaximumModel(global *capacitybroker.Resources, pool, size capacitybroker.Resources, count int64) bool {
	for d, unit := range size {
		if unit <= 0 || count <= 0 || count > min(global[d], pool[d])/unit || global[d] < 0 || pool[d] < 0 {
			return false
		}
	}
	for d, unit := range size {
		global[d] -= count * unit
	}
	return true
}

// At most one minimum change is recommended, and the maximum stays fixed.
// A cut never contributes to the calculation (or authority) of an increase.
func proposeStockIdleDecision(previous, current stockIdleDecisionSample, now time.Time) (stockIdleAdvice, bool) {
	if now.IsZero() || previous.source != current.source || current.source == "" ||
		current.scope == ([sha256.Size]byte{}) || previous.scope != current.scope || previous.revision != current.revision ||
		previous.sequence > current.sequence || !previous.expiry.After(now) || !current.expiry.After(now) ||
		current.observation.FinishedAt.After(now) ||
		current.observation.StartedAt.Before(previous.observation.FinishedAt.Add(15*time.Second)) ||
		!reflect.DeepEqual(previous.settings, current.settings) {
		return stockIdleAdvice{}, false
	}
	if previous.growthExpiry.After(now) && current.growthExpiry.After(now) && previous.quietScope(now) && current.quietScope(now) {
		for _, id := range current.growthOrder {
			row, ok := current.growth[id]
			if old, exists := previous.growth[id]; ok && exists && old == row {
				expiry := current.growthExpiry
				if previous.growthExpiry.Before(expiry) {
					expiry = previous.growthExpiry
				}
				return stockIdleAdvice{PoolID: id, RepositoryID: row.RepositoryID, Observed: row.ObservedMinIdle, Suggested: row.SuggestedMinIdle, UnchangedMaximum: row.UnchangedMaximum, ExpiresAt: expiry}, true
			}
		}
	}
	ids := make([]string, 0, len(current.cuts))
	for id := range current.cuts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if advice, ok := proposeStockIdleDecrease(previous.cuts[id], current.cuts[id], now); ok {
			return advice, true
		}
	}
	return stockIdleAdvice{}, false
}
