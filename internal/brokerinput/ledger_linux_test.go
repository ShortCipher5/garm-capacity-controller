package brokerinput

import (
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

func ledgerFixture() (admission.BrokerGrantState, map[string]LedgerProfile, capacitybroker.Options) {
	_, policy, _, opt := fixture()
	size := admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1}
	total := admission.Resources{CPU: 4000, RAM: 4000, Disk: 4000, Runners: 4}
	state := admission.BrokerGrantState{
		Ready: true, Sequence: 12, Policy: policy,
		Config: admission.Config{Global: total, MaxObservationAge: time.Minute,
			Hosts: map[string]admission.Host{"host": {Budget: total, Healthy: true, Mode: "enabled", ObservedAt: opt.Now}},
			Pools: map[string]admission.Pool{pool: {Budget: total, Hosts: []string{"host"}}}},
	}
	return state, map[string]LedgerProfile{pool: {Repository: repo, Size: size}}, opt
}
func TestLedgerMappingPreservesPoolBudgetsThroughStock(t *testing.T) {
	for dim := 0; dim < 4; dim++ {
		s, profiles, opt := ledgerFixture()
		p := s.Config.Pools[pool]
		switch dim {
		case 0:
			p.Budget.CPU = 0
		case 1:
			p.Budget.RAM = 0
		case 2:
			p.Budget.Disk = 0
		case 3:
			p.Budget.Runners = 0
		}
		s.Config.Pools[pool] = p
		snapshot, err := ledgerSnapshot(s, nil, profiles, opt)
		if err != nil {
			t.Fatal(err)
		}
		_, _, demand, _ := fixture()
		plan, err := Stock(snapshot, s.Policy, demand, opt)
		if err != nil || len(plan.Assignments) != 0 {
			t.Fatalf("pool limit %d lost: %+v %v", dim, plan, err)
		}
	}
}
func TestLedgerMappingRejectsUnknownStaleOrMissingProfile(t *testing.T) {
	for _, mode := range []string{"not-ready", "stale", "future", "missing", "ownership", "size"} {
		t.Run(mode, func(t *testing.T) {
			s, profiles, opt := ledgerFixture()
			switch mode {
			case "not-ready":
				s.Ready = false
			case "stale":
				h := s.Config.Hosts["host"]
				h.ObservedAt = opt.Now.Add(-2 * time.Minute)
				s.Config.Hosts["host"] = h
			case "future":
				h := s.Config.Hosts["host"]
				h.ObservedAt = opt.Now.Add(time.Second)
				s.Config.Hosts["host"] = h
			case "missing":
				delete(profiles, pool)
			case "ownership":
				p := profiles[pool]
				p.Repository = "other"
				profiles[pool] = p
			case "size":
				p := profiles[pool]
				p.Size.RAM = 0
				profiles[pool] = p
			}
			if _, err := ledgerSnapshot(s, nil, profiles, opt); err == nil {
				t.Fatal("unsafe planning input accepted")
			}
		})
	}
}
func TestLedgerRevisionBindsProfileAndChargesRemainDetached(t *testing.T) {
	s, profiles, opt := ledgerFixture()
	charges := []admission.BrokerPlanningCharge{{ID: "unknown", Host: "host", Pool: pool, Resources: admission.Resources{CPU: 3000, RAM: 3000, Disk: 3000, Runners: 1}}}
	a, err := ledgerSnapshot(s, charges, profiles, opt)
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles[pool]
	profile.Size.CPU = 2000
	profiles[pool] = profile
	b, err := ledgerSnapshot(s, charges, profiles, opt)
	if err != nil || a.Revision == b.Revision {
		t.Fatal("profile not bound to revision")
	}
	if a.Charges[0].Resources[0] != 3000 || b.Charges[0].Resources[0] != 3000 {
		t.Fatal("historical size overwritten")
	}
}

func TestLedgerOutstandingReuseRequiresCurrentExactGrantBinding(t *testing.T) {
	for _, mode := range []string{"valid", "expired", "revoked", "wrong-pool", "wrong-provider", "wrong-size", "wrong-execution", "stale-binding", "disabled-host", "draining-host", "removed-host", "lower-global", "lower-host", "lower-pool"} {
		t.Run(mode, func(t *testing.T) {
			s, profiles, opt := ledgerFixture()
			profile := profiles[pool]
			profile.Authority = admission.StockProviderAuthority{PoolID: pool}
			profiles[pool] = profile
			grant := admission.BrokerGrant{ID: "durable", State: "unused", Spec: admission.BrokerGrantSpec{
				Host: "host", HostBindingSequence: 1, Authority: profile.Authority, Resources: profile.Size, ExpiresAt: opt.Now.Add(time.Hour)}}
			s.Allocated = admission.Allocations{Global: profile.Size, Hosts: map[string]admission.Resources{"host": profile.Size}, Pools: map[string]admission.Resources{pool: profile.Size}}
			s.HostBindings = map[string]admission.HostBinding{"host": {Sequence: 1}}
			switch mode {
			case "lower-global":
				s.Config.Global.CPU = 0
			case "lower-host":
				h := s.Config.Hosts["host"]
				h.Budget.RAM = 0
				s.Config.Hosts["host"] = h
			case "lower-pool":
				p := s.Config.Pools[pool]
				p.Budget.Disk = 0
				s.Config.Pools[pool] = p
			case "expired":
				grant.Spec.ExpiresAt = opt.Now
			case "revoked":
				grant.State = "revoked"
			case "wrong-pool":
				grant.Spec.Authority.PoolID = "other"
			case "wrong-provider":
				grant.Spec.Authority.ProviderName = "different"
			case "wrong-size":
				grant.Spec.Resources.CPU++
			case "wrong-execution":
				grant.Spec.Execution.Name = "not-a-grant-profile"
			case "stale-binding":
				s.HostBindings["host"] = admission.HostBinding{Sequence: 2}
			case "disabled-host":
				h := s.Config.Hosts["host"]
				h.Healthy = false
				s.Config.Hosts["host"] = h
			case "draining-host":
				h := s.Config.Hosts["host"]
				h.Mode = "draining"
				s.Config.Hosts["host"] = h
			case "removed-host":
				p := s.Config.Pools[pool]
				p.Hosts = nil
				s.Config.Pools[pool] = p
			}
			s.Grants = []admission.BrokerGrant{grant}
			charges := []admission.BrokerPlanningCharge{{ID: "durable", Host: "host", Pool: pool, Resources: profile.Size}}
			snapshot, err := ledgerSnapshot(s, charges, profiles, opt)
			if err != nil {
				t.Fatal(err)
			}
			_, _, demand, _ := fixture()
			plan, err := Stock(snapshot, s.Policy, demand, opt)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "valid" {
				if len(plan.CoveredByGrant) != 1 || len(plan.Assignments) != 0 {
					t.Fatalf("grant not reused: %+v", plan)
				}
			} else if len(plan.CoveredByGrant) != 0 {
				t.Fatalf("invalid grant reused: %+v", plan)
			}
			if len(snapshot.Charges) != 1 {
				t.Fatal("invalid grant silently freed")
			}
		})
	}
}
