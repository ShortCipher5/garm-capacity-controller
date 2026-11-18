package profilesnapshot

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
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

func overheadFixture() admission.BrokerGrantState {
	s := accountingFixture()
	r := admission.Resources{CPU: 500, RAM: 512, Disk: 10}
	limits := nativeenforcement.Limits{CPUMilli: 500, MemoryBytes: 512 << 20, DiskBytes: 10 << 20, Tasks: 64}
	digest, _ := nativeenforcement.DigestLimits(limits)
	d := nativeenforcement.Descriptor{Version: nativeenforcement.Version, Nonce: strings.Repeat("a", 64), IssuedAt: time.Now().Add(-time.Hour), CapsuleBirthID: strings.Repeat("d", 64), RootImageSHA256: strings.Repeat("1", 64), DeviceProgramSHA256: strings.Repeat("2", 64), EgressPolicySHA256: strings.Repeat("3", 64), Owner: nativeidentity.Descriptor{PolicyDigest: strings.Repeat("4", 64), NativeServerKey: strings.Repeat("5", 64), NativeOrigin: "https://native.invalid", NativeScope: "domain", NativeServerName: "native.invalid", NativeServerVersion: "v1", DomainID: strings.Repeat("d", 64), OwnerClientLeaf: strings.Repeat("b", 64), OwnerServerLeaf: strings.Repeat("c", 64)}, Limits: limits, LimitsDigest: digest}
	s.DomainOverheads = []admission.NativeDomainOverhead{{Sequence: 10, DomainBindingSequence: 8, Host: "host", DomainID: d.Owner.DomainID, CapsuleBirthID: d.CapsuleBirthID, Resources: r, Evidence: d, RecordedAt: time.Now().Add(-time.Hour)}}
	s.Allocated.DomainOverhead = r
	s.Allocated.Global, _ = addResources(s.Allocated.Global, r)
	s.Allocated.Hosts["host"], _ = addResources(s.Allocated.Hosts["host"], r)
	return s
}
func TestDomainOverheadRemainsExplicitGlobalHostCharge(t *testing.T) {
	s := overheadFixture()
	snap := accountingSnapshot(t, s)
	if snap.Accounting.Allocated.Pools["pool"] != accountingFixture().Allocated.Pools["pool"] || snap.Accounting.UnusedGrants.DomainOverhead != (admission.Resources{}) || snap.Accounting.Allocated.DomainOverhead != s.DomainOverheads[0].Resources {
		t.Fatal("overhead became pool charge or runner supply")
	}
	raw, _ := json.Marshal(snap)
	if _, err := Decode(raw, snap.Controller, snap.Nonce, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "duplicate-binding", "duplicate-sequence", "duplicate", "wrong-host", "wrong-subtotal", "wrong-limit-digest", "runner-supply"} {
		t.Run(mode, func(t *testing.T) {
			s := overheadFixture()
			switch mode {
			case "missing":
				s.DomainOverheads = nil
			case "duplicate-sequence", "duplicate-binding":
				second := s.DomainOverheads[0]
				if mode == "duplicate-binding" {
					second.Sequence++
				}
				second.DomainID = strings.Repeat("e", 64)
				second.CapsuleBirthID = second.DomainID
				second.Evidence.Owner.DomainID = second.DomainID
				second.Evidence.CapsuleBirthID = second.DomainID
				s.DomainOverheads = append(s.DomainOverheads, second)
				s.Allocated.DomainOverhead, _ = addResources(s.Allocated.DomainOverhead, second.Resources)
				s.Allocated.Global, _ = addResources(s.Allocated.Global, second.Resources)
				s.Allocated.Hosts["host"], _ = addResources(s.Allocated.Hosts["host"], second.Resources)
			case "duplicate":
				s.DomainOverheads = append(s.DomainOverheads, s.DomainOverheads[0])
			case "wrong-host":
				s.DomainOverheads[0].Host = "unknown"
			case "wrong-subtotal":
				s.Allocated.DomainOverhead.RAM++
			case "wrong-limit-digest":
				s.DomainOverheads[0].Evidence.Limits.MemoryBytes++
			case "runner-supply":
				s.DomainOverheads[0].Resources.Runners = 1
			}
			if _, err := projectAccounting(s); err == nil {
				t.Fatal("inconsistent overhead accepted")
			}
		})
	}
}
