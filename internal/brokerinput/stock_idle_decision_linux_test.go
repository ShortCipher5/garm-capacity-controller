package brokerinput

import (
	"math"
	"reflect"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestStockIdleGrowthDoesNotRaiseMaximumOrSpendUnfilledFloor(t *testing.T) {
	for _, tc := range []struct {
		name             string
		minimum, maximum uint64
		warm             int64
		used, idle       int64
		fits             bool
	}{
		{"one warm slot inside unchanged max", 0, 2, 1, 1, 0, true},
		{"already at desired warm floor", 1, 2, 1, 1, 0, false},
		{"unchanged max already occupied", 0, 2, 1, 2, 0, false},
		{"last runner budget belongs to existing floor", 1, 2, 2, 1, 0, false},
		{"already more idle than floor", 0, 2, 1, 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, policy, obs, opt := fixture()
			p := s.Pools[pool]
			p.Budget = &s.Global
			s.Pools[pool] = p
			policy.Repositories[0].Pools[0].WarmIdle = tc.warm
			obs.Pools[0].MinIdleRunners, obs.Pools[0].MaxRunners = tc.minimum, tc.maximum
			obs.Supply[0].Idle, obs.Supply[0].Busy = tc.idle, tc.used-tc.idle
			obs.Jobs = nil
			// Stock idle/busy identities and attribution are checked by the
			// sampler; this directly exercises only the modeled fit stage.
			for i := int64(0); i < tc.used; i++ {
				s.Charges = append(s.Charges, capacitybroker.Charge{ID: string(rune('a' + i)), Pool: pool, Host: "host", Resources: p.Size})
			}
			charges := append([]capacitybroker.Charge(nil), s.Charges...)
			rows, order := planStockIdleGrowth(s, policy, obs, opt)
			got, ok := rows[pool]
			if ok != tc.fits || len(order) != len(rows) || (ok && (got.ObservedMinIdle != int64(tc.minimum) ||
				got.SuggestedMinIdle != int64(tc.minimum)+1 || got.UnchangedMaximum != int64(tc.maximum))) {
				t.Fatalf("growth=%+v order=%v", rows, order)
			}
			if !reflect.DeepEqual(charges, s.Charges) || policy.Repositories[0].Pools[0].WarmIdle != tc.warm {
				t.Fatal("changed planning inputs")
			}
		})
	}
}

func TestStockIdleGrowthNeverBorrowsPeerCutOrUnknownResources(t *testing.T) {
	const peerPool = "44444444-4444-4444-8444-444444444444"
	const peerRepo = "33333333-3333-4333-8333-333333333333"
	for dim, resource := range []string{"cpu", "ram", "storage"} {
		t.Run(resource, func(t *testing.T) {
			s, policy, obs, opt := fixture()
			s.Global = capacitybroker.Resources{3000, 3000, 3000, 3}
			s.Hosts["host"] = capacitybroker.Host{Budget: s.Global, AcceptNew: true}
			p := s.Pools[pool]
			p.Budget = &s.Global
			s.Pools[pool] = p
			peer := p
			peer.Repository = peerRepo
			peer.Size[dim] = 2000
			s.Pools[peerPool] = peer
			s.Repositories[peerRepo] = capacitybroker.Repository{MaxRunners: 1}
			policy.Repositories[0].Pools[0].WarmIdle = 1
			policy.Repositories = append(policy.Repositories, admission.BrokerRepositoryPolicy{ID: peerRepo, Weight: 1, MaxRunners: 1, Pools: []admission.BrokerPoolPolicy{{ID: peerPool, MaxRunners: 1}}})
			obs.Jobs = nil
			obs.Pools[0].MaxRunners = 1
			obs.Pools = append(obs.Pools, garmapi.DiscoveredPool{
				ManagedPool: garmapi.ManagedPool{ID: peerPool, RepositoryID: peerRepo, Enabled: true, MaxRunners: 1, MinIdleRunners: 1},
				OwnerKind:   "repository", OwnerID: peerRepo,
			})
			obs.Supply = append(obs.Supply, garmapi.RunnerSupply{PoolID: peerPool})
			if rows, _ := planStockIdleGrowth(s, policy, obs, opt); len(rows) != 1 || rows[pool].SuggestedMinIdle != 1 {
				t.Fatalf("known peer floor and one new warm slot should fit: %+v", rows)
			}
			// The peer has a policy floor of zero, but its unchanged stock
			// floor still consumes capacity. One unknown unit leaves no room
			// for both the existing peer floor and the new warm slot.
			cost := capacitybroker.Resources{}
			cost[dim] = 1
			s.Charges = []capacitybroker.Charge{{ID: "unattributed-unknown", Host: "host", Resources: cost}}
			if rows, _ := planStockIdleGrowth(s, policy, obs, opt); len(rows) != 0 {
				t.Fatalf("borrowed a peer cut or discounted unknown charge: %+v", rows)
			}
			s.Charges = nil
			obs.Supply[1].Other = 1
			if rows, _ := planStockIdleGrowth(s, policy, obs, opt); len(rows) != 0 {
				t.Fatalf("ambiguous supply should veto growth: %+v", rows)
			}
		})
	}
}

func TestStockIdleDecisionRefusesUnsealedObservations(t *testing.T) {
	_, _, obs, opt := fixture()
	if (stockIdleDecisionSample{source: "detached", observation: obs}).quietScope(opt.Now) {
		t.Fatal("caller-created quiet scope accepted")
	}
	budget := capacitybroker.Resources{math.MaxInt64, 1, 1, 2}
	left := budget
	if subtractStockMaximumModel(&left, budget, capacitybroker.Resources{math.MaxInt64, 1, 1, 1}, 2) || left != budget {
		t.Fatal("oversized profile wrapped or partly debited the aggregate")
	}
}
