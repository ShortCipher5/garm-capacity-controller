// Package capacitybroker plans shared capacity for stock GARM. Plans are advisory:
// they are NOT reservations, provider permits, remote writes or release evidence.
package capacitybroker

import (
	"errors"
	"math"
	"math/big"
	"sort"
	"time"
)

var ErrSnapshot = errors.New("capacity broker snapshot invalid or unavailable")

// Resources is CPU millicores, RAM MiB, disk MiB, runners. Every dimension has
// an explicit finite budget; zero means no capacity, not unlimited capacity.
type Resources [4]int64

type Host struct {
	Budget    Resources
	AcceptNew bool
}
type Repository struct {
	// Weight zero defaults to equal weight (one). MaxRunners zero stops new work.
	Weight     uint32
	MaxRunners int64
}
type Pool struct {
	// Budget is an optional additional pool resource ceiling. Ledger adapters
	// always set it; nil preserves purely advisory callers without pool limits.
	Budget     *Resources
	Repository string
	Size       Resources
	Hosts      []string
	MaxRunners int64
	// Waiting is unmet queued demand AFTER matching existing idle/creating runners.
	// The adapter must deduplicate jobs across matching pools before planning.
	Waiting int64
	// WarmIdle is the desired spare idle count, not a guaranteed minimum.
	WarmIdle int64
	// AvailableIdle counts verified idle runners already included in Charges.
	AvailableIdle int64
	// PendingWarm counts outstanding/creating warm grants included in Charges.
	// Keep unresolved warm grants here until authoritatively retired or resolved.
	// OutstandingGrants are unused durable grants, not idle runners. They may
	// cover demand for planning only and must also be included in PendingWarm
	// and Charges. Matching one never redeems it or binds it to a GitHub job.
	OutstandingGrants int64
	// Inflight supply defers demand while creation/unknown outcome resolves.
	// It is already charged and must be disjoint from idle and unused grants.
	Inflight      int64
	PendingWarm   int64
	OldestWaiting time.Time
}

// Charge includes existing, unknown, deleting, orphan and outstanding granted
// capacity. An adapter must deduplicate linked resources and reservations. A
// reduced remote pool maximum never removes a charge. Empty Pool represents
// non-pool host usage and is still charged globally and on its host.
type Charge struct {
	ID, Host, Pool string
	Resources      Resources
}

// GroupHeadroom is the coherent ledger limit minus charged physical usage,
// including retained non-pool overhead. Snapshot.Charges is not subtracted from
// it again. Only additions selected by this plan consume remaining headroom.
type GroupHeadroom struct {
	ByHost    map[string]string
	ByPool    map[string]string
	Remaining map[string]Resources
}
type Snapshot struct {
	Groups *GroupHeadroom `json:",omitempty"`

	Revision     string
	ObservedAt   time.Time
	Global       Resources
	Hosts        map[string]Host
	Repositories map[string]Repository
	Pools        map[string]Pool
	Charges      []Charge
	// Complete is an adapter assertion, not proof of external writer fencing.
	Complete bool
}
type Options struct {
	Now            time.Time
	MaxAge         time.Duration
	MaxAssignments int
}
type Assignment struct {
	Pool, Host string
	// JobID is populated only by BuildForJobs and is advisory correlation, not a runner binding.
	JobID     string
	Warm      bool
	Resources Resources
}
type Plan struct {
	Revision    string
	ObservedAt  time.Time
	Assignments []Assignment
	// RemainingQueued excludes warm demand. No plan changes existing allocations.
	RemainingQueued map[string]int64
}

func valid(v Resources) bool {
	for _, n := range v {
		if n < 0 {
			return false
		}
	}
	return true
}
func add(a, b Resources) (Resources, bool) {
	for i := range a {
		if b[i] > math.MaxInt64-a[i] {
			return Resources{}, false
		}
		a[i] += b[i]
	}
	return a, true
}
func fits(used, request, budget Resources) bool {
	for i := range used {
		if used[i] > budget[i] || request[i] > budget[i]-used[i] {
			return false
		}
	}
	return true
}
func weight(r Repository) int64 {
	if r.Weight == 0 {
		return 1
	}
	return int64(r.Weight)
}
func share(used, total Resources, w int64) *big.Rat {
	result := new(big.Rat)
	for i, n := range used {
		if n == 0 {
			continue
		}
		// Zero global budget with a charge stops all new allocation before selection.
		if total[i] == 0 {
			continue
		}
		value := new(big.Rat).SetFrac(big.NewInt(n), new(big.Int).Mul(big.NewInt(total[i]), big.NewInt(w)))
		if value.Cmp(result) > 0 {
			result = value
		}
	}
	return result
}

// Build computes bounded deterministic weighted dominant-resource allocation.
// It prefers queued work over optional warm capacity, never evicts existing work,
// and is deliberately stateless. Durable grants, remote-write reconciliation,
// starvation protection for large jobs and provider admission are separate layers.
func Build(s Snapshot, opt Options) (Plan, error) {
	return build(s, opt, nil)
}

func build(s Snapshot, opt Options, jobs *jobQueue) (Plan, error) {
	if !s.Complete || s.Revision == "" || opt.Now.IsZero() || s.ObservedAt.IsZero() || s.ObservedAt.After(opt.Now) || opt.MaxAge <= 0 || opt.Now.Sub(s.ObservedAt) > opt.MaxAge || opt.MaxAssignments < 1 || opt.MaxAssignments > 10000 || !valid(s.Global) || len(s.Pools) > 1000 || len(s.Hosts) > 1000 || len(s.Charges) > 100000 {
		return Plan{}, ErrSnapshot
	}
	plannedByGroup := map[string]Resources{}
	if groups := s.Groups; groups != nil {
		if len(groups.Remaining) > 128 || len(groups.ByPool) > len(s.Pools) || len(groups.ByHost) > len(s.Hosts) {
			return Plan{}, ErrSnapshot
		}
		for id, budget := range groups.Remaining {
			if id == "" || !valid(budget) {
				return Plan{}, ErrSnapshot
			}
		}
		for host, id := range groups.ByHost {
			if _, ok := s.Hosts[host]; !ok {
				return Plan{}, ErrSnapshot
			}
			if _, ok := groups.Remaining[id]; !ok {
				return Plan{}, ErrSnapshot
			}
		}
		for pool, id := range groups.ByPool {
			if _, ok := s.Pools[pool]; !ok {
				return Plan{}, ErrSnapshot
			}
			if _, ok := groups.Remaining[id]; !ok {
				return Plan{}, ErrSnapshot
			}
		}
	}
	groupFits := func(pool, host string, size Resources) bool {
		groups := s.Groups
		if groups == nil {
			return true
		}
		id := groups.ByHost[host]
		return id == groups.ByPool[pool] && (id == "" || fits(plannedByGroup[id], size, groups.Remaining[id]))
	}
	global := Resources{}
	hosts := map[string]Resources{}
	repos := map[string]Resources{}
	pools := map[string]int64{}
	poolResources := map[string]Resources{}
	ids := map[string]bool{}
	keys := []string{}
	for id, h := range s.Hosts {
		if id == "" || !valid(h.Budget) {
			return Plan{}, ErrSnapshot
		}
	}
	for id, r := range s.Repositories {
		if id == "" || r.MaxRunners < 0 {
			return Plan{}, ErrSnapshot
		}
	}
	for id, p := range s.Pools {
		if (p.Budget != nil && !valid(*p.Budget)) || id == "" || !valid(p.Size) || p.Size[0] == 0 || p.Size[1] == 0 || p.Size[2] == 0 || p.Size[3] != 1 || p.MaxRunners < 0 || p.Waiting < 0 || p.WarmIdle < 0 || p.AvailableIdle < 0 || p.PendingWarm < 0 || p.OutstandingGrants < 0 || p.OutstandingGrants > p.PendingWarm || p.Inflight < 0 || p.Inflight > p.PendingWarm-p.OutstandingGrants || p.OldestWaiting.After(opt.Now) || (p.Waiting > 0 && p.OldestWaiting.IsZero()) {
			return Plan{}, ErrSnapshot
		}
		if _, ok := s.Repositories[p.Repository]; !ok {
			return Plan{}, ErrSnapshot
		}
		seen := map[string]bool{}
		for _, h := range p.Hosts {
			if _, ok := s.Hosts[h]; !ok || seen[h] {
				return Plan{}, ErrSnapshot
			}
			seen[h] = true
		}
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, c := range s.Charges {
		if c.ID == "" || ids[c.ID] || !valid(c.Resources) {
			return Plan{}, ErrSnapshot
		}
		ids[c.ID] = true
		if _, ok := s.Hosts[c.Host]; !ok {
			return Plan{}, ErrSnapshot
		}
		var ok bool
		global, ok = add(global, c.Resources)
		if !ok {
			return Plan{}, ErrSnapshot
		}
		hosts[c.Host], ok = add(hosts[c.Host], c.Resources)
		if !ok {
			return Plan{}, ErrSnapshot
		}
		if c.Pool != "" {
			p, exists := s.Pools[c.Pool]
			if !exists {
				return Plan{}, ErrSnapshot
			}
			repos[p.Repository], ok = add(repos[p.Repository], c.Resources)
			if !ok {
				return Plan{}, ErrSnapshot
			}
			if c.Resources[3] > math.MaxInt64-pools[c.Pool] {
				return Plan{}, ErrSnapshot
			}
			pools[c.Pool] += c.Resources[3]
			poolResources[c.Pool], ok = add(poolResources[c.Pool], c.Resources)
			if !ok {
				return Plan{}, ErrSnapshot
			}
		}
	}
	out := Plan{Revision: s.Revision, ObservedAt: s.ObservedAt, Assignments: []Assignment{}, RemainingQueued: map[string]int64{}}
	queued := map[string]int64{}
	warm := map[string]int64{}
	for _, id := range keys {
		p := s.Pools[id]
		if p.AvailableIdle > pools[id] || p.PendingWarm > pools[id]-p.AvailableIdle {
			return Plan{}, ErrSnapshot
		}
		queued[id] = p.Waiting
		warm[id] = max(int64(0), p.WarmIdle-p.AvailableIdle-p.PendingWarm)
		out.RemainingQueued[id] = p.Waiting
	}
	for phase := 0; phase < 2; phase++ {
		demand := queued
		if phase == 1 {
			demand = warm
		}
		for len(out.Assignments) < opt.MaxAssignments {
			selected, host := "", ""
			var best *big.Rat
			var bestOldest time.Time
			soleHostDemand := map[string]int{}
			for _, id := range keys {
				p := s.Pools[id]
				r := s.Repositories[p.Repository]
				if (p.Budget != nil && !fits(poolResources[id], p.Size, *p.Budget)) || pools[id] >= p.MaxRunners || repos[p.Repository][3] >= r.MaxRunners || !fits(global, p.Size, s.Global) {
					continue
				}
				// Only the sole fit (if any) matters here. For multiple fits,
				// the final host selection below breaks ties lexically.
				placement := ""
				fitCount := 0
				for _, h := range p.Hosts {
					if s.Hosts[h].AcceptNew && groupFits(id, h, p.Size) && fits(hosts[h], p.Size, s.Hosts[h].Budget) {
						if placement == "" {
							placement = h
						}
						fitCount++
						if fitCount == 2 {
							break
						} // Only unique versus multiple fits matters.
					}
				}
				if placement == "" {
					continue
				}
				oldest := p.OldestWaiting
				hasDemand := demand[id] > 0
				if phase == 0 && jobs != nil {
					path := jobs.path(id)
					if jobs.exhausted {
						return Plan{}, ErrSnapshot
					}
					hasDemand = len(path) > 0
					if hasDemand {
						oldest = jobs.rows[path[0].row].CreatedAt
					}
				}
				if !hasDemand {
					continue
				}
				if fitCount == 1 {
					soleHostDemand[placement]++
				}
				score := share(repos[p.Repository], s.Global, weight(r))
				earlier := selected != "" && phase == 0 && oldest.Before(bestOldest)
				if selected == "" || score.Cmp(best) < 0 || (score.Cmp(best) == 0 && earlier) {
					selected = id
					host = placement
					best = score
					bestOldest = oldest
				}
			}
			if selected == "" {
				break
			}
			p := s.Pools[selected]
			// Pool/repository selection above is unchanged. Among fitting hosts
			// for that selected pool, avoid hosts that are the sole current fit
			// for demand-bearing pool candidates. These are not distinct job counts;
			// candidate pools may overlap. Never move or discount existing charges.
			// This bounded tie-break is not a maximum-packing guarantee.
			for _, candidate := range p.Hosts {
				if !s.Hosts[candidate].AcceptNew || !groupFits(selected, candidate, p.Size) || !fits(hosts[candidate], p.Size, s.Hosts[candidate].Budget) {
					continue
				}
				if soleHostDemand[candidate] < soleHostDemand[host] || (soleHostDemand[candidate] == soleHostDemand[host] && candidate < host) {
					host = candidate
				}
			}
			if s.Groups != nil {
				if id := s.Groups.ByHost[host]; id != "" {
					plannedByGroup[id], _ = add(plannedByGroup[id], p.Size)
				}
			}
			global, _ = add(global, p.Size)
			hosts[host], _ = add(hosts[host], p.Size)
			repos[p.Repository], _ = add(repos[p.Repository], p.Size)
			pools[selected]++
			poolResources[selected], _ = add(poolResources[selected], p.Size)
			jobID := ""
			if phase == 0 && jobs != nil {
				jobID = jobs.consume(selected, queued, s.Pools)
			} else {
				demand[selected]--
			}
			out.Assignments = append(out.Assignments, Assignment{Pool: selected, Host: host, JobID: jobID, Warm: phase == 1, Resources: p.Size})
		}
	}
	for id, n := range queued {
		out.RemainingQueued[id] = n
	}
	return out, nil
}
