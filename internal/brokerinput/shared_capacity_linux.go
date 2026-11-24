// Bounded cross-repository shared capacity enforcement above unmodified
// stock GARM. The enforcer turns one coherent ledger revision, reviewed
// policy, enrolled effective runner profiles and deduplicated demand into a
// deterministic commitment batch, and commits it through the existing atomic
// durable grant boundary. Runner count, CPU and RAM are separate constraints
// at the global, host, repository and pool edges; pending and unresolved
// work stays charged; a reduced configured maximum never funds growth.
// GitHub keeps its queue and stock GARM keeps the runner lifecycle: a
// committed grant is a capacity charge, not a runner creation, and nothing
// here retries, deletes, refunds or mutates pool settings.
package brokerinput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

// SharedCapacityRefusal is one bounded, operator-visible reason a scope
// cannot grow. Codes are stable diagnostics, never permission states.
type SharedCapacityRefusal struct {
	Kind   string `json:"kind"` // "global", "host", "repository" or "pool"
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

const (
	// SharedCapacityOverage reports charges that already exceed a budget:
	// external drift or an exhausted scope. New admissions refuse; existing
	// work is never evicted and no instant contraction is promised.
	SharedCapacityOverage = "budget_drifted_overage"
	// SharedCapacityExhausted reports that remaining headroom cannot fit one
	// more runner of the pool's effective profile.
	SharedCapacityExhausted = "budget_exhausted"
	// SharedCapacityCapped reports a repository or pool runner-count ceiling.
	SharedCapacityCapped = "runner_cap_reached"
	// SharedCapacityNoEligibleHost reports that no enrolled host for the pool
	// currently accepts new work with the full runner footprint.
	SharedCapacityNoEligibleHost = "no_eligible_host_headroom"
)

// SharedCapacityDemand is deduplicated unmet demand for one pool: queued
// jobs after matching existing idle, creating and outstanding granted
// supply. The caller owns job-level deduplication across matching pools.
type SharedCapacityDemand struct {
	Waiting       int64
	OldestWaiting time.Time
}

// SharedCapacityRemaining reports the headroom a decision drew from, per
// constraint edge, after all charges including pending and unresolved work.
// A zero vector means no capacity, never unlimited capacity.
type SharedCapacityRemaining struct {
	Global capacitybroker.Resources            `json:"global"`
	Hosts  map[string]capacitybroker.Resources `json:"hosts"`
	Pools  map[string]capacitybroker.Resources `json:"pools"`
	Repos  map[string]int64                    `json:"repositories"`
}

// SharedCapacityDecision is one deterministic reconciliation outcome: the
// remaining budget it was computed from, the assignments, the exact grant
// batch to commit, and the visible refusals for demand that could not be
// served. It is not authority by itself; only CommitSharedCapacity through
// the durable CAS boundary creates a charge.
type SharedCapacityDecision struct {
	Revision  string                     `json:"revision"`
	Remaining SharedCapacityRemaining    `json:"remaining"`
	Plan      capacitybroker.Plan        `json:"plan"`
	Batch     admission.BrokerGrantBatch `json:"batch"`
	Refusals  []SharedCapacityRefusal    `json:"refusals"`
}

func remainingBudget(s capacitybroker.Snapshot) (SharedCapacityRemaining, map[string]int64, []SharedCapacityRefusal, error) {
	out := SharedCapacityRemaining{Hosts: map[string]capacitybroker.Resources{}, Pools: map[string]capacitybroker.Resources{}, Repos: map[string]int64{}}
	refusals := []SharedCapacityRefusal{}
	used := map[string]capacitybroker.Resources{}
	repoUsed := map[string]int64{}
	poolRunners := map[string]int64{}
	global := capacitybroker.Resources{}
	for _, c := range s.Charges {
		var ok bool
		global, ok = addResources(global, c.Resources)
		if !ok {
			return out, nil, nil, capacitybroker.ErrSnapshot
		}
		host := used["host:"+c.Host]
		host, ok = addResources(host, c.Resources)
		if !ok {
			return out, nil, nil, capacitybroker.ErrSnapshot
		}
		used["host:"+c.Host] = host
		if c.Pool != "" {
			pool, exists := s.Pools[c.Pool]
			if !exists {
				return out, nil, nil, capacitybroker.ErrSnapshot
			}
			p := used["pool:"+c.Pool]
			p, ok = addResources(p, c.Resources)
			if !ok {
				return out, nil, nil, capacitybroker.ErrSnapshot
			}
			used["pool:"+c.Pool] = p
			repoUsed[pool.Repository] += c.Resources[3]
			poolRunners[c.Pool] += c.Resources[3]
			if repoUsed[pool.Repository] < 0 || poolRunners[c.Pool] < 0 {
				return out, nil, nil, capacitybroker.ErrSnapshot
			}
		}
	}
	remaining := func(kind, scope string, budget, charge capacitybroker.Resources) capacitybroker.Resources {
		var left capacitybroker.Resources
		overage := false
		for i := range budget {
			if charge[i] > budget[i] {
				overage = true
				left[i] = 0
			} else {
				left[i] = budget[i] - charge[i]
			}
		}
		if overage {
			refusals = append(refusals, SharedCapacityRefusal{Kind: kind, Scope: scope, Reason: SharedCapacityOverage})
		}
		return left
	}
	out.Global = remaining("global", "global", s.Global, global)
	for id, h := range s.Hosts {
		out.Hosts[id] = remaining("host", id, h.Budget, used["host:"+id])
	}
	for id, p := range s.Pools {
		budget := capacitybroker.Resources{}
		if p.Budget != nil {
			budget = *p.Budget
		}
		out.Pools[id] = remaining("pool", id, budget, used["pool:"+id])
	}
	for id, r := range s.Repositories {
		left := r.MaxRunners - repoUsed[id]
		if left < 0 {
			refusals = append(refusals, SharedCapacityRefusal{Kind: "repository", Scope: id, Reason: SharedCapacityOverage})
			left = 0
		}
		out.Repos[id] = left
	}
	sort.Slice(refusals, func(i, j int) bool {
		if refusals[i].Kind != refusals[j].Kind {
			return refusals[i].Kind < refusals[j].Kind
		}
		return refusals[i].Scope < refusals[j].Scope
	})
	return out, poolRunners, refusals, nil
}

func addResources(a, b capacitybroker.Resources) (capacitybroker.Resources, bool) {
	for i := range a {
		if b[i] < 0 || a[i] > math.MaxInt64-b[i] {
			return capacitybroker.Resources{}, false
		}
		a[i] += b[i]
	}
	return a, true
}

// EvaluateSharedCapacity computes one deterministic bounded reconciliation
// from a coherent ledger revision. It is pure: no writes, no retries, no
// inference from blank history. Stale or incoherent input fails closed.
// Demand-bearing pools that cannot be served receive explicit refusals;
// served demand becomes a grant batch bound to the exact eligible host
// binding and enrolled effective profile revision.
func EvaluateSharedCapacity(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, profiles map[string]LedgerProfile, demand map[string]SharedCapacityDemand, opt capacitybroker.Options, ttl time.Duration) (SharedCapacityDecision, error) {
	fail := func() (SharedCapacityDecision, error) { return SharedCapacityDecision{}, capacitybroker.ErrSnapshot }
	if ttl < time.Minute || ttl > time.Hour || len(demand) > 1000 {
		return fail()
	}
	// Canonicalize charge order: the decision and its batch identity must not
	// depend on the caller's slice order. The ledger already serves charges
	// sorted; sorting here keeps the pure path equally stable.
	charges = append([]admission.BrokerPlanningCharge(nil), charges...)
	sort.Slice(charges, func(i, j int) bool { return charges[i].ID < charges[j].ID })
	snapshot, err := ledgerSnapshot(state, charges, profiles, opt)
	if err != nil {
		return fail()
	}
	for id, d := range demand {
		pool, exists := snapshot.Pools[id]
		if !exists || d.Waiting < 0 || d.Waiting > 1000000 || d.OldestWaiting.After(opt.Now) || (d.Waiting > 0 && d.OldestWaiting.IsZero()) {
			return fail()
		}
		pool.Waiting = d.Waiting
		pool.OldestWaiting = d.OldestWaiting
		snapshot.Pools[id] = pool
	}
	remaining, poolRunners, refusals, err := remainingBudget(snapshot)
	if err != nil {
		return fail()
	}
	plan, err := capacitybroker.Build(snapshot, opt)
	if err != nil {
		return fail()
	}
	blocked := map[string]bool{}
	for _, r := range refusals {
		if r.Kind == "global" {
			for id := range snapshot.Pools {
				blocked[id] = true
			}
		}
	}
	served := map[string]int64{}
	batch := admission.BrokerGrantBatch{ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision}
	for _, a := range plan.Assignments {
		profile, ok := profiles[a.Pool]
		enrolled, enrolledOK := state.PoolProfiles[a.Pool]
		binding, boundOK := state.HostBindings[a.Host]
		if !ok || !enrolledOK || !boundOK || profile.Authority != enrolled.Authority ||
			profile.Size != enrolled.Resources || profile.Repository != enrolled.Repository ||
			len(profile.Execution.Name) != 0 || enrolled.Execution.Name != "" {
			return fail()
		}
		served[a.Pool]++
		batch.Grants = append(batch.Grants, admission.BrokerGrantSpec{
			Authority: enrolled.Authority, Host: a.Host, HostBindingSequence: binding.Sequence,
			Resources: enrolled.Resources, Execution: enrolled.Execution, ExpiresAt: opt.Now.Add(ttl),
		})
	}
	// Refusal reasons are computed against post-decision headroom: capacity
	// committed to a competing pool in this same decision is spent, not
	// available, for the demand that lost.
	post := remaining
	post.Hosts = map[string]capacitybroker.Resources{}
	post.Pools = map[string]capacitybroker.Resources{}
	post.Repos = map[string]int64{}
	for k, v := range remaining.Hosts {
		post.Hosts[k] = v
	}
	for k, v := range remaining.Pools {
		post.Pools[k] = v
	}
	for k, v := range remaining.Repos {
		post.Repos[k] = v
	}
	for _, a := range plan.Assignments {
		for i := range post.Global {
			post.Global[i] -= a.Resources[i]
		}
		h, p := post.Hosts[a.Host], post.Pools[a.Pool]
		for i := range h {
			h[i] -= a.Resources[i]
			p[i] -= a.Resources[i]
		}
		post.Hosts[a.Host], post.Pools[a.Pool] = h, p
		post.Repos[snapshot.Pools[a.Pool].Repository]--
	}
	// Demand that received no commitment gets a bounded visible reason. This
	// covers queued demand and configured idle floors alike.
	for id, pool := range snapshot.Pools {
		waiting := demand[id].Waiting
		warm := pool.WarmIdle - pool.AvailableIdle - pool.PendingWarm
		if waiting+warm-served[id] <= 0 {
			continue
		}
		reason := ""
		switch {
		case blocked[id]:
			reason = SharedCapacityOverage
		case pool.MaxRunners <= poolRunners[id]+served[id]:
			reason = SharedCapacityCapped
		case post.Repos[pool.Repository] <= 0:
			reason = SharedCapacityCapped
		case post.Pools[id][3] <= 0 || post.Global[3] <= 0 ||
			post.Global[0] < pool.Size[0] || post.Global[1] < pool.Size[1]:
			reason = SharedCapacityExhausted
		default:
			fits := false
			for _, h := range pool.Hosts {
				host := snapshot.Hosts[h]
				if host.AcceptNew && post.Hosts[h][3] >= pool.Size[3] && post.Hosts[h][0] >= pool.Size[0] && post.Hosts[h][1] >= pool.Size[1] {
					fits = true
				}
			}
			if !fits {
				reason = SharedCapacityNoEligibleHost
			} else {
				reason = SharedCapacityCapped
			}
		}
		refusals = append(refusals, SharedCapacityRefusal{Kind: "pool", Scope: id, Reason: reason})
	}
	sort.Slice(refusals, func(i, j int) bool {
		if refusals[i].Kind != refusals[j].Kind {
			return refusals[i].Kind < refusals[j].Kind
		}
		if refusals[i].Scope != refusals[j].Scope {
			return refusals[i].Scope < refusals[j].Scope
		}
		return refusals[i].Reason < refusals[j].Reason
	})
	// The batch identity commits to the exact ledger revision, policy,
	// profiles, demand and selected assignments: an identical re-evaluation
	// replays idempotently, and any drift conflicts instead of double-spending.
	key, err := json.Marshal(struct {
		Version  string
		Revision string
		TTL      int64
		Demand   map[string]SharedCapacityDemand
		Grants   []admission.BrokerGrantSpec
	}{"shared-capacity-decision/v1", snapshot.Revision, int64(ttl), demand, batch.Grants})
	if err != nil {
		return fail()
	}
	sum := sha256.Sum256(key)
	batch.ID = hex.EncodeToString(sum[:])
	return SharedCapacityDecision{Revision: snapshot.Revision, Remaining: remaining, Plan: plan, Batch: batch, Refusals: refusals}, nil
}

// CommitSharedCapacity issues the decision's batch through the existing
// atomic durable boundary. The revision and policy CAS means a competing
// committer loses and must re-evaluate from fresh charges; an identical
// batch replays without allocating twice. An empty batch commits nothing.
// Committing never releases, retires or refunds any existing charge.
func CommitSharedCapacity(ctx context.Context, store *admission.SQLiteStore, decision SharedCapacityDecision) ([]admission.BrokerGrant, error) {
	if store == nil {
		return nil, capacitybroker.ErrSnapshot
	}
	if len(decision.Batch.Grants) == 0 {
		return nil, nil
	}
	return store.IssueBrokerGrants(ctx, decision.Batch)
}
