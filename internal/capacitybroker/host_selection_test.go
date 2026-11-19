package capacitybroker

import (
	"reflect"
	"testing"
	"time"
)

func TestHostChoicePreservesSoleHostForConstrainedDemand(t *testing.T) {
	s, o := fixture()
	size := Resources{1000, 1000, 1000, 1}
	s.Global = Resources{2000, 2000, 2000, 2}
	s.Hosts = map[string]Host{"h1": {Budget: size, AcceptNew: true}, "h2": {Budget: size, AcceptNew: true}}
	a := s.Pools["a"]
	a.Hosts = []string{"h1", "h2"}
	a.Waiting = 1
	s.Pools["a"] = a
	b := s.Pools["b"]
	b.Hosts = []string{"h1"}
	b.Waiting = 1
	s.Pools["b"] = b
	plan, err := Build(s, o)
	if err != nil || len(plan.Assignments) != 2 || plan.Assignments[0].Pool != "a" || plan.Assignments[0].Host != "h2" || plan.Assignments[1].Pool != "b" || plan.Assignments[1].Host != "h1" {
		t.Fatalf("avoidable host fragmentation: %+v %v", plan, err)
	}
}

func hostChoiceFixture() (Snapshot, Options) {
	s, o := fixture()
	size := Resources{1000, 1000, 1000, 1}
	s.Global = Resources{2000, 2000, 2000, 2}
	s.Hosts = map[string]Host{"h1": {Budget: size, AcceptNew: true}, "h2": {Budget: size, AcceptNew: true}}
	a := s.Pools["a"]
	a.Hosts = []string{"h2", "h1"}
	a.Waiting = 1
	s.Pools["a"] = a
	b := s.Pools["b"]
	b.Hosts = []string{"h1"}
	b.Waiting = 1
	s.Pools["b"] = b
	return s, o
}
func TestHostChoiceAppliesToRealJobMatchingAndWarmPhase(t *testing.T) {
	s, o := hostChoiceFixture()
	for id, p := range s.Pools {
		p.Waiting = 0
		p.OldestWaiting = time.Time{}
		s.Pools[id] = p
	}
	jobs := []QueuedDemand{{ID: "older", Candidates: []string{"a"}, CreatedAt: o.Now.Add(-time.Minute)}, {ID: "newer", Candidates: []string{"b"}, CreatedAt: o.Now.Add(-time.Second)}}
	plan, err := BuildForJobs(s, o, jobs)
	if err != nil || len(plan.Assignments) != 2 || len(plan.RemainingJobs) != 0 || plan.Assignments[0].JobID != "older" || plan.Assignments[0].Host != "h2" {
		t.Fatalf("job path stranded %+v %v", plan, err)
	}
	for id, p := range s.Pools {
		p.WarmIdle = 1
		s.Pools[id] = p
	}
	warm, err := Build(s, o)
	if err != nil || len(warm.Assignments) != 2 || !warm.Assignments[0].Warm || warm.Assignments[0].Host != "h2" {
		t.Fatalf("warm phase stranded %+v %v", warm, err)
	}
}
func TestHostChoiceIgnoresAbsentOrCappedDemand(t *testing.T) {
	for _, mode := range []string{"no-demand", "pool-cap", "repo-cap", "pool-budget"} {
		t.Run(mode, func(t *testing.T) {
			s, o := hostChoiceFixture()
			b := s.Pools["b"]
			switch mode {
			case "no-demand":
				b.Waiting = 0
			case "pool-cap":
				b.MaxRunners = 0
			case "repo-cap":
				s.Repositories["b"] = Repository{}
			case "pool-budget":
				zero := Resources{}
				b.Budget = &zero
			}
			s.Pools["b"] = b
			plan, err := Build(s, o)
			if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != "a" || plan.Assignments[0].Host != "h1" {
				t.Fatalf("ineligible demand changed host %+v %v", plan, err)
			}
		})
	}
}
func TestHostChoiceCannotBypassAlternateResourcesOrUnknownCharges(t *testing.T) {
	for dimension := 0; dimension < 4; dimension++ {
		s, o := hostChoiceFixture()
		h := s.Hosts["h2"]
		h.Budget[dimension] = 0
		s.Hosts["h2"] = h
		plan, err := Build(s, o)
		if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Host != "h1" || plan.RemainingQueued["b"] != 1 {
			t.Fatalf("dimension %d bypass %+v %v", dimension, plan, err)
		}
	}
	s, o := hostChoiceFixture()
	s.Charges = []Charge{{ID: "unknown", Host: "h2", Pool: "a", Resources: s.Hosts["h2"].Budget}}
	before := append([]Charge(nil), s.Charges...)
	plan, err := Build(s, o)
	if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != "b" || plan.Assignments[0].Host != "h1" || !reflect.DeepEqual(s.Charges, before) {
		t.Fatalf("charge/fairness changed %+v %v", plan, err)
	}
}
func TestHostChoiceKeepsSelectionBoundAndDeterministic(t *testing.T) {
	s, o := hostChoiceFixture()
	o.MaxAssignments = 1
	a := s.Pools["a"]
	for _, hosts := range [][]string{{"h2", "h1"}, {"h1", "h2"}} {
		a.Hosts = hosts
		s.Pools["a"] = a
		plan, err := Build(s, o)
		if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != "a" || plan.Assignments[0].Host != "h2" {
			t.Fatalf("order/bound changed %+v %v", plan, err)
		}
	}
}

func TestHostChoiceOverlappingCandidatePoolsRemainOneJob(t *testing.T) {
	s, o := hostChoiceFixture()
	for id, p := range s.Pools {
		p.Waiting = 0
		p.OldestWaiting = time.Time{}
		s.Pools[id] = p
	}
	// Both pools refer to the same queued job. The heuristic counts feasible pool
	// candidates, not distinct jobs: it may prefer h2 even though no second job
	// needs h1. This must never create duplicate demand or an extra assignment.
	plan, err := BuildForJobs(s, o, []QueuedDemand{{ID: "shared", Candidates: []string{"a", "b"}, CreatedAt: o.Now.Add(-time.Minute)}})
	if err != nil || len(plan.Assignments) != 1 || len(plan.RemainingJobs) != 0 || plan.Assignments[0].JobID != "shared" || plan.Assignments[0].Pool != "a" || plan.Assignments[0].Host != "h2" {
		t.Fatalf("candidate overlap changed demand %+v %v", plan, err)
	}
}
func TestHostChoiceRejectsDuplicateHostEdgesBeforeCountingFits(t *testing.T) {
	s, o := hostChoiceFixture()
	p := s.Pools["a"]
	p.Hosts = []string{"h1", "h1"}
	s.Pools["a"] = p
	if _, err := Build(s, o); err != ErrSnapshot {
		t.Fatal("duplicate host edges accepted", err)
	}
	p.Waiting = 0
	p.OldestWaiting = time.Time{}
	s.Pools["a"] = p
	b := s.Pools["b"]
	b.Waiting = 0
	b.OldestWaiting = time.Time{}
	s.Pools["b"] = b
	if _, err := BuildForJobs(s, o, []QueuedDemand{{ID: "job", Candidates: []string{"a"}, CreatedAt: o.Now}}); err != ErrSnapshot {
		t.Fatal("job planner accepted duplicate host edges", err)
	}
}
