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
	rows   []QueuedDemand
	done   map[string]bool
	byPool map[string][]int
	head   map[string]int
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
	q := &jobQueue{done: map[string]bool{}, byPool: map[string][]int{}, head: map[string]int{}}
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
func (q *jobQueue) first(pool string) *QueuedDemand {
	list := q.byPool[pool]
	for q.head[pool] < len(list) {
		j := &q.rows[list[q.head[pool]]]
		if !q.done[j.ID] {
			return j
		}
		q.head[pool]++
	}
	return nil
}
func (q *jobQueue) consume(pool string, counts map[string]int64, pools map[string]Pool) string {
	j := q.first(pool)
	if j == nil {
		return ""
	}
	q.done[j.ID] = true
	for _, id := range j.Candidates {
		counts[id]--
		p := pools[id]
		p.OldestWaiting = time.Time{}
		if next := q.first(id); next != nil {
			p.OldestWaiting = next.CreatedAt
		}
		pools[id] = p
	}
	return j.ID
}
