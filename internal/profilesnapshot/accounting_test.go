package profilesnapshot

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func accountingFixture() admission.BrokerGrantState {
	unit := admission.Resources{CPU: 1000, RAM: 1024, Disk: 5, Runners: 1}
	two := admission.Resources{CPU: 2000, RAM: 2048, Disk: 10, Runners: 2}
	grant := func(id, state, key string) admission.BrokerGrant {
		return admission.BrokerGrant{ID: id, State: state, RequestKey: key, Spec: admission.BrokerGrantSpec{Host: "host", Authority: admission.StockProviderAuthority{PoolID: "pool"}, Resources: unit, ExpiresAt: time.Now().Add(-time.Hour)}}
	}
	return admission.BrokerGrantState{Sequence: 20, Epoch: 4, Ready: false, Config: admission.Config{Global: two, Hosts: map[string]admission.Host{"host": {Budget: two}}, Pools: map[string]admission.Pool{"pool": {Budget: two, Hosts: []string{"host"}}}}, PoolProfiles: map[string]admission.BrokerPoolProfile{}, HostBindings: map[string]admission.HostBinding{}, Allocated: admission.Allocations{Global: two, Hosts: map[string]admission.Resources{"host": two}, Pools: map[string]admission.Resources{"pool": two}}, Grants: []admission.BrokerGrant{grant("unused", "unused", ""), grant("consumed", "consumed", "unknown"), grant("released", "consumed", "released"), grant("revoked", "revoked", ""), grant("expired", "expired", "")}, Reservations: map[string]admission.Reservation{"unknown": {State: "unknown", Request: admission.Request{Key: "unknown", Pool: "pool", Host: "host", Resources: unit}}, "released": {State: "unknown", Request: admission.Request{Key: "released", Pool: "pool", Host: "host", Resources: unit}}}, Released: map[string]bool{"released": true}}
}
func accountingSnapshot(t *testing.T, state admission.BrokerGrantState) Snapshot {
	t.Helper()
	s, e := New("controller", strings.Repeat("a", 64), strings.Repeat("b", 64), state, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestAccountingKeepsExpiredUnusedAndUnknownCharges(t *testing.T) {
	s := accountingSnapshot(t, accountingFixture())
	a := s.Accounting
	if a.Sequence != 20 || a.ReconciliationEpoch != 4 || a.LedgerReady || a.UnusedGrantCounts["pool"] != 1 || a.ConsumedUnreleasedGrantCounts["pool"] != 1 || a.UnusedGrants.Global.Runners != 1 || a.Allocated.Global.Runners != 2 {
		t.Fatalf("bad projection %+v", a)
	}
	raw, _ := json.Marshal(s)
	if _, err := Decode(raw, s.Controller, s.Nonce, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Lowered budgets do not erase retained over-budget physical/unknown charges.
	state := accountingFixture()
	p := state.Config.Pools["pool"]
	p.Budget.Runners = 1
	state.Config.Pools["pool"] = p
	s = accountingSnapshot(t, state)
	if s.Accounting.Allocated.Pools["pool"].Runners != 2 {
		t.Fatal("overbudget charge disappeared")
	}
}
func TestAccountingChangesDoNotRevokeImmutableProfileDigest(t *testing.T) {
	state := accountingFixture()
	before := accountingSnapshot(t, state)
	state.Sequence++
	state.Grants[0].State = "expired"
	unit := state.Grants[0].Spec.Resources
	state.Allocated = admission.Allocations{Global: unit, Hosts: map[string]admission.Resources{"host": unit}, Pools: map[string]admission.Resources{"pool": unit}}
	after := accountingSnapshot(t, state)
	if before.ProfileDigest != after.ProfileDigest || before.AccountingDigest == after.AccountingDigest || after.Accounting.UnusedGrantCounts["pool"] != 0 {
		t.Fatal("static/dynamic snapshot digests confused")
	}
}
func TestAccountingRejectsRehashedInconsistentPayload(t *testing.T) {
	for _, mode := range []string{"revision", "negative", "unknown-pool", "double-count", "consumed-count", "budget-scope"} {
		t.Run(mode, func(t *testing.T) {
			s := accountingSnapshot(t, accountingFixture())
			switch mode {
			case "revision":
				s.Accounting.Sequence--
			case "negative":
				s.Accounting.Allocated.Global.CPU = -1
			case "unknown-pool":
				s.Accounting.Allocated.Pools["other"] = admission.Resources{}
			case "double-count":
				s.Accounting.UnusedGrantCounts["pool"] = 2
			case "consumed-count":
				s.Accounting.ConsumedUnreleasedGrantCounts["pool"] = 2
			case "budget-scope":
				delete(s.Accounting.Budgets.Pools, "pool")
			}
			s.AccountingDigest = AccountingDigest(s.Accounting)
			raw, _ := json.Marshal(s)
			if _, err := Decode(raw, s.Controller, s.Nonce, time.Now()); err == nil {
				t.Fatal("inconsistent accounting accepted")
			}
		})
	}
	state := accountingFixture()
	state.Grants[0].Spec.Resources.CPU = math.MaxInt64
	state.Grants = append(state.Grants, admission.BrokerGrant{ID: "overflow", State: "unused", Spec: state.Grants[0].Spec})
	if _, err := New("controller", strings.Repeat("a", 64), strings.Repeat("b", 64), state, time.Now()); err == nil {
		t.Fatal("overflow accepted")
	}
}
