package brokerinput

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestRoutedStockSixOrdinaryPoolsShareProviderAndRetainedBudget(t *testing.T) {
	const provider = "shared-provider"
	_, observation, source, opt := enrolledCycleFixture(t)
	unit := capacitybroker.Resources{1000, 1000, 1000, 1}
	six := capacitybroker.Resources{6000, 6000, 6000, 6}
	four := capacitybroker.Resources{4000, 4000, 4000, 4}
	// Offline input only: no router, GARM, host, or retirement is invoked.
	state := admission.BrokerGrantState{
		Policy:       admission.BrokerPolicy{Revision: 1},
		HostBindings: map[string]admission.HostBinding{"a": {Sequence: 3}, "b": {Sequence: 4}},
		PoolProfiles: map[string]admission.BrokerPoolProfile{},
	}
	snapshot := capacitybroker.Snapshot{
		Revision: "modeled", ObservedAt: opt.Now, Complete: true, Global: six,
		Hosts: map[string]capacitybroker.Host{"a": {Budget: six, AcceptNew: true}, "b": {Budget: six, AcceptNew: true}},
		Pools: map[string]capacitybroker.Pool{}, Repositories: map[string]capacitybroker.Repository{},
	}
	// Keep the independent source stamp; the remaining fields are modeled
	// to exercise six pools with one shared provider in the offline calculation.
	observation.StartedAt, observation.FinishedAt, observation.Digest = opt.Now, opt.Now, "modeled"
	observation.Pools, observation.Supply, observation.Jobs = nil, nil, nil
	for i := 1; i <= 6; i++ {
		r := fmt.Sprintf("11111111-1111-4111-8111-%012d", i)
		p := fmt.Sprintf("22222222-2222-4222-8222-%012d", i)
		fingerprint := fmt.Sprintf("modeled-pool-%d", i)
		snapshot.Repositories[r] = capacitybroker.Repository{Weight: 1, MaxRunners: 4}
		snapshot.Pools[p] = capacitybroker.Pool{Repository: r, Size: unit, Budget: &four, Hosts: []string{"a", "b"}, MaxRunners: 4}
		state.Policy.Repositories = append(state.Policy.Repositories, admission.BrokerRepositoryPolicy{ID: r, Weight: 1, MaxRunners: 4, Pools: []admission.BrokerPoolPolicy{{ID: p, MaxRunners: 4}}})
		state.PoolProfiles[p] = admission.BrokerPoolProfile{Pool: p, Repository: r, SourceIdentity: source, PoolFingerprint: fingerprint,
			Domains:   []admission.BrokerProfileDomain{{Host: "a", HostBindingSequence: 3}, {Host: "b", HostBindingSequence: 4}},
			Authority: admission.StockProviderAuthority{PoolID: p, ProviderName: provider}}
		observation.Pools = append(observation.Pools, garmapi.DiscoveredPool{ManagedPool: garmapi.ManagedPool{ID: p, RepositoryID: r, Enabled: true, ProviderName: provider, MaxRunners: 4}, OwnerKind: "repository", OwnerID: r, OwnershipFingerprint: fingerprint})
		observation.Supply = append(observation.Supply, garmapi.RunnerSupply{PoolID: p})
		observation.Jobs = append(observation.Jobs, garmapi.QueuedJob{WorkflowJobID: int64(i), RepositoryID: r, CandidatePoolIDs: []string{p}, CreatedAt: opt.Now})
		host := "a"
		if i == 6 {
			host = "b"
		} // Off-route occupancy still consumes global capacity.
		snapshot.Charges = append(snapshot.Charges, capacitybroker.Charge{ID: fingerprint, Pool: p, Host: host, Resources: unit})
	}
	routes := []VerifiedStockProviderRoute{{Source: source, Provider: provider, Host: "a", HostBindingSequence: 3, ObservedAt: opt.Now.Add(-time.Second)}}
	original := append([]capacitybroker.Charge(nil), snapshot.Charges...)
	full, expiry, err := planRoutedStockPreview(state, snapshot, observation, source, opt, routes)
	if err != nil || !expiry.Equal(routes[0].ObservedAt.Add(opt.MaxAge)) || len(full.Assignments) != 0 || len(full.RemainingJobs) != 6 {
		t.Fatalf("one shared route must not multiply or erase retained global capacity: %+v %v %v", full, expiry, err)
	}

	// A separately supplied model with five charges. This does not represent a
	// release operation; no observation here establishes actual settlement.
	settled := snapshot
	settled.Charges = append([]capacitybroker.Charge(nil), snapshot.Charges[:5]...)
	if plan, expiry, err := planRoutedStockPreview(state, settled, observation, source, opt, nil); !errors.Is(err, capacitybroker.ErrSnapshot) || !expiry.IsZero() || len(plan.Assignments) != 0 {
		t.Fatalf("modeled headroom is not evidence of a provider route: %+v %v %v", plan, expiry, err)
	}
	plan, _, err := planRoutedStockPreview(state, settled, observation, source, opt, routes)
	if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Host != "a" || len(plan.RemainingJobs) != 5 {
		t.Fatalf("one shared route may use only the one modeled free slot: %+v %v", plan, err)
	}
	if !reflect.DeepEqual(snapshot.Charges, original) || !reflect.DeepEqual(settled.Charges, original[:5]) {
		t.Fatal("route comparison changed retained accounting")
	}
	for _, p := range snapshot.Pools {
		if !reflect.DeepEqual(p.Hosts, []string{"a", "b"}) {
			t.Fatal("route comparison changed the configured pool")
		}
	}
}
