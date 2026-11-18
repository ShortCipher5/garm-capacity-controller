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
	RemainingJobs []QueuedDemand
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
	if len(jobs) > 1000 || len(s.Pools) > 1000 || len(s.Charges) > 100000 {
		return JobPlan{}, ErrSnapshot
	}
	copyPools := make(map[string]Pool, len(s.Pools))
	idle := map[string]int{}
	for id, p := range s.Pools {
		if p.Waiting != 0 || !p.OldestWaiting.IsZero() || p.AvailableIdle < 0 || p.AvailableIdle > 1000 {
			return JobPlan{}, ErrSnapshot
		}
		p.Hosts = append([]string(nil), p.Hosts...)
		copyPools[id] = p
		idle[id] = int(p.AvailableIdle)
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
		edges += len(j.Candidates)
		if edges > 100000 {
			return JobPlan{}, ErrSnapshot
		}
		j.Candidates = append([]string(nil), j.Candidates...)
		sort.Strings(j.Candidates)
		q.rows = append(q.rows, j)
		matchJobs = append(matchJobs, DemandJob{ID: j.ID, Candidates: j.Candidates})
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
	match, e := MatchDemand(matchJobs, idle)
	if e != nil {
		return JobPlan{}, e
	}
	for id, pool := range match.Covered {
		q.done[id] = true
		p := s.Pools[pool]
		p.AvailableIdle-- // Planned job use consumes spare supply, not its charge.
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
	return JobPlan{Plan: plan, CoveredByIdle: match.Covered, RemainingJobs: remaining}, nil
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
