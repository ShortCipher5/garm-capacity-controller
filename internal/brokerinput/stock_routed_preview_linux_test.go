package brokerinput

import (
	"reflect"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func routedStockFixture(t *testing.T) (admission.BrokerGrantState, capacitybroker.Snapshot, garmapi.DemandObservation, capacitybroker.Options, []VerifiedStockProviderRoute) {
	t.Helper()
	snapshot, policy, _, _ := fixture()
	_, observation, source, opt := enrolledCycleFixture(t)
	snapshot.ObservedAt = opt.Now
	p := snapshot.Pools[pool]
	p.Hosts = []string{"a", "b"}
	snapshot.Pools[pool] = p
	snapshot.Hosts = map[string]capacitybroker.Host{"a": {Budget: p.Size, AcceptNew: true}, "b": {Budget: p.Size, AcceptNew: true}}
	snapshot.Charges = []capacitybroker.Charge{{ID: "retained-unknown", Host: "a", Pool: pool, Resources: p.Size}}
	observed := observation.Pools[0]
	state := admission.BrokerGrantState{
		Policy:       policy,
		HostBindings: map[string]admission.HostBinding{"a": {Sequence: 3}, "b": {Sequence: 4}},
		PoolProfiles: map[string]admission.BrokerPoolProfile{
			pool: {Pool: pool, Repository: repo, SourceIdentity: source, PoolFingerprint: observed.OwnershipFingerprint,
				Domains:   []admission.BrokerProfileDomain{{Host: "a", HostBindingSequence: 3}, {Host: "b", HostBindingSequence: 4}},
				Authority: admission.StockProviderAuthority{PoolID: pool, ProviderName: observed.ProviderName}},
		},
	}
	return state, snapshot, observation, opt, []VerifiedStockProviderRoute{{Source: source, Provider: observed.ProviderName, Host: "a", HostBindingSequence: 3, ObservedAt: opt.Now.Add(-time.Second)}}
}

func TestRoutedStockPreviewRequiresCurrentEnrolledDestination(t *testing.T) {
	for name, edit := range map[string]func(*admission.BrokerPoolProfile){
		"wrong repository":      func(p *admission.BrokerPoolProfile) { p.Repository = "elsewhere" },
		"missing destination":   func(p *admission.BrokerPoolProfile) { p.Domains = p.Domains[1:] },
		"stale destination":     func(p *admission.BrokerPoolProfile) { p.Domains[0].HostBindingSequence-- },
		"duplicate destination": func(p *admission.BrokerPoolProfile) { p.Domains = append(p.Domains, p.Domains[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			state, snapshot, observation, opt, routes := routedStockFixture(t)
			profile := state.PoolProfiles[pool]
			edit(&profile)
			state.PoolProfiles[pool] = profile
			plan, expiry, err := planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes)
			if err != capacitybroker.ErrSnapshot || !expiry.IsZero() || len(plan.Assignments) != 0 {
				t.Fatalf("invalid pool enrollment returned advice: %+v %v %v", plan, expiry, err)
			}
		})
	}
}

func TestRoutedStockPreviewDoesNotBorrowSpareOnWrongHost(t *testing.T) {
	state, snapshot, observation, opt, routes := routedStockFixture(t)
	// The ordinary read-only planner can use B because it models the pool
	// allowlist, not the independently verified stock route.
	ordinary, err := stockPlan(snapshot, state.Policy, observation, opt, true, true)
	if err != nil || len(ordinary.Assignments) != 1 || ordinary.Assignments[0].Host != "b" {
		t.Fatalf("fixture did not model spare on B: %+v %v", ordinary, err)
	}
	routed, expiry, err := planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes)
	if err != nil || len(routed.Assignments) != 0 || len(routed.RemainingJobs) != 1 || !expiry.Equal(routes[0].ObservedAt.Add(opt.MaxAge)) {
		t.Fatalf("borrowed B or dropped blocked job: %+v %v %v", routed, expiry, err)
	}
	// With verified B routing, A's old charge retains its host and remains
	// charged to the same pool and global budget.
	routes[0].Host, routes[0].HostBindingSequence = "b", 4
	routed, _, err = planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes)
	if err != nil || len(routed.Assignments) != 1 || routed.Assignments[0].Host != "b" ||
		!reflect.DeepEqual(snapshot.Pools[pool].Hosts, []string{"a", "b"}) || snapshot.Charges[0].Host != "a" {
		t.Fatalf("valid route rejected or original accounting changed: %+v %v", routed, err)
	}
	snapshot.Global = snapshot.Pools[pool].Size
	routed, _, err = planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes)
	if err != nil || len(routed.Assignments) != 0 {
		t.Fatalf("A's retained charge lost from global accounting: %+v %v", routed, err)
	}
}

func TestRoutedStockPreviewRejectsUnverifiedRoutes(t *testing.T) {
	for name, edit := range map[string]func(*capacitybroker.Snapshot, *[]VerifiedStockProviderRoute, capacitybroker.Options){
		"missing": func(_ *capacitybroker.Snapshot, r *[]VerifiedStockProviderRoute, _ capacitybroker.Options) { *r = nil },
		"provider mismatch": func(_ *capacitybroker.Snapshot, r *[]VerifiedStockProviderRoute, _ capacitybroker.Options) {
			(*r)[0].Provider = "stock-b"
		},
		"host mismatch": func(_ *capacitybroker.Snapshot, r *[]VerifiedStockProviderRoute, _ capacitybroker.Options) {
			(*r)[0].Host = "b"
		},
		"wrong source": func(_ *capacitybroker.Snapshot, r *[]VerifiedStockProviderRoute, _ capacitybroker.Options) {
			(*r)[0].Source = "other"
		},
		"expired": func(_ *capacitybroker.Snapshot, r *[]VerifiedStockProviderRoute, o capacitybroker.Options) {
			(*r)[0].ObservedAt = o.Now.Add(-o.MaxAge)
		},
		"future": func(_ *capacitybroker.Snapshot, r *[]VerifiedStockProviderRoute, o capacitybroker.Options) {
			(*r)[0].ObservedAt = o.Now.Add(time.Nanosecond)
		},
		"duplicate": func(_ *capacitybroker.Snapshot, r *[]VerifiedStockProviderRoute, _ capacitybroker.Options) {
			*r = append(*r, (*r)[0])
		},
		"outside allowlist": func(s *capacitybroker.Snapshot, _ *[]VerifiedStockProviderRoute, _ capacitybroker.Options) {
			p := s.Pools[pool]
			p.Hosts = []string{"b"}
			s.Pools[pool] = p
		},
	} {
		t.Run(name, func(t *testing.T) {
			state, snapshot, observation, opt, routes := routedStockFixture(t)
			edit(&snapshot, &routes, opt)
			plan, expiry, err := planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes)
			if err != capacitybroker.ErrSnapshot || !expiry.IsZero() || len(plan.Assignments) != 0 {
				t.Fatalf("uncertain route returned advice: %+v %v %v", plan, expiry, err)
			}
		})
	}
}

func TestRoutedStockPreviewKeepsSpareForActuallyRoutedPool(t *testing.T) {
	const poolB, repoB = "44444444-4444-4444-8444-444444444444", "33333333-3333-4333-8333-333333333333"
	state, snapshot, observation, opt, routes := routedStockFixture(t)
	p := snapshot.Pools[pool]
	p.Repository = repoB
	snapshot.Pools[poolB] = p
	snapshot.Repositories[repoB] = capacitybroker.Repository{MaxRunners: 2}
	state.Policy.Repositories = append(state.Policy.Repositories, admission.BrokerRepositoryPolicy{
		ID: repoB, Weight: 1, MaxRunners: 2, Pools: []admission.BrokerPoolPolicy{{ID: poolB, MaxRunners: 2}},
	})
	profile := state.PoolProfiles[pool]
	profile.Pool, profile.Repository, profile.PoolFingerprint = poolB, repoB, "registered-ownership-b"
	profile.Authority.PoolID, profile.Authority.ProviderName = poolB, "stock-b"
	state.PoolProfiles[poolB] = profile
	remote := observation.Pools[0]
	remote.ID, remote.RepositoryID, remote.OwnerID = poolB, repoB, repoB
	remote.ProviderName, remote.OwnershipFingerprint = "stock-b", profile.PoolFingerprint
	observation.Pools = append(observation.Pools, remote)
	observation.Supply = append(observation.Supply, garmapi.RunnerSupply{PoolID: poolB})
	observation.Jobs = append(observation.Jobs, garmapi.QueuedJob{
		WorkflowJobID: 2, RepositoryID: repoB, CreatedAt: opt.Now, CandidatePoolIDs: []string{poolB},
	})
	if _, _, err := planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes); err != capacitybroker.ErrSnapshot {
		t.Fatal("incomplete route map trusted", err)
	}
	routes = append(routes, VerifiedStockProviderRoute{Source: observation.SourceIdentity(), Provider: "stock-b", Host: "b", HostBindingSequence: 4, ObservedAt: opt.Now.Add(-2 * time.Second)})
	plan, expiry, err := planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes)
	if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Pool != poolB || plan.Assignments[0].Host != "b" ||
		len(plan.RemainingJobs) != 1 || plan.RemainingJobs[0].ID != repo+":1" || !expiry.Equal(routes[1].ObservedAt.Add(opt.MaxAge)) {
		t.Fatalf("misallocated pooled spare capacity: %+v %v %v", plan, expiry, err)
	}
}

func TestRoutedStockPreviewBoundsExpiryByDemandAndCapacity(t *testing.T) {
	for _, input := range []string{"demand started", "capacity observed"} {
		t.Run(input, func(t *testing.T) {
			state, snapshot, observation, opt, routes := routedStockFixture(t)
			oldest := &snapshot.ObservedAt
			if input == "demand started" {
				oldest = &observation.StartedAt
			}
			*oldest = opt.Now.Add(-opt.MaxAge + time.Second)
			plan, expiry, err := planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes)
			if err != nil || !expiry.Equal(opt.Now.Add(time.Second)) || len(plan.RemainingJobs) != 1 {
				t.Errorf("expected routed advice to expire with %s: %+v %v %v", input, plan, expiry, err)
			}
			*oldest = opt.Now.Add(-opt.MaxAge)
			plan, expiry, err = planRoutedStockPreview(state, snapshot, observation, observation.SourceIdentity(), opt, routes)
			if err != capacitybroker.ErrSnapshot || !expiry.IsZero() || len(plan.RemainingJobs) != 0 {
				t.Errorf("expired %s still returned routed advice: %+v %v %v", input, plan, expiry, err)
			}
		})
	}
}

func TestRoutedStockPreviewRequiresDemandFromSameSource(t *testing.T) {
	state, snapshot, observation, opt, routes := routedStockFixture(t)
	source := observation.SourceIdentity()
	routes[0].Host, routes[0].HostBindingSequence = "b", 4
	_, other, otherSource, later := enrolledCycleFixture(t)
	if source == "" || otherSource == "" || otherSource == source ||
		observation.Pools[0].OwnershipFingerprint != other.Pools[0].OwnershipFingerprint {
		t.Fatal("expected identical enrolled pool identity from two different stock sources")
	}
	opt.Now = later.Now
	plan, expiry, err := planRoutedStockPreview(state, snapshot, observation, source, opt, routes)
	if err != nil || expiry.IsZero() || len(plan.Assignments) != 1 || plan.Assignments[0].Host != "b" {
		t.Fatalf("matching source should retain valid advice: %+v %v %v", plan, expiry, err)
	}
	// Copy exactly the public fields without the independently stamped source.
	unstamped := garmapi.DemandObservation{
		StartedAt: observation.StartedAt, FinishedAt: observation.FinishedAt, Digest: observation.Digest,
		Pools: observation.Pools, Supply: observation.Supply, Jobs: observation.Jobs,
	}
	for _, tc := range []struct {
		name        string
		observation garmapi.DemandObservation
	}{
		{"absent", unstamped},
		{"another stock source", other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, expiry, err := planRoutedStockPreview(state, snapshot, tc.observation, source, opt, routes)
			if err != capacitybroker.ErrSnapshot || !expiry.IsZero() || len(plan.Assignments) != 0 {
				t.Fatalf("detached demand yielded routed advice: %+v %v %v", plan, expiry, err)
			}
		})
	}
}
