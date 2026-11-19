package capacitybroker

import (
	"reflect"
	"testing"
	"time"
)

func TestStockScalingChronologicalIdleCoverageDoesNotChangeLegacy(t *testing.T) {
	s, o := jobsFixture()
	p := s.Pools["a"]
	p.AvailableIdle = 1
	s.Pools["a"] = p
	s.Charges = []Charge{{ID: "idle", Host: "h", Pool: "a", Resources: p.Size}}
	jobs := []QueuedDemand{
		{"a-younger", []string{"a"}, o.Now},
		{"z-older", []string{"a"}, o.Now.Add(-time.Minute)},
	}
	legacy, err := BuildForJobsWithIdleOnly(s, o, jobs, nil)
	if err != nil || !reflect.DeepEqual(legacy.CoveredByIdle, map[string]string{"a-younger": "a"}) {
		t.Fatalf("legacy selection changed: %+v, %v", legacy, err)
	}
	adv, err := BuildForStockScaling(s, o, jobs, nil)
	if err != nil || !reflect.DeepEqual(adv.CoveredByIdle, map[string]string{"z-older": "a"}) || len(adv.Assignments) != 1 || adv.Assignments[0].JobID != "a-younger" {
		t.Fatalf("advisory age order: %+v, %v", adv, err)
	}
	jobs[1].CreatedAt = jobs[0].CreatedAt
	adv, err = BuildForStockScaling(s, o, []QueuedDemand{jobs[1], jobs[0]}, nil)
	if err != nil || !reflect.DeepEqual(adv.CoveredByIdle, map[string]string{"a-younger": "a"}) {
		t.Fatalf("non-deterministic equal-age order: %+v, %v", adv, err)
	}
}

func TestStockScalingChronologicalMaximumMatchingMovesOnlyCorrelation(t *testing.T) {
	s, o := jobsFixture()
	for _, id := range []string{"a", "b"} {
		p := s.Pools[id]
		p.AvailableIdle = 1
		s.Pools[id] = p
		s.Charges = append(s.Charges, Charge{ID: id + "-idle", Host: "h", Pool: id, Resources: p.Size})
	}
	jobs := []QueuedDemand{
		{"a-constrained", []string{"a"}, o.Now},
		{"z-flexible", []string{"a", "b"}, o.Now.Add(-time.Minute)},
	}
	adv, err := BuildForStockScaling(s, o, jobs, nil)
	if err != nil || !reflect.DeepEqual(adv.CoveredByIdle, map[string]string{"a-constrained": "a", "z-flexible": "b"}) ||
		len(adv.Assignments) != 0 || len(adv.RemainingJobs) != 0 || s.Pools["a"].AvailableIdle != 1 || len(s.Charges) != 2 {
		t.Fatalf("maximum matching or accounting lost: %+v, %v", adv, err)
	}
}

func TestStockScalingChronologicalSupplyClassificationAndIdleFloor(t *testing.T) {
	s, o := jobsFixture()
	s.Global = Resources{3000, 3000, 3000, 3}
	s.Hosts["h"] = Host{Budget: s.Global, AcceptNew: true}
	p := s.Pools["a"]
	p.AvailableIdle, p.PendingWarm, p.OutstandingGrants, p.Inflight, p.WarmIdle = 1, 2, 1, 1, 1
	s.Pools["a"] = p
	s.Charges = []Charge{{ID: "all-supply", Host: "h", Pool: "a", Resources: s.Global}}
	jobs := []QueuedDemand{
		{"a-young", []string{"a"}, o.Now},
		{"z-old", []string{"a"}, o.Now.Add(-2 * time.Minute)},
		{"x-middle", []string{"a"}, o.Now.Add(-time.Minute)},
	}
	adv, err := BuildForStockScaling(s, o, jobs, nil)
	if err != nil || adv.CoveredByIdle["z-old"] != "a" || adv.CoveredByGrant["x-middle"] != "a" ||
		adv.DeferredByInflight["a-young"] != "a" || len(adv.Assignments) != 0 || !reflect.DeepEqual(s.Pools["a"], p) {
		t.Fatalf("changed supply class, accounting or warm floor: %+v, %v", adv, err)
	}
}
