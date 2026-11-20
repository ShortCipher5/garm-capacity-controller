package brokerinput

import (
	"math"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// The direct settings/history tests supply a deliberately roomy model so they
// exercise their own vetoes, not the independent configured-maximum scenario.
func roomyMaximumFit(ids ...string) *stockConfiguredMaximumFit {
	size := capacitybroker.Resources{1000, 1000, 1000, 1}
	room := capacitybroker.Resources{100000, 100000, 100000, 100}
	fit := &stockConfiguredMaximumFit{global: room, pools: map[string]stockPoolMaximumFit{}}
	for _, id := range ids {
		fit.pools[id] = stockPoolMaximumFit{size: size, remaining: room}
	}
	return fit
}

func TestStockScalingVetoesOversizedConfiguredMaximumWithoutDoubleCounting(t *testing.T) {
	const poolB = "44444444-4444-4444-8444-444444444444"
	const repoB = "33333333-3333-4333-8333-333333333333"
	for dim, resource := range []string{"cpu", "ram", "disk"} {
		t.Run(resource, func(t *testing.T) {
			build := func(offset time.Duration, maxB uint64) scalingPreviewSample {
				t.Helper()
				s, policy, observed, opt := fixture()
				state, _, _ := ledgerFixture()
				opt.Now = opt.Now.Add(offset)
				s.ObservedAt, observed.StartedAt, observed.FinishedAt = opt.Now, opt.Now, opt.Now
				s.Global = capacitybroker.Resources{6000, 6000, 6000, 6}
				s.Global[dim] = 4000
				s.Hosts["host"] = capacitybroker.Host{Budget: s.Global, AcceptNew: true}
				h := state.Config.Hosts["host"]
				h.ObservedAt = opt.Now
				state.Config.Hosts["host"] = h
				a := s.Pools[pool]
				a.Budget = &s.Global
				s.Pools[pool] = a
				b := a
				b.Repository = repoB
				b.Size[dim] = 2000
				s.Pools[poolB] = b
				s.Repositories[repoB] = capacitybroker.Repository{MaxRunners: 3}
				policy.Repositories = append(policy.Repositories, admission.BrokerRepositoryPolicy{
					ID: repoB, Weight: 1, MaxRunners: 3, Pools: []admission.BrokerPoolPolicy{{ID: poolB, MaxRunners: 3}},
				})
				state.Policy = policy
				observed.Pools[0].MaxRunners = 1
				observed.Pools[0].DiscoveryFingerprint = "a-settings"
				observed.Pools = append(observed.Pools, garmapi.DiscoveredPool{
					ManagedPool: garmapi.ManagedPool{ID: poolB, RepositoryID: repoB, Enabled: true, MaxRunners: maxB},
					OwnerKind:   "repository", OwnerID: repoB, DiscoveryFingerprint: "b-settings",
				})
				observed.Supply = append(observed.Supply, garmapi.RunnerSupply{PoolID: poolB})
				s.Charges = []capacitybroker.Charge{{ID: "retained", Host: "host", Pool: pool, Resources: a.Size}}
				state.Allocated.Global = admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1}
				state.Allocated.Pools = map[string]admission.Resources{pool: state.Allocated.Global}
				plan, err := stockPlan(s, policy, observed, opt, true, true)
				if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != pool {
					t.Fatalf("actual charged planner should still fit the job: %+v %v", plan, err)
				}
				sample, err := stockScalingSample(state, s, observed, plan, "modeled-source", opt)
				if err != nil || sample.pools[pool].target != 2 {
					t.Fatalf("sample should consider growth before maximum fit: %+v %v", sample, err)
				}
				return scalingPreviewSample{StockScalingSample: sample, scope: [32]byte{1}}
			}
			for _, maxB := range []uint64{1, 2} {
				a, b := build(0, maxB), build(20*time.Second, maxB)
				var history scalingPreviewHistory
				history.accept(a, nil, a.finish, a.expires)
				rows := history.accept(b, nil, b.finish, b.expires).Proposals
				if len(rows) != 1 || (maxB == 1 && (rows[0].PoolID != pool || rows[0].Suggested != 2)) ||
					(maxB == 2 && (rows[0].PoolID != poolB || rows[0].Suggested != 1)) {
					t.Fatalf("max B %d: double counted charges or borrowed unconfirmed reduction: %+v", maxB, rows)
				}
			}
		})
	}
}

func TestStockConfiguredMaximumFitBoundsBothSamplesAndCumulativeGrowth(t *testing.T) {
	a, b := roomyMaximumFit("a", "b"), roomyMaximumFit("a", "b")
	b.global[2] = 1000
	fit := compareStockMaximumFit(a, b)
	if !fit.tryGrowth("a") || fit.tryGrowth("b") || b.global[2] != 1000 {
		t.Fatal("second proposal overspent or comparison mutated evidence")
	}
	row := a.pools["a"]
	row.remaining[1] = 999
	a.pools["a"] = row
	fit = compareStockMaximumFit(a, b)
	if fit.tryGrowth("a") || !fit.tryGrowth("b") {
		t.Fatal("vetoed early candidate either fit or consumed the last unit")
	}
	fit = compareStockMaximumFit(a, nil)
	if fit.tryGrowth("b") || subtractMaximumExposure(math.MaxInt64, 2, math.MaxInt64) != 0 {
		t.Fatal("missing fit or wrapped cost supplied headroom")
	}
}
