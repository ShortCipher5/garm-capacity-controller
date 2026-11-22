package capacitybroker

import (
	"errors"
	"fmt"
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

func TestStockScalingLargeSinglePoolQueueCoversOldestWithoutRecursiveWork(t *testing.T) {
	s, o := jobsFixture()
	s.Global = Resources{32000, 32000, 32000, 32}
	s.Hosts["h"] = Host{Budget: s.Global, AcceptNew: true}
	a := s.Pools["a"]
	a.AvailableIdle, a.MaxRunners = 32, 32
	s.Pools["a"] = a
	s.Charges = []Charge{{ID: "existing", Host: "h", Pool: "a", Resources: s.Global}}
	jobs := make([]QueuedDemand, 1000)
	for i := range jobs {
		jobs[i] = QueuedDemand{fmt.Sprintf("job-%04d", i), []string{"a"}, o.Now.Add(-time.Duration(i) * time.Second)}
	}
	got, err := BuildForStockScaling(s, o, jobs, nil)
	if err != nil || len(got.CoveredByIdle) != 32 || len(got.RemainingJobs) != 968 || len(got.Assignments) != 0 {
		t.Fatalf("normal single-pool queue did not fit existing supply: idle=%d waiting=%d assignments=%d err=%v", len(got.CoveredByIdle), len(got.RemainingJobs), len(got.Assignments), err)
	}
	for i, job := range jobs {
		if (got.CoveredByIdle[job.ID] == "a") != (i >= 968) {
			t.Fatalf("wrong age preference for %s", job.ID)
		}
	}
	if !reflect.DeepEqual(s.Pools["a"], a) || len(s.Charges) != 1 {
		t.Fatal("advice changed the original supply or its charge")
	}

	// The unrelated public planner and matcher keep their bounds.
	if _, err := BuildForJobs(s, o, jobs); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("legacy planner path unexpectedly changed: %v", err)
	}
	legacy := make([]DemandJob, len(jobs))
	for i, j := range jobs {
		legacy[i] = DemandJob{j.ID, j.Candidates}
	}
	if _, err := MatchDemand(legacy, map[string]int{"a": 32}); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("legacy path unexpectedly changed: %v", err)
	}
}

func TestStockScalingSingleCandidateKeepsIdleOnlyAndZeroCandidateSemantics(t *testing.T) {
	s, o := jobsFixture()
	s.Global = Resources{3000, 3000, 3000, 3}
	s.Hosts["h"] = Host{Budget: s.Global, AcceptNew: true}
	a, b := s.Pools["a"], s.Pools["b"]
	a.AvailableIdle = 1
	b.MaxRunners, b.WarmIdle = 0, 0
	b.AvailableIdle, b.PendingWarm, b.OutstandingGrants = 1, 1, 1
	s.Pools["a"], s.Pools["b"] = a, b
	s.Charges = []Charge{
		{ID: "normal", Host: "h", Pool: "a", Resources: a.Size},
		{ID: "disabled", Host: "h", Pool: "b", Resources: Resources{2000, 2000, 2000, 2}},
	}
	jobs := []QueuedDemand{
		{"disabled-young", nil, o.Now}, {"normal", []string{"a"}, o.Now},
		{"disabled-old", nil, o.Now.Add(-time.Minute)}, {"none", nil, o.Now},
	}
	idleOnly := map[string][]string{"disabled-young": {"b"}, "disabled-old": {"b"}}
	got, err := BuildForStockScaling(s, o, jobs, idleOnly)
	if err != nil || !reflect.DeepEqual(got.CoveredByIdle, map[string]string{"disabled-old": "b", "normal": "a"}) ||
		len(got.CoveredByGrant) != 0 || len(got.DeferredByInflight) != 0 || len(got.Assignments) != 0 ||
		!reflect.DeepEqual(got.RemainingJobs, []QueuedDemand{jobs[0], jobs[3]}) {
		t.Fatalf("idle-only matching borrowed a grant or lost zero-candidate demand: %+v, %v", got, err)
	}
}

func TestStockDemandStillRefusesDenseAndExpandedEdgeCaps(t *testing.T) {
	for _, mode := range []demandMatchMode{stockDemandByID, stockDemandChronological} {
		jobs := make([]DemandJob, 1000)
		for i := range jobs {
			jobs[i] = DemandJob{fmt.Sprintf("j%04d", i), []string{"a", "b"}}
		}
		// One zero-supply alternate still makes the candidate graph multi-pool.
		// It must use the original bounded augmenting matcher.
		if got, err := matchDemand(jobs, map[string]int{"a": 32, "b": 0}, mode); !errors.Is(err, ErrSnapshot) || got.Covered != nil || got.Unmet != nil {
			t.Fatalf("dense multi-pool work cap bypassed: %+v, %v", got, err)
		}
		for i := range jobs {
			jobs[i].Candidates = []string{"a"}
		}
		if got, err := matchDemand(jobs, map[string]int{"a": 101}, mode); !errors.Is(err, ErrSnapshot) || got.Covered != nil || got.Unmet != nil {
			t.Fatalf("expanded edge bound bypassed for direct matching: %+v, %v", got, err)
		}
	}
}
