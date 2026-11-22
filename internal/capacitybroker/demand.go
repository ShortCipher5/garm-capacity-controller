package capacitybroker

import "sort"

// DemandJob is one deduplicated queued job in a single source namespace.
// Candidates must contain the complete eligible pool set for the intended scope.
type DemandJob struct {
	ID         string
	Candidates []string
}

// DemandMatch is advisory only. Unmet jobs retain their candidate sets: they
// cannot be copied into every candidate pool's Waiting count. Matching does not
// bind a runner to a job, create a grant, or release charged resources.
type DemandMatch struct {
	Covered map[string]string
	Unmet   []DemandJob
}

// MatchDemand performs bounded maximum-cardinality bipartite matching against
// caller-verified idle slots. Creating, busy, unknown and pending warm resources
// are not idle supply. Callers must not infer a complete/fresh observation from
// this pure function or use its result as admission authority.
func MatchDemand(jobs []DemandJob, idle map[string]int) (DemandMatch, error) {
	return matchDemand(jobs, idle, legacyDemand)
}

type demandMatchMode uint8

const (
	legacyDemand demandMatchMode = iota
	stockDemandByID
	stockDemandChronological
)

// Keep the legacy public and planner behavior. The two stock advisory callers
// can avoid recursive slot reassignment for disjoint single-pool demand.
func matchDemand(jobs []DemandJob, idle map[string]int, mode demandMatchMode) (DemandMatch, error) {
	if len(jobs) > 1000 || len(idle) > 1000 {
		return DemandMatch{}, ErrSnapshot
	}
	ids := make([]string, 0, len(idle))
	slots := 0
	for id, n := range idle {
		if id == "" || len(id) > 256 || n < 0 || n > 1000 || slots+n > 1000 {
			return DemandMatch{}, ErrSnapshot
		}
		slots += n
		ids = append(ids, id)
	}
	ordered := make([]DemandJob, len(jobs))
	seen := map[string]bool{}
	edges := 0
	direct := mode != legacyDemand
	for i, j := range jobs {
		if j.ID == "" || len(j.ID) > 256 || seen[j.ID] || len(j.Candidates) > 1000 {
			return DemandMatch{}, ErrSnapshot
		}
		seen[j.ID] = true
		c := append([]string(nil), j.Candidates...)
		sort.Strings(c)
		direct = direct && len(c) <= 1
		for k, p := range c {
			if _, ok := idle[p]; !ok || (k > 0 && c[k-1] == p) {
				return DemandMatch{}, ErrSnapshot
			}
			edges += idle[p]
			if edges > 100000 {
				return DemandMatch{}, ErrSnapshot
			}
		}
		ordered[i] = DemandJob{ID: j.ID, Candidates: c}
	}
	// Stable ordering avoids dependence on maps/caller order. Eligibility is more
	// important than preference; augmenting paths repair greedy flexible matches.
	if mode != stockDemandChronological {
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	}
	if direct {
		return matchSingleCandidateDemand(ordered, idle), nil
	}
	sort.Strings(ids)
	poolSlots := map[string][]int{}
	slotPool := []string{}
	for _, id := range ids {
		for n := 0; n < idle[id]; n++ {
			poolSlots[id] = append(poolSlots[id], len(slotPool))
			slotPool = append(slotPool, id)
		}
	}
	owners := make([]int, len(slotPool))
	for i := range owners {
		owners[i] = -1
	}
	operations := 0
	exhausted := false
	var visit func(int, []bool) bool
	visit = func(job int, visited []bool) bool {
		for _, p := range ordered[job].Candidates {
			// Empty pools still consume matching work, including recursive visits.
			operations++
			if operations > 1000000 {
				exhausted = true
				return false
			}
			for _, slot := range poolSlots[p] {
				operations++
				if operations > 1000000 {
					exhausted = true
					return false
				}
				if visited[slot] {
					continue
				}
				visited[slot] = true
				if owners[slot] < 0 || visit(owners[slot], visited) {
					owners[slot] = job
					return true
				}
				if exhausted {
					return false
				}
			}
		}
		return false
	}
	for i := range ordered {
		visit(i, make([]bool, len(slotPool)))
		if exhausted {
			return DemandMatch{}, ErrSnapshot
		}
	}
	result := DemandMatch{Covered: map[string]string{}, Unmet: []DemandJob{}}
	for slot, j := range owners {
		if j >= 0 {
			result.Covered[ordered[j].ID] = slotPool[slot]
		}
	}
	for _, j := range ordered {
		if _, ok := result.Covered[j.ID]; !ok {
			result.Unmet = append(result.Unmet, j)
		}
	}
	return result, nil
}

// With at most one candidate per job, pools have independent supply. Walking
// the requested stable priority order yields the same priority-based coverage and
// cardinality without recursively shifting owners between identical slots.
// Input validation, expanded edge and slot caps still run before this path.
func matchSingleCandidateDemand(ordered []DemandJob, idle map[string]int) DemandMatch {
	result := DemandMatch{Covered: map[string]string{}, Unmet: []DemandJob{}}
	used := make(map[string]int)
	for _, job := range ordered {
		if len(job.Candidates) == 1 {
			pool := job.Candidates[0]
			if used[pool] < idle[pool] {
				used[pool]++
				result.Covered[job.ID] = pool
				continue
			}
		}
		result.Unmet = append(result.Unmet, job)
	}
	return result
}
