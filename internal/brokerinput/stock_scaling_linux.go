package brokerinput

import (
	"math"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// StockScalingSample is an in-memory, read-only comparison. It is not a GARM
// write precondition, reservation, release proof or physical resource limit.
type StockScalingSample struct {
	source                     string
	sequence, revision, budget int64
	start, finish, expires     time.Time
	pools                      map[string]scalingPool
	repoCaps                   map[string]int64
	order                      []string
	outside                    int64
	incomplete                 bool
}

type scalingPool struct {
	repo, fingerprint        string
	enabled                  bool
	current, idle, cap, used int64
	target                   int64
}

// StockMaximumProposal is advice about one positive stock setting, not a
// promise that stock GARM will start or stop a runner.
type StockMaximumProposal struct {
	PoolID       string `json:"pool_id"`
	RepositoryID string `json:"repository_id"`
	Observed     int64  `json:"observed"`
	Suggested    int64  `json:"suggested"`
	// ObservedMinIdle is the unchanged stock setting, not a recommendation to
	// adjust idle runners. A pointer preserves a verified zero versus absence.
	ObservedMinIdle *int64 `json:"observed_min_idle,omitempty"`
}

// SampleStockScaling reuses the ledger- and ownership-checked weighted broker
// for demand allocation. The caller owns acquisition and any final source or
// revision reread; this calculation neither polls nor changes state.
func SampleStockScaling(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, observation garmapi.DemandObservation, source string, opt capacitybroker.Options) (StockScalingSample, error) {
	snapshot, _, err := enrolledPlanningSnapshot(state, charges, observation, source, opt)
	if err != nil {
		return StockScalingSample{}, err
	}
	plan, err := stockPlan(snapshot, state.Policy, observation, opt, true, true)
	if err != nil {
		return StockScalingSample{}, err
	}
	return stockScalingSample(state, snapshot, observation, plan, source, opt)
}

func stockScalingSample(state admission.BrokerGrantState, snapshot capacitybroker.Snapshot, observation garmapi.DemandObservation, plan capacitybroker.JobPlan, source string, opt capacitybroker.Options) (StockScalingSample, error) {
	fail := func() (StockScalingSample, error) { return StockScalingSample{}, capacitybroker.ErrSnapshot }
	view := projectReadPlan(state, observation, plan, opt.Now, opt.MaxAge)
	out := StockScalingSample{
		source: source, sequence: view.LedgerSequence, revision: view.PolicyRevision,
		budget: snapshot.Global[3], start: view.StartedAt, finish: view.FinishedAt, expires: view.ExpiresAt,
		pools: map[string]scalingPool{}, repoCaps: map[string]int64{},
	}
	if out.source == "" || !out.expires.After(opt.Now) || out.start.IsZero() || out.finish.Before(out.start) || out.finish.After(opt.Now) || out.budget < 0 {
		return fail()
	}
	pools := map[string]garmapi.DiscoveredPool{}
	for _, remote := range observation.Pools {
		pools[remote.ID] = remote
	}
	projections := map[string]ReadPlanPool{}
	for _, row := range view.Pools {
		projections[row.Pool] = row
	}
	// Recheck runner accounting against the exact charge list that produced
	// the weighted plan, not a separately supplied total.
	usedByPool := map[string]int64{}
	total := int64(0)
	for _, c := range snapshot.Charges {
		if n := c.Resources[3]; n < 0 || n > math.MaxInt64-total {
			return fail()
		} else {
			total += n
			usedByPool[c.Pool] += n
		}
	}
	if state.Allocated.Global.Runners != total {
		return fail()
	}
	for _, repo := range state.Policy.Repositories {
		out.repoCaps[repo.ID] = repo.MaxRunners
		for _, desired := range repo.Pools {
			remote, ok := pools[desired.ID]
			row, rowOK := projections[desired.ID]
			poolBudget := snapshot.Pools[desired.ID].Budget
			if !ok || !rowOK || row.Supply == nil || poolBudget == nil || remote.DiscoveryFingerprint == "" || remote.MaxRunners > 1000000 || remote.MinIdleRunners > remote.MaxRunners ||
				row.Charged.Runners != usedByPool[desired.ID] || row.Charged.Runners < 0 || row.Charged.Runners > 100000 || row.Additional < 0 || row.Additional > 100000 {
				return fail()
			}
			p := scalingPool{
				repo: repo.ID, fingerprint: remote.DiscoveryFingerprint, enabled: remote.Enabled,
				current: int64(remote.MaxRunners), idle: int64(remote.MinIdleRunners),
				cap: min(desired.MaxRunners, poolBudget[3]), used: row.Charged.Runners,
			}
			if p.enabled && p.current == 0 {
				out.incomplete = true
			}
			p.target = p.current
			floor := max(1, p.idle, desired.WarmIdle, p.used)
			if !p.enabled || p.current == 0 || floor > p.cap {
				out.pools[desired.ID] = p
				continue
			}
			if row.Additional > 0 {
				p.target = max(p.current, min(p.cap, max(floor, p.used+row.Additional)))
			} else if view.RemainingJobs == 0 {
				// Unmatched demand might be label-compatible but infeasible on the
				// observed hosts. It is not evidence for shrinking a pool.
				// Keep one spare slot where policy permits: merely consuming
				// a slot must not reverse a just-suggested decrease.
				p.target = min(p.current, max(floor, min(p.cap, p.used+1)))
			}
			out.pools[desired.ID] = p
		}
	}
	for id, pool := range pools {
		if _, tracked := out.pools[id]; !tracked && (pool.Enabled || out.repoCaps[pool.RepositoryID] > 0) {
			out.incomplete = true
		}
	}
	// Keep all non-enrolled/orphan charges in the global denominator.
	out.outside = total
	for _, p := range out.pools {
		out.outside -= p.used
	}
	if out.outside < 0 {
		return fail()
	}
	seen := map[string]bool{}
	for _, a := range plan.Assignments {
		if !seen[a.Pool] {
			out.order = append(out.order, a.Pool)
			seen[a.Pool] = true
		}
	}
	return out, nil
}

// ProposeStockMaximums requires two disjoint, still-fresh observations of the
// same settings, policy and direction. It moves by only one and never spends
// headroom from a proposed reduction. This limits rapid reversals; it is not a
// durable cooldown or protection against races with external GARM writers.
func ProposeStockMaximums(previous, current StockScalingSample, now time.Time) []StockMaximumProposal {
	if current.source == "" || previous.source != current.source ||
		previous.revision != current.revision || previous.sequence > current.sequence ||
		previous.budget != current.budget || !previous.expires.After(now) || !current.expires.After(now) ||
		current.start.Before(previous.finish.Add(15*time.Second)) || current.finish.After(now) ||
		previous.incomplete || current.incomplete {
		return nil
	}
	if len(previous.pools) != len(current.pools) || len(previous.repoCaps) != len(current.repoCaps) {
		return nil
	}
	for repo, limit := range current.repoCaps {
		if prior, exist := previous.repoCaps[repo]; !exist || prior != limit {
			return nil
		}
	}
	for id, p := range current.pools {
		before, ok := previous.pools[id]
		if !ok || before.repo != p.repo || before.fingerprint != p.fingerprint ||
			before.enabled != p.enabled || before.current != p.current || before.idle != p.idle || before.cap != p.cap {
			return nil
		}
	}
	var out []StockMaximumProposal
	global := current.outside
	repo := map[string]int64{}
	for _, p := range current.pools {
		baseline := p.used
		if p.enabled {
			baseline = max(baseline, p.current)
		}
		global += min(baseline, max(0, current.budget-global))
		repo[p.repo] += baseline
	}
	appendProposal := func(id string, p scalingPool, target int64) {
		idle := p.idle
		out = append(out, StockMaximumProposal{PoolID: id, RepositoryID: p.repo, Observed: p.current, Suggested: target, ObservedMinIdle: &idle})
	}
	ids := make([]string, 0, len(current.pools))
	for id := range current.pools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := current.pools[id]
		if p.target < p.current && previous.pools[id].target < p.current {
			appendProposal(id, p, p.current-1)
		}
	}
	// Reuse the weighted broker's deterministic allocation order for scarce
	// scoped cap headroom rather than prefer lexicographic repositories.
	considered := map[string]bool{}
	for _, id := range current.order {
		if considered[id] {
			continue
		}
		considered[id] = true
		p := current.pools[id]
		if p.target <= p.current || previous.pools[id].target <= p.current || global >= current.budget || repo[p.repo] >= current.repoCaps[p.repo] {
			continue
		}
		appendProposal(id, p, p.current+1)
		global++
		repo[p.repo]++
	}
	return out
}
