package capacitybroker

import (
	"fmt"
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

func TestHostChoicePermutationKeepsExactPlanAndInputs(t *testing.T) {
	s, o := hostChoiceFixture()
	s.Global = Resources{3000, 3000, 3000, 3}
	s.Hosts["h3"] = s.Hosts["h2"]
	a := s.Pools["a"]
	permutations := [][]string{
		{"h1", "h2", "h3"}, {"h1", "h3", "h2"}, {"h2", "h1", "h3"},
		{"h2", "h3", "h1"}, {"h3", "h1", "h2"}, {"h3", "h2", "h1"},
	}
	for _, warm := range []bool{false, true} {
		if warm {
			a.Waiting, a.WarmIdle = 0, 1
			b := s.Pools["b"]
			b.Waiting, b.WarmIdle = 0, 1
			s.Pools["b"] = b
		}
		for _, order := range permutations {
			a.Hosts = append([]string(nil), order...)
			s.Pools["a"] = a
			p, err := Build(s, o)
			if err != nil || len(p.Assignments) != 2 ||
				p.Assignments[0].Pool != "a" || p.Assignments[0].Host != "h2" ||
				p.Assignments[1].Pool != "b" || p.Assignments[1].Host != "h1" ||
				p.Assignments[0].Warm != warm || p.Assignments[1].Warm != warm {
				t.Fatalf("order=%v warm=%v: changed plan %+v, %v", order, warm, p, err)
			}
			if !reflect.DeepEqual(s.Pools["a"].Hosts, order) {
				t.Fatalf("order=%v warm=%v: input host slice modified: %v", order, warm, s.Pools["a"].Hosts)
			}
		}
	}
}

func BenchmarkCrossRepoHostCandidateScan(b *testing.B) {
	now := time.Now()
	poolCount, hostCount := 64, 128
	s := Snapshot{Revision: "bench", ObservedAt: now, Complete: true, Global: Resources{10000, 10000, 10000, 10000}, Hosts: map[string]Host{}, Repositories: map[string]Repository{}, Pools: map[string]Pool{}}
	ids := make([]string, hostCount)
	for i := range ids {
		ids[i] = fmt.Sprintf("h%03d", hostCount-i)
		s.Hosts[ids[i]] = Host{Budget: Resources{200, 200, 200, 200}, AcceptNew: true}
	}
	for i := 0; i < poolCount; i++ {
		k := fmt.Sprintf("p%03d", i)
		s.Repositories[k] = Repository{MaxRunners: 10, Weight: uint32(i%7 + 1)}
		s.Pools[k] = Pool{Hosts: ids, Repository: k, Size: Resources{1, 1, 1, 1}, Waiting: 10, MaxRunners: 10, OldestWaiting: now}
	}
	for i := 0; i < b.N; i++ {
		p, err := Build(s, Options{Now: now, MaxAge: time.Minute, MaxAssignments: 128})
		if err != nil || len(p.Assignments) != 128 {
			b.Fatal(err, len(p.Assignments))
		}
	}
}

func TestNamedGroupPlannerUsesRemainingCapacityOnce(t *testing.T) {
	now := time.Now().UTC()
	s := Snapshot{Global: Resources{10, 10, 10, 10}, Revision: "groups", ObservedAt: now, Complete: true, Hosts: map[string]Host{}, Repositories: map[string]Repository{"r": {MaxRunners: 10}}, Pools: map[string]Pool{}}
	for _, h := range []string{"a", "b", "x"} {
		s.Hosts[h] = Host{Budget: s.Global, AcceptNew: true}
	}
	s.Pools["p"] = Pool{Repository: "r", Size: Resources{1, 1, 1, 1}, Hosts: []string{"x", "a", "b"}, MaxRunners: 9, Waiting: 4, OldestWaiting: now}
	s.Groups = &GroupHeadroom{ByHost: map[string]string{"a": "g", "b": "g"}, ByPool: map[string]string{"p": "g"}, Remaining: map[string]Resources{"g": {2, 2, 10, 2}}}
	s.Charges = []Charge{{ID: "old", Host: "a", Pool: "p", Resources: Resources{2, 2, 2, 2}}}
	p, e := Build(s, Options{Now: now, MaxAge: time.Minute, MaxAssignments: 10})
	if e != nil || len(p.Assignments) != 2 {
		t.Fatalf("unexpected group headroom: %+v %v", p, e)
	}
	for _, a := range p.Assignments {
		if a.Host == "x" {
			t.Fatal("awarded ungrouped host")
		}
	}
}
