package capacitybroker

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func jobsFixture() (Snapshot, Options) {
	s, o := fixture()
	for id, p := range s.Pools {
		p.Waiting = 0
		p.OldestWaiting = time.Time{}
		s.Pools[id] = p
	}
	return s, o
}
func TestJobPlanDoesNotMultiplyFlexibleDemand(t *testing.T) {
	s, o := jobsFixture()
	jobs := []QueuedDemand{{"j", []string{"a", "b"}, o.Now.Add(-time.Minute)}}
	p, e := BuildForJobs(s, o, jobs)
	if e != nil || len(p.Assignments) != 1 || p.Assignments[0].JobID != "j" || len(p.RemainingJobs) != 0 || p.RemainingQueued != nil {
		t.Fatalf("%+v %v", p, e)
	}
	if s.Pools["a"].Waiting != 0 || jobs[0].Candidates[0] != "a" {
		t.Fatal("mutated input")
	}
}
func TestJobPlanFitsAlternatePoolInsteadOfPrematureAssignment(t *testing.T) {
	s, o := jobsFixture()
	p := s.Pools["a"]
	p.Size = Resources{10000, 1000, 1000, 1}
	s.Pools["a"] = p
	plan, e := BuildForJobs(s, o, []QueuedDemand{{"j", []string{"a", "b"}, o.Now}})
	if e != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != "b" {
		t.Fatalf("%+v %v", plan, e)
	}
}
func TestJobPlanMatchesIdleOnlyOnce(t *testing.T) {
	s, o := jobsFixture()
	p := s.Pools["a"]
	p.AvailableIdle = 1
	s.Pools["a"] = p
	s.Charges = []Charge{{ID: "idle", Host: "h", Pool: "a", Resources: Resources{1000, 1000, 1000, 1}}}
	plan, e := BuildForJobs(s, o, []QueuedDemand{{"j1", []string{"a", "b"}, o.Now}, {"j2", []string{"a", "b"}, o.Now}})
	if e != nil || len(plan.Assignments) != 1 || len(plan.CoveredByIdle) != 1 {
		t.Fatalf("%+v %v", plan, e)
	}
}
func TestJobPlanPreservesUnmatchedAndPrioritizesQueue(t *testing.T) {
	s, o := jobsFixture()
	s.Global = Resources{1000, 1000, 1000, 1}
	for id, p := range s.Pools {
		p.WarmIdle = 2
		s.Pools[id] = p
	}
	jobs := []QueuedDemand{{"new", []string{"a"}, o.Now}, {"old", []string{"b"}, o.Now.Add(-time.Hour)}, {"none", nil, o.Now}}
	plan, e := BuildForJobs(s, o, jobs)
	if e != nil || len(plan.Assignments) != 1 || plan.Assignments[0].JobID != "old" || len(plan.RemainingJobs) != 2 {
		t.Fatalf("%+v %v", plan, e)
	}
	reverse := []QueuedDemand{jobs[2], jobs[1], jobs[0]}
	other, e := BuildForJobs(s, o, reverse)
	if e != nil || !reflect.DeepEqual(plan, other) {
		t.Fatalf("unstable %+v %v", other, e)
	}
}
func TestJobPlanRejectsInvalidAndUnbackedIdle(t *testing.T) {
	s, o := jobsFixture()
	for _, jobs := range [][]QueuedDemand{{{"x", []string{"a", "a"}, o.Now}}, {{"x", []string{"missing"}, o.Now}}, {{"x", nil, o.Now.Add(time.Second)}}, {{"x", nil, o.Now}, {"x", nil, o.Now}}} {
		if _, e := BuildForJobs(s, o, jobs); e == nil {
			t.Fatal("accepted invalid jobs")
		}
	}
	p := s.Pools["a"]
	p.AvailableIdle = 1
	s.Pools["a"] = p
	if _, e := BuildForJobs(s, o, nil); e == nil {
		t.Fatal("accepted idle not in charges")
	}
}

func TestJobPlanReplenishesIdleConsumedByQueue(t *testing.T) {
	s, o := jobsFixture()
	p := s.Pools["a"]
	p.AvailableIdle = 1
	p.WarmIdle = 1
	s.Pools["a"] = p
	s.Charges = []Charge{{ID: "idle", Host: "h", Pool: "a", Resources: p.Size}}
	plan, e := BuildForJobs(s, o, []QueuedDemand{{"job", []string{"a"}, o.Now}})
	if e != nil || len(plan.CoveredByIdle) != 1 || len(plan.Assignments) != 1 || !plan.Assignments[0].Warm || plan.Assignments[0].JobID != "" {
		t.Fatalf("%+v %v", plan, e)
	}
	if s.Pools["a"].AvailableIdle != 1 || len(s.Charges) != 1 {
		t.Fatal("mutated historical charge/idle input")
	}
}
func TestJobPlanCannotHideUnchargedIdleByMatching(t *testing.T) {
	s, o := jobsFixture()
	p := s.Pools["a"]
	p.AvailableIdle = 1
	s.Pools["a"] = p
	if _, e := BuildForJobs(s, o, []QueuedDemand{{"job", []string{"a"}, o.Now}}); e == nil {
		t.Fatal("matched uncharged idle")
	}
}

func TestJobPlanAugmentsFlexibleJobWithoutMovingCapacity(t *testing.T) {
	s, o := jobsFixture()
	for id, p := range s.Pools {
		p.MaxRunners = 1
		s.Pools[id] = p
	}
	jobs := []QueuedDemand{{"flexible", []string{"a", "b"}, o.Now.Add(-time.Hour)}, {"constrained", []string{"a"}, o.Now}}
	plan, e := BuildForJobs(s, o, jobs)
	if e != nil || len(plan.Assignments) != 2 || len(plan.RemainingJobs) != 0 {
		t.Fatalf("%+v %v", plan, e)
	}
	got := map[string]string{}
	for _, a := range plan.Assignments {
		got[a.Pool] = a.JobID
		if a.Host != "h" || a.Resources != s.Pools[a.Pool].Size {
			t.Fatal("placement changed")
		}
	}
	if got["a"] != "constrained" || got["b"] != "flexible" {
		t.Fatalf("%v", got)
	}
}

func TestJobPlanAugmentationMatchesExhaustiveUnitSlotOptimum(t *testing.T) {
	// Exhaust every eligibility graph for three jobs and three independent unit
	// slots. This proves the concrete flexible/constrained family without claiming
	// optimal heterogeneous resource packing.
	for graph := 0; graph < 512; graph++ {
		s, o := jobsFixture()
		s.Global = Resources{3000, 3000, 3000, 3}
		s.Hosts["h"] = Host{Budget: s.Global, AcceptNew: true}
		p := s.Pools["a"]
		p.MaxRunners = 1
		s.Pools = map[string]Pool{"a": p, "b": p, "c": p}
		r := s.Repositories[p.Repository]
		r.MaxRunners = 3
		s.Repositories[p.Repository] = r
		jobs := []QueuedDemand{}
		demands := []DemandJob{}
		for j := 0; j < 3; j++ {
			candidates := []string{}
			for k, pool := range []string{"a", "b", "c"} {
				if graph&(1<<(j*3+k)) != 0 {
					candidates = append(candidates, pool)
				}
			}
			id := string(rune('x' + j))
			jobs = append(jobs, QueuedDemand{id, candidates, o.Now.Add(time.Duration(j-3) * time.Minute)})
			demands = append(demands, DemandJob{id, candidates})
		}
		want, e := MatchDemand(demands, map[string]int{"a": 1, "b": 1, "c": 1})
		if e != nil {
			t.Fatal(e)
		}
		got, e := BuildForJobs(s, o, jobs)
		if e != nil || len(got.Assignments) != len(want.Covered) {
			t.Fatalf("graph %d: %+v want %v err %v", graph, got, want, e)
		}
		seen := map[string]bool{}
		for _, a := range got.Assignments {
			if seen[a.JobID] {
				t.Fatal("duplicate job")
			}
			seen[a.JobID] = true
			eligible := false
			for _, j := range jobs {
				if j.ID == a.JobID {
					for _, p := range j.Candidates {
						eligible = eligible || p == a.Pool
					}
				}
			}
			if !eligible {
				t.Fatal("ineligible correlation")
			}
		}
	}
}

func TestJobPlanAugmentationWorkBudgetFailsClosed(t *testing.T) {
	q := &jobQueue{rows: []QueuedDemand{{ID: "j"}}, byPool: map[string][]int{"a": {0}}, done: map[string]bool{}, assigned: map[string]string{}, paths: map[string][]jobStep{}, operations: 1000000}
	if path := q.path("a"); path != nil || !q.exhausted || len(q.assigned) != 0 {
		t.Fatal("exhaustion did not fail closed")
	}
}

func TestJobPlanDenseHundredPoolsFitsWorkBudget(t *testing.T) {
	s, o := jobsFixture()
	s.Global = Resources{100000, 100000, 100000, 100}
	s.Hosts["h"] = Host{Budget: s.Global, AcceptNew: true}
	p := s.Pools["a"]
	p.MaxRunners = 1
	r := s.Repositories[p.Repository]
	r.MaxRunners = 100
	s.Repositories[p.Repository] = r
	s.Pools = map[string]Pool{}
	candidates := []string{}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("p%03d", i)
		s.Pools[id] = p
		candidates = append(candidates, id)
	}
	jobs := []QueuedDemand{}
	for i := 0; i < 100; i++ {
		jobs = append(jobs, QueuedDemand{fmt.Sprintf("j%03d", i), candidates, o.Now})
	}
	plan, e := BuildForJobs(s, o, jobs)
	if e != nil || len(plan.Assignments) != 100 {
		t.Fatalf("%d %v", len(plan.Assignments), e)
	}
}

func TestJobPlanAugmentationCannotBypassHostCapacity(t *testing.T) {
	s, o := jobsFixture()
	for id, p := range s.Pools {
		p.MaxRunners = 1
		s.Pools[id] = p
	}
	p := s.Pools["b"]
	p.Hosts = []string{"blocked"}
	s.Pools["b"] = p
	s.Hosts["blocked"] = Host{Budget: p.Size, AcceptNew: false}
	jobs := []QueuedDemand{{"flexible", []string{"a", "b"}, o.Now.Add(-time.Hour)}, {"constrained", []string{"a"}, o.Now}}
	plan, e := BuildForJobs(s, o, jobs)
	if e != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != "a" || plan.Assignments[0].JobID != "flexible" {
		t.Fatalf("%+v %v", plan, e)
	}
}
