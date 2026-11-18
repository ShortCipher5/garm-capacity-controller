package brokerinput

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"math"
	"strings"
	"testing"
	"time"
)

func withOverhead(s *admission.BrokerGrantState, r admission.Resources) {
	if r.RAM == 0 {
		r.RAM = 1
	}
	if r.Disk == 0 {
		r.Disk = 1
	}
	domain := strings.Repeat("a", 64)
	now := time.Now()
	evidence := nativeenforcement.Descriptor{Version: nativeenforcement.Version, Nonce: strings.Repeat("1", 64), IssuedAt: now, Owner: nativeidentity.Descriptor{DomainID: domain, PolicyDigest: strings.Repeat("5", 64), OwnerClientLeaf: strings.Repeat("6", 64), OwnerServerLeaf: strings.Repeat("7", 64), NativeServerKey: strings.Repeat("8", 64), NativeOrigin: "private-origin", NativeScope: "private-scope", NativeServerName: "private-native-domain", NativeServerVersion: "6.0.4"}, CapsuleBirthID: domain, RootImageSHA256: strings.Repeat("2", 64), DeviceProgramSHA256: strings.Repeat("3", 64), EgressPolicySHA256: strings.Repeat("4", 64), Limits: nativeenforcement.Limits{CPUMilli: uint64(r.CPU), MemoryBytes: uint64(r.RAM) << 20, DiskBytes: uint64(r.Disk) << 20, Tasks: 128}}
	evidence.LimitsDigest, _ = nativeenforcement.DigestLimits(evidence.Limits)
	s.DomainOverheads = []admission.NativeDomainOverhead{{Sequence: 2, DomainBindingSequence: 1, Host: "host", DomainID: domain, CapsuleBirthID: domain, RecordedAt: now, Evidence: evidence, Resources: r}}
	s.Allocated = admission.Allocations{DomainOverhead: r, Global: r, Hosts: map[string]admission.Resources{"host": r}, Pools: map[string]admission.Resources{}}
}
func TestDomainOverheadReducesOnlyGlobalHostCapacity(t *testing.T) {
	s, profiles, opt := ledgerFixture()
	r := admission.Resources{CPU: 3000, RAM: 2000, Disk: 1000}
	withOverhead(&s, r)
	snap, err := ledgerSnapshot(s, nil, profiles, opt)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Global[0] != 1000 || snap.Hosts["host"].Budget[0] != 1000 || snap.Global[3] != 4 || snap.Pools[pool].Budget[0] != 4000 || len(snap.Charges) != 0 || snap.Pools[pool].PendingWarm != 0 {
		t.Fatalf("overhead became pool/supply or was omitted: %+v", snap)
	}
	_, _, demand, _ := fixture()
	second := demand.Jobs[0]
	second.WorkflowJobID = 2
	demand.Jobs = append(demand.Jobs, second)
	plan, err := Stock(snap, s.Policy, demand, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Assignments) != 1 {
		t.Fatal("domain overhead was overscheduled", plan)
	}
	if s.Config.Global.CPU != 4000 || s.Allocated.Global.CPU != 3000 {
		t.Fatal("canonical state mutated")
	}
}
func TestDomainOverheadExhaustionAndInvalidProjectionFailClosed(t *testing.T) {
	for _, n := range []int64{4000, 5000} {
		s, p, o := ledgerFixture()
		withOverhead(&s, admission.Resources{CPU: n})
		snap, e := ledgerSnapshot(s, nil, p, o)
		if e != nil {
			t.Fatal(e)
		}
		_, _, d, _ := fixture()
		plan, e := Stock(snap, s.Policy, d, o)
		if e != nil || len(plan.Assignments) != 0 {
			t.Fatal("exhausted overhead allocated", plan, e)
		}
	}
	for _, mode := range []string{"missing", "duplicate", "negative", "runner", "wronghost", "subtotal", "hosttotal", "overflow", "sequence", "domain", "evidence", "bound"} {
		t.Run(mode, func(t *testing.T) {
			s, p, o := ledgerFixture()
			withOverhead(&s, admission.Resources{CPU: 1000})
			switch mode {
			case "sequence", "domain":
				v := s.DomainOverheads[0]
				v.DomainBindingSequence = 1
				if mode == "sequence" {
					s.DomainOverheads[0].Sequence = 3
					v.Sequence = 3
					v.DomainBindingSequence = 2
					v.DomainID = strings.Repeat("b", 64)
					v.CapsuleBirthID = v.DomainID
					v.Evidence.Owner.DomainID = v.DomainID
					v.Evidence.CapsuleBirthID = v.DomainID
				} else {
					v.DomainBindingSequence = 2
					v.Sequence = 3
				}
				s.Sequence = 3
				s.DomainOverheads = append(s.DomainOverheads, v)
			case "evidence":
				s.DomainOverheads[0].Evidence.LimitsDigest = ""
			case "bound":
				s.DomainOverheads = make([]admission.NativeDomainOverhead, 1001)
			case "missing":
				s.DomainOverheads = nil
			case "duplicate":
				s.DomainOverheads = append(s.DomainOverheads, s.DomainOverheads[0])
			case "negative":
				s.DomainOverheads[0].Resources.RAM = -1
			case "runner":
				s.DomainOverheads[0].Resources.Runners = 1
			case "wronghost":
				s.DomainOverheads[0].Host = "missing"
			case "subtotal":
				s.Allocated.DomainOverhead.CPU = 999
			case "hosttotal":
				s.Allocated.Hosts["host"] = admission.Resources{}
			case "overflow":
				s.DomainOverheads[0].Resources.CPU = math.MaxInt64
				v := s.DomainOverheads[0]
				v.Sequence = 3
				v.DomainBindingSequence = 2
				v.DomainID = strings.Repeat("b", 64)
				v.CapsuleBirthID = v.DomainID
				v.Evidence.Owner.DomainID = v.DomainID
				v.Evidence.CapsuleBirthID = v.DomainID
				s.DomainOverheads = append(s.DomainOverheads, v)
			}
			if _, e := ledgerSnapshot(s, nil, p, o); e == nil {
				t.Fatal("invalid overhead accepted")
			}
		})
	}
}

func TestDomainOverheadAndOrdinaryUnknownChargeAreNotDoubleCounted(t *testing.T) {
	s, profiles, opt := ledgerFixture()
	withOverhead(&s, admission.Resources{CPU: 2000, RAM: 2000, Disk: 2000})
	ordinary := profiles[pool].Size
	s.Allocated.Global = admission.Resources{CPU: 3000, RAM: 3000, Disk: 3000, Runners: 1}
	s.Allocated.Hosts["host"] = s.Allocated.Global
	s.Allocated.Pools[pool] = ordinary
	charges := []admission.BrokerPlanningCharge{{ID: "unknown-physical", Host: "host", Pool: pool, Resources: ordinary}}
	snap, e := ledgerSnapshot(s, charges, profiles, opt)
	if e != nil {
		t.Fatal(e)
	}
	_, _, d, opt := fixture()
	plan, e := Stock(snap, s.Policy, d, opt)
	if e != nil || len(plan.Assignments) != 1 || len(snap.Charges) != 1 || snap.Charges[0].Resources[0] != 1000 {
		t.Fatal("ordinary charge lost or overhead doublecounted", plan, e)
	}
}
func TestDomainOverheadRemainsLocalToHostAndGlobalNotPool(t *testing.T) {
	s, p, o := ledgerFixture()
	s.Config.Global = admission.Resources{CPU: 8000, RAM: 8000, Disk: 8000, Runners: 8}
	s.Config.Hosts["other"] = s.Config.Hosts["host"]
	poolConfig := s.Config.Pools[pool]
	poolConfig.Hosts = []string{"host", "other"}
	s.Config.Pools[pool] = poolConfig
	withOverhead(&s, admission.Resources{CPU: 4000, RAM: 4000, Disk: 4000})
	snap, e := ledgerSnapshot(s, nil, p, o)
	if e != nil {
		t.Fatal(e)
	}
	_, _, d, _ := fixture()
	plan, e := Stock(snap, s.Policy, d, o)
	if e != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Host != "other" || snap.Hosts["other"].Budget[0] != 4000 || snap.Pools[pool].Budget[0] != 4000 {
		t.Fatal("host overhead reduced unrelated pool or host", plan, e)
	}
}
