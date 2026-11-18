package capacitybroker

import (
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
