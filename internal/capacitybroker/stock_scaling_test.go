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

func TestStockScalingMixedSizesConserveCPUAndRAMBeforeSlots(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		budget, large, small Resources
	}{
		{"CPU", Resources{4000, 8000, 8000, 6}, Resources{3000, 1000, 1000, 1}, Resources{1000, 2000, 1000, 1}},
		{"RAM", Resources{8000, 4000, 8000, 6}, Resources{1000, 3000, 1000, 1}, Resources{2000, 1000, 1000, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, o := jobsFixture()
			s.Global = tc.budget
			// The host independently has enough capacity for a large runner;
			// it is the shared CPU or RAM budget that constrains this comparison.
			s.Hosts["h"] = Host{Budget: Resources{10000, 10000, 10000, 6}, AcceptNew: true}
			a, b := s.Pools["a"], s.Pools["b"]
			a.Size, b.Size = tc.large, tc.small
			s.Pools["a"], s.Pools["b"] = a, b
			s.Charges = []Charge{{ID: "modeled-existing", Host: "h", Pool: "a", Resources: tc.large}}
			prior := append([]Charge(nil), s.Charges...)
			jobs := []QueuedDemand{
				{"large-older", []string{"a"}, o.Now.Add(-time.Minute)},
				{"small-fits", []string{"b"}, o.Now.Add(-time.Second)},
				{"small-after-exhaustion", []string{"b"}, o.Now},
			}
			plan, err := BuildForStockScaling(s, o, jobs, nil)
			if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].JobID != "small-fits" || plan.Assignments[0].Resources != tc.small {
				t.Fatalf("wanted one fitting small job, not the blocked older job: %+v %v", plan, err)
			}
			if !reflect.DeepEqual(plan.RemainingJobs, []QueuedDemand{jobs[0], jobs[2]}) {
				t.Fatalf("blocked jobs must stay queued despite spare runner slots: %+v", plan.RemainingJobs)
			}
			if !reflect.DeepEqual(s.Charges, prior) || !reflect.DeepEqual(s.Pools["a"], a) || !reflect.DeepEqual(s.Pools["b"], b) {
				t.Fatal("advisory planning changed charges or runner profiles")
			}
		})
	}
}
