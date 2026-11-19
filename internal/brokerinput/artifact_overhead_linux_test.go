package brokerinput

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"math"
	"strings"
	"testing"
)

func withArtifact(s *admission.BrokerGrantState, disk int64) {
	v := s.DomainOverheads[0]
	a := admission.NativeArtifactOverhead{ArtifactID: strings.Repeat("9", 64), Nonce: strings.Repeat("a", 64), IssuedAt: v.RecordedAt, Sequence: 3, DomainBindingSequence: 1, Host: "host", Owner: v.Evidence.Owner, PoolGUID: "101", PoolLoadGUID: "102", RootGUID: "103", Dataset: "pool/control-" + strings.Repeat("9", 64), QuotaBytes: uint64(disk) << 20, Resources: admission.Resources{Disk: disk}, ObservedAt: v.RecordedAt, RecordedAt: v.RecordedAt}
	s.ArtifactOverheads = []admission.NativeArtifactOverhead{a}
	s.Allocated.ArtifactOverhead = a.Resources
	s.Allocated.Global.Disk += disk
	h := s.Allocated.Hosts["host"]
	h.Disk += disk
	s.Allocated.Hosts["host"] = h
}
func TestArtifactOverheadCombinesWithoutPoolOrRunnerCharge(t *testing.T) {
	s, p, o := ledgerFixture()
	withOverhead(&s, admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000})
	withArtifact(&s, 3000)
	snapshot, e := ledgerSnapshot(s, nil, p, o)
	if e != nil {
		t.Fatal(e)
	}
	if snapshot.Global[2] != 0 || snapshot.Hosts["host"].Budget[2] != 0 || snapshot.Pools[pool].Budget[2] != 4000 || snapshot.Global[3] != 4 || len(snapshot.Charges) != 0 {
		t.Fatal(snapshot)
	}
	_, _, d, _ := fixture()
	plan, e := Stock(snapshot, s.Policy, d, o)
	if e != nil || len(plan.Assignments) != 0 {
		t.Fatal("artifact disk overscheduled", plan, e)
	}
}
func TestArtifactOverheadRejectsInvalidProjection(t *testing.T) {
	for _, mode := range []string{"subtotal", "sequence", "domain-sequence", "artifact-id", "root", "host", "evidence", "bound", "runner", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			s, p, o := ledgerFixture()
			withOverhead(&s, admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000})
			withArtifact(&s, 1000)
			switch mode {
			case "subtotal":
				s.Allocated.ArtifactOverhead.Disk--
			case "domain-sequence":
				s.ArtifactOverheads[0].Sequence = s.DomainOverheads[0].Sequence
			case "sequence", "artifact-id", "root":
				v := s.ArtifactOverheads[0]
				v.Sequence = 4
				if mode != "artifact-id" {
					v.ArtifactID = strings.Repeat("b", 64)
					v.Dataset = "pool/control-" + v.ArtifactID
				}
				if mode != "root" {
					v.RootGUID = "104"
				}
				if mode == "sequence" {
					v.Sequence = s.ArtifactOverheads[0].Sequence
				}
				s.ArtifactOverheads = append(s.ArtifactOverheads, v)
			case "host":
				s.ArtifactOverheads[0].Host = "missing"
			case "evidence":
				s.ArtifactOverheads[0].Nonce = ""
			case "bound":
				s.ArtifactOverheads = make([]admission.NativeArtifactOverhead, 1001)
			case "overflow":
				s.ArtifactOverheads[0].Resources.Disk = math.MaxInt64
			case "runner":
				s.ArtifactOverheads[0].Resources.Runners = 1
			}
			if _, e := ledgerSnapshot(s, nil, p, o); e == nil {
				t.Fatal("invalid artifacts accepted")
			}
		})
	}
}

func TestArtifactOverheadRetainsOrdinaryUnknownCharge(t *testing.T) {
	s, p, o := ledgerFixture()
	withOverhead(&s, admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000})
	withArtifact(&s, 1000)
	size := p[pool].Size
	s.Allocated.Global.CPU += size.CPU
	s.Allocated.Global.RAM += size.RAM
	s.Allocated.Global.Disk += size.Disk
	s.Allocated.Global.Runners++
	s.Allocated.Hosts["host"] = s.Allocated.Global
	s.Allocated.Pools[pool] = size
	snapshot, e := ledgerSnapshot(s, []admission.BrokerPlanningCharge{{ID: "unknown", Host: "host", Pool: pool, Resources: size}}, p, o)
	if e != nil {
		t.Fatal(e)
	}
	_, _, d, _ := fixture()
	plan, e := Stock(snapshot, s.Policy, d, o)
	if e != nil || len(snapshot.Charges) != 1 || len(plan.Assignments) != 1 {
		t.Fatal("ordinary debt lost or overhead duplicated", plan, e)
	}
}
