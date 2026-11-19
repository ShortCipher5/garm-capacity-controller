package capacitybroker

import (
	"math"
	"sort"
	"time"
)

// QueuedDemand retains a job's complete eligible pool set until placement, rather
// than arbitrarily choosing a pool or counting the job once for every candidate.
type QueuedDemand struct {
	ID         string
	Candidates []string
	CreatedAt  time.Time
}

type JobPlan struct {
	Plan
	// CoveredByIdle and assignment JobIDs are planning correlations only, never
	// claims that GitHub assigned a particular job or permission to delete a runner.
	CoveredByIdle map[string]string
	// CoveredByGrant is provisional demand coverage by an outstanding pool
	// grant, not proof of a running worker or a grant-to-job binding.
	CoveredByGrant map[string]string
	// DeferredByInflight is not fulfilled demand. Provisioning is withheld
	// pending identity-correlated creating/unknown outcomes, without job binding.
	DeferredByInflight map[string]string
	RemainingJobs      []QueuedDemand
}
type jobQueue struct {
	rows       []QueuedDemand
	done       map[string]bool
	byPool     map[string][]int
	assigned   map[string]string
	paths      map[string][]jobStep
	operations int
	oldest     int
	exhausted  bool
}

// BuildForJobs matches existing idle supply once, then chooses each unmet job's
// pool jointly with fair-share/host fitting. Caller supplies all current charges,
// including creating, unknown and outstanding grants. This does NOT issue grants.
// Pool.Waiting/OldestWaiting are derived here and must be empty in the input.
func BuildForJobs(s Snapshot, opt Options, jobs []QueuedDemand) (JobPlan, error) {
	return buildForJobs(s, opt, jobs, nil, false)
}

// BuildForJobsWithIdleOnly adds advisory existing-idle edges for stopped pools.
// These edges never enter grant/inflight matching or new/warm placement.
func BuildForJobsWithIdleOnly(s Snapshot, opt Options, jobs []QueuedDemand, idleOnly map[string][]string) (JobPlan, error) {
	return buildForJobs(s, opt, jobs, idleOnly, false)
}

// BuildForStockScaling compares demand by age for offline maximum proposals
// only. It preserves charge and supply semantics, and never binds jobs.
func BuildForStockScaling(s Snapshot, opt Options, jobs []QueuedDemand, idleOnly map[string][]string) (JobPlan, error) {
	return buildForJobs(s, opt, jobs, idleOnly, true)
}

func buildForJobs(s Snapshot, opt Options, jobs []QueuedDemand, idleOnly map[string][]string, chronological bool) (JobPlan, error) {
	if len(idleOnly) > 1000 {
		return JobPlan{}, ErrSnapshot
	}
	if len(jobs) > 1000 || len(s.Pools) > 1000 || len(s.Charges) > 100000 {
		return JobPlan{}, ErrSnapshot
	}
	copyPools := make(map[string]Pool, len(s.Pools))
	idle := map[string]int{}
	for id, p := range s.Pools {
		if p.Waiting != 0 || !p.OldestWaiting.IsZero() || p.AvailableIdle < 0 || p.AvailableIdle > 1000 || p.OutstandingGrants < 0 || p.OutstandingGrants > 1000 || p.OutstandingGrants > p.PendingWarm || p.Inflight < 0 || p.Inflight > 1000 || p.Inflight > p.PendingWarm-p.OutstandingGrants || p.OutstandingGrants+p.AvailableIdle+p.Inflight > 1000 {
			return JobPlan{}, ErrSnapshot
		}
		p.Hosts = append([]string(nil), p.Hosts...)
		copyPools[id] = p
		idle[id] = int(p.AvailableIdle + p.OutstandingGrants + p.Inflight)
	}
	// Idle-only pools must be globally disjoint from creation candidates, not
	// merely disjoint for one job. Otherwise pooled grant slots could leak through.
	if len(idleOnly) > 0 {
		normal := map[string]bool{}
		knownJobs := map[string]bool{}
		checkedEdges := 0
		for _, j := range jobs {
			if len(j.Candidates) > 1000 || checkedEdges > 100000-len(j.Candidates) {
				return JobPlan{}, ErrSnapshot
			}
			checkedEdges += len(j.Candidates)
			knownJobs[j.ID] = true
			for _, p := range j.Candidates {
				normal[p] = true
			}
		}
		for id, candidates := range idleOnly {
			if !knownJobs[id] || len(candidates) > 1000 || checkedEdges > 100000-len(candidates) {
				return JobPlan{}, ErrSnapshot
			}
			checkedEdges += len(candidates)
			for _, id := range candidates {
				p, exists := s.Pools[id]
				if !exists || normal[id] || p.MaxRunners != 0 || p.WarmIdle != 0 {
					return JobPlan{}, ErrSnapshot
				}
				idle[id] = int(p.AvailableIdle)
			}
		}
	}
	// Validate idle against original charges before matching reduces spare supply.
	charged := map[string]int64{}
	for _, c := range s.Charges {
		if c.Resources[3] < 0 || c.Resources[3] > math.MaxInt64-charged[c.Pool] {
			return JobPlan{}, ErrSnapshot
		}
		charged[c.Pool] += c.Resources[3]
	}
	for id, p := range copyPools {
		if p.AvailableIdle > charged[id] || p.PendingWarm < 0 || p.PendingWarm > charged[id]-p.AvailableIdle {
			return JobPlan{}, ErrSnapshot
		}
	}
	s.Pools = copyPools
	q := &jobQueue{done: map[string]bool{}, byPool: map[string][]int{}, assigned: map[string]string{}, paths: map[string][]jobStep{}}
	matchJobs := make([]DemandJob, 0, len(jobs))
	edges := 0
	for _, j := range jobs {
		if j.CreatedAt.IsZero() || j.CreatedAt.After(opt.Now) {
			return JobPlan{}, ErrSnapshot
		}
		edges += len(j.Candidates) + len(idleOnly[j.ID])
		if edges > 100000 {
			return JobPlan{}, ErrSnapshot
		}
		j.Candidates = append([]string(nil), j.Candidates...)
		sort.Strings(j.Candidates)
		q.rows = append(q.rows, j)
		existingCandidates := append([]string(nil), j.Candidates...)
		existingCandidates = append(existingCandidates, idleOnly[j.ID]...)
		matchJobs = append(matchJobs, DemandJob{ID: j.ID, Candidates: existingCandidates})
	}
	sort.Slice(q.rows, func(i, j int) bool {
		if q.rows[i].CreatedAt.Equal(q.rows[j].CreatedAt) {
			return q.rows[i].ID < q.rows[j].ID
		}
		return q.rows[i].CreatedAt.Before(q.rows[j].CreatedAt)
	})
	for i, j := range q.rows {
		for _, p := range j.Candidates {
			q.byPool[p] = append(q.byPool[p], i)
		}
	}
	if chronological {
		rank := make(map[string]int, len(q.rows))
		for i, row := range q.rows {
			rank[row.ID] = i
		}
		sort.Slice(matchJobs, func(i, j int) bool { return rank[matchJobs[i].ID] < rank[matchJobs[j].ID] })
	}
	match, e := matchDemand(matchJobs, idle, chronological)
	if e != nil {
		return JobPlan{}, e
	}
	coveredIdle, coveredGrant, deferred := map[string]string{}, map[string]string{}, map[string]string{}
	matchedIDs := make([]string, 0, len(match.Covered))
	if chronological {
		for _, job := range matchJobs {
			if _, matched := match.Covered[job.ID]; matched {
				matchedIDs = append(matchedIDs, job.ID)
			}
		}
	} else {
		for id := range match.Covered {
			matchedIDs = append(matchedIDs, id)
		}
		sort.Strings(matchedIDs)
	}
	for _, id := range matchedIDs {
		pool := match.Covered[id]
		q.done[id] = true
		p := s.Pools[pool]
		if p.AvailableIdle > 0 {
			p.AvailableIdle-- // Planned use consumes spare supply, not its charge.
			coveredIdle[id] = pool
		} else if p.OutstandingGrants > 0 {
			p.OutstandingGrants--
			p.PendingWarm--
			coveredGrant[id] = pool
		} else {
			p.Inflight--
			p.PendingWarm--
			deferred[id] = pool
		}
		s.Pools[pool] = p
	}
	for _, j := range q.rows {
		if q.done[j.ID] {
			continue
		}
		for _, id := range j.Candidates {
			p := s.Pools[id]
			p.Waiting++
			if p.OldestWaiting.IsZero() {
				p.OldestWaiting = j.CreatedAt
			}
			s.Pools[id] = p
		}
	}
	plan, e := build(s, opt, q)
	if e != nil {
		return JobPlan{}, e
	}
	q.correlate(&plan)
	remaining := []QueuedDemand{}
	for _, j := range q.rows {
		if !q.done[j.ID] {
			remaining = append(remaining, j)
		}
	}
	// Per-pool candidate counts overlap. Do not expose them as additive queue size.
	plan.RemainingQueued = nil
	return JobPlan{Plan: plan, CoveredByIdle: coveredIdle, CoveredByGrant: coveredGrant, DeferredByInflight: deferred, RemainingJobs: remaining}, nil
}

// jobStep is an advisory correlation change, never a resource movement.
type jobStep struct {
	pool string
	row  int
}

// path finds the oldest unmet job reachable from a prospective pool slot through
// previously planned slots. Each existing job stays covered. Idle matches remain
// fixed. Work is globally bounded per BuildForJobs invocation.
func (q *jobQueue) path(pool string) []jobStep {
	if path, ok := q.paths[pool]; ok {
		return path
	}
	seen := map[string]bool{pool: true}
	parents := map[string]jobStep{}
	todo := []string{pool}
	best := -1
	for q.oldest < len(q.rows) && q.done[q.rows[q.oldest].ID] {
		q.oldest++
	}
	if q.oldest == len(q.rows) {
		q.paths[pool] = nil
		return nil
	}
	oldest := q.oldest
	end := ""
search:
	for len(todo) > 0 {
		current := todo[0]
		todo = todo[1:]
		for _, row := range q.byPool[current] {
			q.operations++
			if q.operations > 1000000 {
				q.exhausted = true
				return nil
			}
			j := q.rows[row]
			assigned, planned := q.assigned[j.ID]
			if !q.done[j.ID] {
				if best < 0 || row < best {
					best = row
					end = current
				}
				if best == oldest {
					break search
				}
			} else if planned && !seen[assigned] {
				seen[assigned] = true
				parents[assigned] = jobStep{current, row}
				todo = append(todo, assigned)
			}
		}
	}
	var path []jobStep
	if best >= 0 {
		path = append(path, jobStep{end, best})
		for end != pool {
			step := parents[end]
			path = append(path, step)
			end = step.pool
		}
	}
	q.paths[pool] = path
	return path
}
func (q *jobQueue) consume(pool string, _ map[string]int64, _ map[string]Pool) string {
	path := q.path(pool)
	if len(path) == 0 {
		return ""
	}
	for _, step := range path {
		id := q.rows[step.row].ID
		q.assigned[id] = step.pool
		q.done[id] = true
	}
	q.paths = map[string][]jobStep{}
	return q.rows[path[len(path)-1].row].ID
}

// Refresh advisory job IDs after augmentation; physical pool/host assignments
// and their resource accounting never change.
func (q *jobQueue) correlate(plan *Plan) {
	ids := map[string][]string{}
	for _, j := range q.rows {
		if pool, ok := q.assigned[j.ID]; ok {
			ids[pool] = append(ids[pool], j.ID)
		}
	}
	for i := range plan.Assignments {
		a := &plan.Assignments[i]
		if !a.Warm {
			a.JobID = ids[a.Pool][0]
			ids[a.Pool] = ids[a.Pool][1:]
		}
	}
}
