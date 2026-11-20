package brokerinput

import (
	"reflect"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestAdvisoryPressureScarceAlternatePreservesWeightedFairness(t *testing.T) {
	const poolB = "44444444-4444-4444-8444-444444444444"
	const repoB = "33333333-3333-4333-8333-333333333333"
	for dimension, resource := range []string{"cpu", "ram", "disk"} {
		t.Run(resource, func(t *testing.T) {
			for _, tc := range []struct {
				name            string
				weight          uint32
				winner, waiting string
			}{
				{"equal-weights", 1, pool, repoB + ":2"},
				{"higher-weight", 3, poolB, repo + ":1"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s, p, d, o := fixture()
					size := s.Pools[pool].Size
					s.Global = capacitybroker.Resources{6000, 6000, 6000, 6}
					s.Hosts["host"] = capacitybroker.Host{Budget: s.Global, AcceptNew: true}
					alternate := capacitybroker.Host{Budget: s.Global, AcceptNew: true}
					alternate.Budget[dimension] = 2 * size[dimension]
					s.Hosts["other"] = alternate
					a := s.Pools[pool]
					a.Hosts = []string{"host", "other"}
					s.Pools[pool] = a
					b := a
					b.Repository = repoB
					s.Pools[poolB] = b
					s.Repositories[repoB] = capacitybroker.Repository{MaxRunners: 2}
					p.Repositories = append(p.Repositories, admission.BrokerRepositoryPolicy{
						ID: repoB, Weight: tc.weight, MaxRunners: 2,
						Pools: []admission.BrokerPoolPolicy{{ID: poolB, MaxRunners: 2, WarmIdle: 1}},
					})
					d.Pools = append(d.Pools, garmapi.DiscoveredPool{
						ManagedPool: garmapi.ManagedPool{ID: poolB, RepositoryID: repoB, Enabled: true},
						OwnerKind:   "repository", OwnerID: repoB,
					})
					d.Supply = append(d.Supply, garmapi.RunnerSupply{PoolID: poolB})
					d.Jobs[0].CreatedAt = o.Now.Add(-time.Second)
					d.Jobs = append(d.Jobs, garmapi.QueuedJob{
						WorkflowJobID: 2, RepositoryID: repoB, CreatedAt: o.Now, CandidatePoolIDs: []string{poolB},
					})
					// Keep equal existing charges for both repos. The uncertain hot
					// charge still counts toward fair share; the alternate charge
					// leaves one slot in exactly the selected resource dimension.
					s.Charges = []capacitybroker.Charge{
						{ID: "unknown-hot", Host: "host", Pool: pool, Resources: size},
						{ID: "existing-alternate", Host: "other", Pool: poolB, Resources: size},
					}
					charges := append([]capacitybroker.Charge(nil), s.Charges...)
					baseline, err := advisoryStock(s, p, d, o, nil)
					if err != nil || len(baseline.Assignments) != 2 || len(baseline.RemainingJobs) != 0 {
						t.Fatalf("fixture did not fit both jobs before the veto: %+v %v", baseline, err)
					}
					plan, err := advisoryStock(s, p, d, o, map[string]bool{"host": true})
					if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != tc.winner ||
						plan.Assignments[0].Host != "other" || plan.Assignments[0].Warm ||
						len(plan.RemainingJobs) != 1 || plan.RemainingJobs[0].ID != tc.waiting {
						t.Fatalf("veto changed fairness or overfilled %s: %+v %v", resource, plan, err)
					}
					if !reflect.DeepEqual(s.Charges, charges) || !s.Hosts["host"].AcceptNew || s.Hosts["other"] != alternate {
						t.Fatal("read-only planning modified its input")
					}
				})
			}
		})
	}
}
