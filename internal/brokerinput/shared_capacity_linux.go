// Offline shared-capacity planning for ordinary repository pools. Live enforcement
// requires independently qualified stop/recovery (#397), slot reuse (#396), and
// the same durable admission limit on every relevant stock creation path.
package brokerinput

import (
	"cmp"
	"maps"
	"slices"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// SharedCapacityRemaining is measured before proposed assignments. Global, host and pool
// budgets include CPU, RAM, disk and runner slots. Policy separately caps
// repository and pool runner counts.
type SharedCapacityRemaining struct {
	Global            capacitybroker.Resources            `json:"global"`
	Hosts             map[string]capacitybroker.Resources `json:"hosts"`
	Pools             map[string]capacitybroker.Resources `json:"pools"`
	PoolRunners       map[string]int64                    `json:"pool_runners"`
	RepositoryRunners map[string]int64                    `json:"repository_runners"`
}

type SharedCapacityRefusal struct {
	Kind   string `json:"kind"`
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

const (
	SharedCapacityOverage        = "budget_drifted_overage"
	SharedCapacityPoolCap        = "pool_runner_cap_reached"
	SharedCapacityRepoCap        = "repository_runner_cap_reached"
	SharedCapacityPoolBudget     = "pool_budget_exhausted"
	SharedCapacityGlobalBudget   = "global_budget_exhausted"
	SharedCapacityNoEligibleHost = "no_eligible_host_headroom"
	SharedCapacityPlanLimit      = "assignment_limit_reached"
)

type SharedCapacityDecision struct {
	Remaining SharedCapacityRemaining `json:"remaining"`
	Plan      capacitybroker.JobPlan  `json:"plan"`
	Refusals  []SharedCapacityRefusal `json:"refusals"`
}

// EvaluateSharedCapacity compares one coherent ReadBrokerPlanningState result
// with one bounded read-only stock capture. Source and scope, fresh inventory,
// enabled pools and exact runner/ledger identities are checked before matching.
// The result is advisory and has no grant, settings or execution capability.
func EvaluateSharedCapacity(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, observed garmapi.DemandObservation, source string, opt capacitybroker.Options) (SharedCapacityDecision, error) {
	if len(charges) > 100000 || len(state.Policy.Repositories) > 1000 || len(observed.Pools) > 1000 || len(observed.Supply) > 1000 || len(observed.Jobs) > 1000 || len(observed.Runners) > 10000 {
		return SharedCapacityDecision{}, capacitybroker.ErrSnapshot
	}
	repos := make([]string, 0, len(state.Policy.Repositories))
	for _, r := range state.Policy.Repositories {
		repos = append(repos, r.ID)
	}
	// Verify the transport-sealed capture, including explicit zero supply rows.
	// Classification concerns idle reductions; this comparison only plans growth.
	if observed.ExactRepositoryIdleScopes(repos, opt.Now, opt.MaxAge) == nil {
		return SharedCapacityDecision{}, capacitybroker.ErrSnapshot
	}
	for _, p := range observed.Pools {
		if profile, enrolled := state.PoolProfiles[p.ID]; enrolled && (p.OwnershipFingerprint == "" || p.ProviderName != profile.Authority.ProviderName) {
			return SharedCapacityDecision{}, capacitybroker.ErrSnapshot
		}
	}
	charges = slices.Clone(charges)
	slices.SortFunc(charges, func(a, b admission.BrokerPlanningCharge) int { return cmp.Compare(a.ID, b.ID) })
	snapshot, _, err := enrolledPlanningSnapshot(state, charges, observed, source, opt)
	if err != nil {
		return SharedCapacityDecision{}, err
	}
	plan, err := stock(snapshot, state.Policy, observed, opt, true)
	if err != nil {
		return SharedCapacityDecision{}, err
	}
	held, err := allocatorHeld(snapshot)
	if err != nil {
		return SharedCapacityDecision{}, err
	}
	if !sharedCapacityTotalsMatch(state, held) {
		return SharedCapacityDecision{}, capacitybroker.ErrSnapshot
	}
	post, err := allocatorPlanned(snapshot, plan, held)
	if err != nil {
		return SharedCapacityDecision{}, err
	}
	out := SharedCapacityDecision{Plan: plan}
	out.Remaining = SharedCapacityRemaining{
		Hosts: map[string]capacitybroker.Resources{}, Pools: map[string]capacitybroker.Resources{},
		PoolRunners: map[string]int64{}, RepositoryRunners: map[string]int64{},
	}
	budgetLeft := func(kind, id string, budget, used capacitybroker.Resources) capacitybroker.Resources {
		if !allocatorFits(used, capacitybroker.Resources{}, budget) {
			out.Refusals = append(out.Refusals, SharedCapacityRefusal{kind, id, SharedCapacityOverage})
		}
		for i := range budget {
			budget[i] = max(0, budget[i]-used[i])
		}
		return budget
	}
	// The coherent allocation aggregates include domain and artifact overhead
	// even when it alone already exceeds a configured budget.
	out.Remaining.Global = budgetLeft("global", "global", resources(state.Config.Global), resources(state.Allocated.Global))
	for id, h := range state.Config.Hosts {
		out.Remaining.Hosts[id] = budgetLeft("host", id, resources(h.Budget), resources(state.Allocated.Hosts[id]))
	}
	for id, p := range snapshot.Pools {
		out.Remaining.Pools[id] = budgetLeft("pool", id, *p.Budget, held.pools[id])
		out.Remaining.PoolRunners[id] = max(0, p.MaxRunners-held.pools[id][3])
		if held.pools[id][3] > p.MaxRunners {
			out.Refusals = append(out.Refusals, SharedCapacityRefusal{"pool_runner_cap", id, SharedCapacityOverage})
		}
	}
	for id, r := range snapshot.Repositories {
		out.Remaining.RepositoryRunners[id] = max(0, r.MaxRunners-held.repos[id][3])
		if held.repos[id][3] > r.MaxRunners {
			out.Refusals = append(out.Refusals, SharedCapacityRefusal{"repository", id, SharedCapacityOverage})
		}
	}

	unserved := map[string]bool{}
	for _, j := range plan.RemainingJobs {
		for _, id := range j.Candidates {
			unserved[id] = true
		}
	}
	// Only spare idle and pending supply counts toward a floor. Each job
	// match spends its supply once. Surplus never cancels a queued refusal.
	spare := map[string]int64{}
	for _, s := range observed.Supply {
		spare[s.PoolID] = s.Idle + snapshot.Pools[s.PoolID].PendingWarm
	}
	for _, matches := range []map[string]string{plan.CoveredByIdle, plan.CoveredByGrant, plan.DeferredByInflight} {
		for _, id := range matches {
			spare[id]--
		}
	}
	for _, a := range plan.Assignments {
		if a.Warm {
			spare[a.Pool]++
		}
	}
	for _, remote := range observed.Pools {
		p, known := snapshot.Pools[remote.ID]
		if !known || !remote.Enabled || (!unserved[remote.ID] && spare[remote.ID] >= p.WarmIdle) {
			continue
		}
		reason := sharedCapacityBlocker(snapshot, post, remote.ID)
		if reason == "" {
			if len(plan.Assignments) != opt.MaxAssignments {
				return SharedCapacityDecision{}, capacitybroker.ErrSnapshot
			}
			reason = SharedCapacityPlanLimit
		}
		out.Refusals = append(out.Refusals, SharedCapacityRefusal{"pool", remote.ID, reason})
	}
	slices.SortFunc(out.Refusals, func(a, b SharedCapacityRefusal) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.Reason, b.Reason))
	})
	out.Refusals = slices.Compact(out.Refusals)
	return out, nil
}

// Match the planner's detached charge list to the ledger aggregates before
// reporting headroom. Overhead is already subtracted in the planner snapshot.
func sharedCapacityTotalsMatch(state admission.BrokerGrantState, held allocatorHeldView) bool {
	global, ok := allocatorAdd(held.global, resources(state.Allocated.DomainOverhead))
	if !ok {
		return false
	}
	global, ok = allocatorAdd(global, resources(state.Allocated.ArtifactOverhead))
	if !ok || global != resources(state.Allocated.Global) {
		return false
	}
	for id := range state.Config.Pools {
		if held.pools[id] != resources(state.Allocated.Pools[id]) {
			return false
		}
	}
	// overheadBudgets validated and bounded the per-host entries.
	hosts := maps.Clone(held.hosts)
	for _, o := range state.DomainOverheads {
		hosts[o.Host], ok = allocatorAdd(hosts[o.Host], resources(o.Resources))
		if !ok {
			return false
		}
	}
	for _, o := range state.ArtifactOverheads {
		hosts[o.Host], ok = allocatorAdd(hosts[o.Host], resources(o.Resources))
		if !ok {
			return false
		}
	}
	for id := range state.Config.Hosts {
		if hosts[id] != resources(state.Allocated.Hosts[id]) {
			return false
		}
	}
	return true
}

func sharedCapacityBlocker(s capacitybroker.Snapshot, post allocatorHeldView, id string) string {
	p := s.Pools[id]
	switch {
	case post.pools[id][3] >= p.MaxRunners:
		return SharedCapacityPoolCap
	case post.repos[p.Repository][3] >= s.Repositories[p.Repository].MaxRunners:
		return SharedCapacityRepoCap
	case !allocatorFits(post.pools[id], p.Size, *p.Budget):
		return SharedCapacityPoolBudget
	case !allocatorFits(post.global, p.Size, s.Global):
		return SharedCapacityGlobalBudget
	}
	for _, id := range p.Hosts {
		h := s.Hosts[id]
		if h.AcceptNew && allocatorFits(post.hosts[id], p.Size, h.Budget) {
			return ""
		}
	}
	return SharedCapacityNoEligibleHost
}
