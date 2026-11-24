package brokerinput

import (
	"reflect"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

func TestAllocatorExplanationRequiresAwardsToFitSharedBudgets(t *testing.T) {
	in, state := allocatorFixture(t)
	size := in.Snapshot.Pools[pool].Size
	allocatorEnrollPool(&in, &state, allocatorRepoB, allocatorPoolB, "fingerprint-b", 1, size, 3, 3, 0)
	allocatorSetBudgets(&state, 3*size[0], 3*size[1], 3*size[2], 3)
	allocatorQueueJob(&in, allocatorRepoB, allocatorPoolB, 2, in.Options.Now)
	allocatorRebuild(t, state, nil, &in)
	// A retained non-pool charge uses the first slot on the shared host. A
	// genuine plan uses exactly the two remaining slots across two repos.
	in.Snapshot.Charges = append(in.Snapshot.Charges, capacitybroker.Charge{ID: "retained-fixture", Host: "host", Resources: size})
	plan := planFor(t, in)
	if len(plan.Assignments) != 2 || plan.Assignments[0].Pool == plan.Assignments[1].Pool {
		t.Fatalf("expected two awards against the shared budget, got %+v", plan.Assignments)
	}
	expires := in.Observation.StartedAt.Add(in.Options.MaxAge)
	valid, err := ExplainAllocatorPlan(in, plan, expires)
	if err != nil || len(valid.Awards) != 2 || valid.Held.Global != heldResources(size) {
		t.Fatalf("valid exact fit or retained charge lost: %+v, %v", valid, err)
	}

	for _, tc := range []struct {
		name   string
		change func(*AllocatorInput, *capacitybroker.JobPlan)
	}{
		{"changed CPU in profile", func(_ *AllocatorInput, p *capacitybroker.JobPlan) { p.Assignments[0].Resources[0]-- }},
		{"changed runner count in profile", func(_ *AllocatorInput, p *capacitybroker.JobPlan) { p.Assignments[0].Resources[3] = 0 }},
		{"changed job and warm shape", func(_ *AllocatorInput, p *capacitybroker.JobPlan) { p.Assignments[0].Warm = true }},
		{"cross-repository global slot surplus", func(in *AllocatorInput, _ *capacitybroker.JobPlan) { in.Snapshot.Global[3]-- }},
		{"cross-repository host disk surplus", func(in *AllocatorInput, _ *capacitybroker.JobPlan) {
			h := in.Snapshot.Hosts["host"]
			h.Budget[2]--
			in.Snapshot.Hosts["host"] = h
		}},
		{"pool RAM ceiling", func(in *AllocatorInput, plan *capacitybroker.JobPlan) {
			id := plan.Assignments[0].Pool
			p := in.Snapshot.Pools[id]
			budget := *p.Budget
			budget[1] = p.Size[1] - 1
			p.Budget = &budget
			in.Snapshot.Pools[id] = p
		}},
		{"paused host", func(in *AllocatorInput, _ *capacitybroker.JobPlan) {
			h := in.Snapshot.Hosts["host"]
			h.AcceptNew = false
			in.Snapshot.Hosts["host"] = h
		}},
		{"host outside pool", func(in *AllocatorInput, plan *capacitybroker.JobPlan) {
			in.Snapshot.Hosts["other-host"] = in.Snapshot.Hosts["host"]
			plan.Assignments[0].Host = "other-host"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modeled, sample := in, plan
			modeled.Snapshot.Hosts = map[string]capacitybroker.Host{"host": in.Snapshot.Hosts["host"]}
			modeled.Snapshot.Pools = map[string]capacitybroker.Pool{pool: in.Snapshot.Pools[pool], allocatorPoolB: in.Snapshot.Pools[allocatorPoolB]}
			sample.Assignments = append([]capacitybroker.Assignment(nil), plan.Assignments...)
			tc.change(&modeled, &sample)
			got, err := ExplainAllocatorPlan(modeled, sample, expires)
			if err != capacitybroker.ErrSnapshot || !reflect.DeepEqual(got, AllocatorProposal{}) {
				t.Fatalf("invalid award produced an explanation (including a partial prefix): %+v, %v", got, err)
			}
		})
	}
}

func TestAllocatorExplanationPreservesOverbudgetDebtWithoutAwards(t *testing.T) {
	in, _ := allocatorFixture(t)
	held := in.Snapshot.Global
	held[0]++
	in.Snapshot.Charges = append(in.Snapshot.Charges, capacitybroker.Charge{ID: "retained-fixture", Host: "host", Resources: held})
	plan := planFor(t, in)
	if len(plan.Assignments) != 0 || len(plan.RemainingJobs) != 1 {
		t.Fatalf("expected only retained debt and unmet demand: %+v", plan)
	}
	got, err := ExplainAllocatorPlan(in, plan, in.Observation.StartedAt.Add(in.Options.MaxAge))
	if err != nil || len(got.Awards) != 0 || got.Held.Global != heldResources(held) ||
		allocatorDecision(got, pool).Reason != allocatorDecisionBlockedGlobal {
		t.Fatalf("retained overbudget debt or reason lost: %+v, %v", got, err)
	}
}
