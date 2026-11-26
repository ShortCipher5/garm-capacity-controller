package brokerinput

import (
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

func TestReadPlanCannotOutliveProvisionallyReusedGrant(t *testing.T) {
	state, profiles, opt := ledgerFixture()
	profile := profiles[pool]
	profile.Authority = admission.StockProviderAuthority{PoolID: pool}
	profiles[pool] = profile
	expiry := opt.Now.Add(5 * time.Second)
	state.Grants = []admission.BrokerGrant{{ID: "durable", State: "unused", IssuedAt: opt.Now.Add(-time.Minute), EvidenceBound: true, Spec: admission.BrokerGrantSpec{Host: "host", HostBindingSequence: 1, Authority: profile.Authority, Resources: profile.Size, ExpiresAt: expiry}}}
	state.HostBindings = map[string]admission.HostBinding{"host": {Sequence: 1}}
	state.Allocated = admission.Allocations{Global: profile.Size, Hosts: map[string]admission.Resources{"host": profile.Size}, Pools: map[string]admission.Resources{pool: profile.Size}}
	charges := []admission.BrokerPlanningCharge{{ID: "durable", Host: "host", Pool: pool, Resources: profile.Size}}
	_, _, demand, _ := fixture()
	snapshot, err := ledgerSnapshot(state, charges, profiles, opt)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Stock(snapshot, state.Policy, demand, opt)
	if err != nil || len(plan.CoveredByGrant) != 1 || len(plan.Assignments) != 0 {
		t.Fatalf("initial grant supply: %+v %v", plan, err)
	}
	out := projectReadPlan(state, demand, plan, opt.Now, time.Minute)
	// Time alone changes eligibility: no ledger revision or charge is removed.
	opt.Now = expiry
	after, err := ledgerSnapshot(state, charges, profiles, opt)
	if err != nil {
		t.Fatal(err)
	}
	replanned, err := Stock(after, state.Policy, demand, opt)
	if err != nil || len(replanned.CoveredByGrant) != 0 || len(replanned.Assignments) != 1 || len(after.Charges) != 1 {
		t.Fatalf("expired grant must stay charged but not cover demand: %+v %v", replanned, err)
	}
	if out.ExpiresAt.After(expiry) {
		t.Fatalf("projection claims grant coverage until %s, but grant stops covering demand at %s without ledger change", out.ExpiresAt, expiry)
	}
}

func TestReadPlanGrantDeadlineAlsoBoundsWarmSupply(t *testing.T) {
	state, profiles, opt := ledgerFixture()
	profile := profiles[pool]
	profile.Authority = admission.StockProviderAuthority{PoolID: pool}
	profiles[pool] = profile
	expiry := opt.Now.Add(5 * time.Second)
	state.Grants = []admission.BrokerGrant{{ID: "durable", State: "unused", IssuedAt: opt.Now.Add(-time.Minute), EvidenceBound: true, Spec: admission.BrokerGrantSpec{Host: "host", HostBindingSequence: 1, Authority: profile.Authority, Resources: profile.Size, ExpiresAt: expiry}}}
	state.HostBindings = map[string]admission.HostBinding{"host": {Sequence: 1}}
	state.Allocated = admission.Allocations{Global: profile.Size, Hosts: map[string]admission.Resources{"host": profile.Size}, Pools: map[string]admission.Resources{pool: profile.Size}}
	state.Policy.Repositories[0].Pools[0].WarmIdle = 1
	charges := []admission.BrokerPlanningCharge{{ID: "durable", Host: "host", Pool: pool, Resources: profile.Size}}
	_, _, demand, _ := fixture()
	demand.Jobs = nil
	for _, at := range []time.Time{opt.Now, expiry} {
		opt.Now = at
		snapshot, err := ledgerSnapshot(state, charges, profiles, opt)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := Stock(snapshot, state.Policy, demand, opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Charges) != 1 {
			t.Fatal("charge lost")
		}
		if at.Before(expiry) {
			if len(plan.Assignments) != 0 {
				t.Fatal("warm grant was not reused")
			}
			// Completion after the deadline cannot resurrect the earlier projection.
			out := projectReadPlan(state, demand, plan, expiry, time.Minute)
			if !out.ExpiresAt.Equal(expiry) || out.ExpiresAt.After(expiry) {
				t.Fatal(out.ExpiresAt)
			}
		} else if len(plan.Assignments) != 1 || !plan.Assignments[0].Warm {
			t.Fatalf("warm demand not re-evaluated at equality: %+v", plan)
		}
	}
}

func TestReadPlanGrantDeadlineScope(t *testing.T) {
	for _, mode := range []string{"unused", "consumed", "revoked", "expired", "unrelated", "already-expired", "equal-start", "later-than-host"} {
		t.Run(mode, func(t *testing.T) {
			state, _, opt := ledgerFixture()
			_, _, demand, _ := fixture()
			grant := admission.BrokerGrant{State: mode, Spec: admission.BrokerGrantSpec{Authority: admission.StockProviderAuthority{PoolID: pool}, ExpiresAt: opt.Now.Add(5 * time.Second)}}
			want := opt.Now.Add(time.Minute)
			switch mode {
			case "unused":
				want = grant.Spec.ExpiresAt
			case "unrelated":
				grant.State = "unused"
				grant.Spec.Authority.PoolID = "other"
			case "already-expired":
				grant.State = "unused"
				grant.Spec.ExpiresAt = opt.Now.Add(-time.Second)
			case "equal-start":
				grant.State = "unused"
				grant.Spec.ExpiresAt = opt.Now
			case "later-than-host":
				grant.State = "unused"
				grant.Spec.ExpiresAt = opt.Now.Add(time.Hour)
			}
			state.Grants = []admission.BrokerGrant{grant}
			out := projectReadPlan(state, demand, capacitybroker.JobPlan{}, opt.Now, time.Minute)
			if !out.ExpiresAt.Equal(want) {
				t.Fatalf("got %v want %v", out.ExpiresAt, want)
			}
		})
	}
}
