package brokerinput

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestThreeRepositoryCapsFourShareSixWithoutBorrowingUnsettledCharges(t *testing.T) {
	// Independent modeled input views, NOT a retirement/release transition or
	// proof that stock GARM/physical resources actually settled. No I/O occurs.
	repos := []string{repo, "33333333-3333-4333-8333-333333333333", "55555555-5555-4555-8555-555555555555"}
	pools := []string{pool, "44444444-4444-4444-8444-444444444444", "66666666-6666-4666-8666-666666666666"}
	base := time.Unix(1700000000, 0).UTC()
	for _, tc := range []struct {
		name                string
		charged, maxima     [3]int64
		assignments, growth int
	}{
		{"six retained charges", [3]int64{4, 2, 0}, [3]int64{4, 4, 1}, 0, 0},
		{"lower settings cannot erase six retained charges", [3]int64{4, 2, 0}, [3]int64{1, 1, 1}, 0, 0},
		{"settled occupancy alone cannot borrow oversized maxima", [3]int64{1, 1, 0}, [3]int64{4, 4, 1}, 2, 0},
		{"independent settled and adjusted view", [3]int64{1, 1, 0}, [3]int64{1, 1, 1}, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func(at time.Time) StockScalingSample {
				state, _, opt := ledgerFixture()
				opt.Now = at
				total := capacitybroker.Resources{6000, 6000, 6000, 6}
				size := capacitybroker.Resources{1000, 1000, 1000, 1}
				budget := capacitybroker.Resources{4000, 4000, 4000, 4}
				snapshot := capacitybroker.Snapshot{Revision: "modeled-view", ObservedAt: at, Complete: true, Global: total, Hosts: map[string]capacitybroker.Host{"host": {Budget: total, AcceptNew: true}}, Repositories: map[string]capacitybroker.Repository{}, Pools: map[string]capacitybroker.Pool{}}
				policy := admission.BrokerPolicy{Revision: 1}
				observation := garmapi.DemandObservation{StartedAt: at, FinishedAt: at, Digest: "modeled-not-authenticated"}
				allocated := admission.Allocations{Hosts: map[string]admission.Resources{}, Pools: map[string]admission.Resources{}}
				for i, id := range pools {
					snapshot.Repositories[repos[i]] = capacitybroker.Repository{Weight: 1, MaxRunners: 4}
					snapshot.Pools[id] = capacitybroker.Pool{Budget: &budget, Repository: repos[i], Size: size, Hosts: []string{"host"}, MaxRunners: 4}
					policy.Repositories = append(policy.Repositories, admission.BrokerRepositoryPolicy{ID: repos[i], Weight: 1, MaxRunners: 4, Pools: []admission.BrokerPoolPolicy{{ID: id, MaxRunners: 4}}})
					observation.Pools = append(observation.Pools, garmapi.DiscoveredPool{ManagedPool: garmapi.ManagedPool{ID: id, RepositoryID: repos[i], Enabled: true, MaxRunners: uint64(tc.maxima[i])}, OwnerKind: "repository", OwnerID: repos[i]})
					observation.Pools[i].DiscoveryFingerprint = fmt.Sprintf("modeled-settings-%d", tc.maxima[i])
					observation.Supply = append(observation.Supply, garmapi.RunnerSupply{PoolID: id})
					for n := int64(0); n < tc.charged[i]; n++ {
						snapshot.Charges = append(snapshot.Charges, capacitybroker.Charge{ID: fmt.Sprintf("retained-%d-%d", i, n), Host: "host", Pool: id, Resources: size})
					}
					charge := admission.Resources{CPU: 1000 * tc.charged[i], RAM: 1000 * tc.charged[i], Disk: 1000 * tc.charged[i], Runners: tc.charged[i]}
					allocated.Pools[id] = charge
					allocated.Global.CPU += charge.CPU
					allocated.Global.RAM += charge.RAM
					allocated.Global.Disk += charge.Disk
					allocated.Global.Runners += charge.Runners
				}
				allocated.Hosts["host"] = allocated.Global
				state.Allocated = allocated
				state.Policy = policy
				state.Config.Hosts["host"] = admission.Host{ObservedAt: at}
				for n := int64(1); n <= 2; n++ {
					observation.Jobs = append(observation.Jobs, garmapi.QueuedJob{WorkflowJobID: n, RepositoryID: repos[2], CreatedAt: at.Add(-time.Minute), CandidatePoolIDs: []string{pools[2]}})
				}
				originalCharges := append([]capacitybroker.Charge(nil), snapshot.Charges...)
				plan, err := stockPlan(snapshot, policy, observation, opt, true, true)
				if err != nil || len(plan.Assignments) != tc.assignments {
					t.Fatalf("assignments=%v, err=%v", plan.Assignments, err)
				}
				if int64(len(plan.Assignments))+allocated.Global.Runners > 6 {
					t.Fatal("shared quota exceeded")
				}
				for _, a := range plan.Assignments {
					if a.Pool != pools[2] {
						t.Fatal("assignment left queued repository")
					}
				}
				sample, err := stockScalingSample(state, snapshot, observation, plan, "modeled-scoped-source", opt)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(originalCharges, snapshot.Charges) || state.Allocated.Global != allocated.Global {
					t.Fatal("comparison changed retained charges")
				}
				return sample
			}
			prior, current := build(base), build(base.Add(20*time.Second))
			proposals := ProposeStockMaximums(prior, current, current.finish)
			growth := 0
			for _, p := range proposals {
				if p.Suggested > p.Observed {
					growth++
					if p.PoolID != pools[2] || p.RepositoryID != repos[2] || p.Observed != 1 || p.Suggested != 2 {
						t.Fatalf("unexpected growth %+v", p)
					}
				}
			}
			if growth != tc.growth {
				t.Fatalf("growth=%d expected%d; %+v", growth, tc.growth, proposals)
			}
		})
	}
}
