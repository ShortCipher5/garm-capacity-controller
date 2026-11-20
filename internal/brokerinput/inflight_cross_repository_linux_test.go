package brokerinput

import (
	"reflect"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestFreshStockPollsDoNotLendMissingUnknownStartupChargeAcrossRepositories(t *testing.T) {
	const repoB = "33333333-3333-4333-8333-333333333333"
	const poolB = "44444444-4444-4444-8444-444444444444"
	// The reservation is already unknown in the model. No operation, timeout
	// transition, reconciliation, or release is performed by this test.
	state, profiles, opt := ledgerFixture()
	profile := profiles[pool]
	profiles[poolB] = LedgerProfile{Repository: repoB, Size: profile.Size}
	state.Config.Global = profile.Size
	state.Config.Pools[poolB] = state.Config.Pools[pool]
	state.Policy.Repositories = append(state.Policy.Repositories, admission.BrokerRepositoryPolicy{
		ID: repoB, Weight: 1, MaxRunners: 2, Pools: []admission.BrokerPoolPolicy{{ID: poolB, MaxRunners: 2}},
	})
	req := admission.Request{Key: "retained", Pool: pool, Host: "host", Resources: profile.Size}
	reservation := admission.Reservation{Request: req, State: "unknown"}
	state.Reservations = map[string]admission.Reservation{req.Key: reservation}
	state.ExecutionPlans = map[string]admission.ExecutionPlan{req.Key: {Request: req, Execution: admission.ExecutionImage{Controller: "controller", Name: "runner-a"}}}
	state.Grants = []admission.BrokerGrant{{State: "consumed", RequestKey: req.Key, Spec: admission.BrokerGrantSpec{Authority: admission.StockProviderAuthority{Controller: "controller", PoolID: pool, ProviderName: "shared"}}}}
	state.Allocated = admission.Allocations{Global: profile.Size, Hosts: map[string]admission.Resources{"host": profile.Size}, Pools: map[string]admission.Resources{pool: profile.Size}}
	charges := []admission.BrokerPlanningCharge{{ID: "reservation:retained", Pool: pool, Host: "host", Resources: profile.Size}}
	base := opt.Now
	for i, visible := range []bool{true, false, false} {
		// Rebuild the planner input each time. The final fresh observation is
		// beyond the first observation's lifetime, not stale stock replay.
		opt.Now = base.Add(time.Duration(i) * 45 * time.Second)
		h := state.Config.Hosts["host"]
		h.ObservedAt = opt.Now
		state.Config.Hosts["host"] = h
		_, _, obs, _ := fixture()
		obs.StartedAt, obs.FinishedAt = opt.Now, opt.Now
		obs.Pools[0].ProviderName = "shared"
		obs.Pools = append(obs.Pools, garmapi.DiscoveredPool{ManagedPool: garmapi.ManagedPool{ID: poolB, RepositoryID: repoB, ProviderName: "shared", Enabled: true}, OwnerKind: "repository", OwnerID: repoB})
		obs.Supply = append(obs.Supply, garmapi.RunnerSupply{PoolID: poolB})
		obs.Jobs = []garmapi.QueuedJob{{WorkflowJobID: 2, RepositoryID: repoB, CandidatePoolIDs: []string{poolB}, CreatedAt: base}}
		if visible {
			obs.Runners = []garmapi.ObservedRunner{{RowID: "stock-a", Name: "runner-a", ProviderName: "shared", PoolID: pool, Kind: "creating"}}
			obs.Supply[0].Creating = 1
		}
		snapshot, err := ledgerSnapshot(state, charges, profiles, opt)
		if err != nil {
			t.Fatal(err)
		}
		before := append([]capacitybroker.Charge(nil), snapshot.Charges...)
		if err := inflight(&snapshot, state, obs); err != nil {
			t.Fatal(err)
		}
		plan, err := stockPlan(snapshot, state.Policy, obs, opt, true, true)
		if err != nil || len(plan.Assignments) != 0 || len(plan.RemainingJobs) != 1 || plan.RemainingJobs[0].ID != repoB+":2" || len(plan.DeferredByInflight) != 0 {
			t.Fatalf("poll %d: unknown A startup must neither fund nor service B: %+v %v", i, plan, err)
		}
		if !reflect.DeepEqual(snapshot.Charges, before) || len(before) != 1 || before[0].Resources[3] != 1 || state.Reservations[req.Key] != reservation {
			t.Fatalf("poll %d: unknown charge or reservation changed", i)
		}
	}
}
