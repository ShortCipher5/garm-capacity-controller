package capacitybroker

import (
	"reflect"
	"testing"
)

func TestIdleOnlyEdgesDoNotBecomeGrantOrCreationEdges(t *testing.T) {
	s, o := jobsFixture()
	p := s.Pools["a"]
	p.MaxRunners = 0
	p.WarmIdle = 0
	p.AvailableIdle = 1
	p.PendingWarm = 1
	p.OutstandingGrants = 1
	s.Pools["a"] = p
	s.Charges = []Charge{{ID: "both", Host: "h", Pool: "a", Resources: Resources{2000, 2000, 2000, 2}}}
	jobs := []QueuedDemand{{ID: "one", Candidates: []string{"b"}, CreatedAt: o.Now}, {ID: "two", CreatedAt: o.Now}}
	before := append([]Charge(nil), s.Charges...)
	plan, err := BuildForJobsWithIdleOnly(s, o, jobs, map[string][]string{"one": {"a"}, "two": {"a"}})
	if err != nil || len(plan.CoveredByIdle) != 1 || len(plan.CoveredByGrant) != 0 || len(plan.DeferredByInflight) != 0 {
		t.Fatalf("bad idle-only coverage %+v %v", plan, err)
	}
	for _, a := range plan.Assignments {
		if a.Pool == "a" {
			t.Fatal("disabled creation")
		}
	}
	if !reflect.DeepEqual(s.Charges, before) {
		t.Fatal("charge changed")
	}
}
func TestIdleOnlyRejectsMixedUnknownAndDuplicateEdges(t *testing.T) {
	s, o := jobsFixture()
	for _, edges := range []map[string][]string{{"job": {"a"}}, {"unknown": {"b"}}, {"job": {"missing"}}, {"job": {"b", "b"}}} {
		p := s.Pools["b"]
		p.MaxRunners = 0
		p.WarmIdle = 0
		s.Pools["b"] = p
		if _, err := BuildForJobsWithIdleOnly(s, o, []QueuedDemand{{ID: "job", Candidates: []string{"a"}, CreatedAt: o.Now}}, edges); err == nil {
			t.Fatal("bad edges accepted", edges)
		}
	}
}
