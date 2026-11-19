package brokerinput

import (
	"reflect"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

func TestAdvisoryDisabledPoolUsesOnlyExistingIdle(t *testing.T) {
	s, p, d, o := fixture()
	d.Pools[0].Enabled = false
	d.Jobs[0].CandidatePoolIDs = nil
	d.Jobs[0].IdleOnlyPoolIDs = []string{pool}
	s.Charges = []capacitybroker.Charge{{ID: "idle", Host: "host", Pool: pool, Resources: s.Pools[pool].Size}}
	d.Supply[0].Idle = 1
	plan, err := advisoryStock(s, p, d, o, nil)
	if err != nil || len(plan.CoveredByIdle) != 1 || len(plan.Assignments) != 0 || len(plan.RemainingJobs) != 0 {
		t.Fatalf("disabled existing idle lost: %+v %v", plan, err)
	}
}

func TestDisabledIdleNeverEnablesGrantInflightWarmOrCreation(t *testing.T) {
	for _, kind := range []string{"idle", "grant", "inflight", "busy", "unknown", "removed"} {
		t.Run(kind, func(t *testing.T) {
			s, p, d, o := fixture()
			d.Pools[0].Enabled = false
			d.Jobs[0].CandidatePoolIDs = nil
			d.Jobs[0].IdleOnlyPoolIDs = []string{pool}
			size := s.Pools[pool].Size
			s.Charges = []capacitybroker.Charge{{ID: "retained", Host: "host", Pool: pool, Resources: size}}
			candidate := s.Pools[pool]
			switch kind {
			case "idle":
				d.Supply[0].Idle = 1
			case "grant":
				candidate.OutstandingGrants = 1
				candidate.PendingWarm = 1
			case "inflight":
				candidate.Inflight = 1
				candidate.PendingWarm = 1
				d.Supply[0].Creating = 1
			case "busy":
				d.Supply[0].Busy = 1
			case "unknown":
				d.Supply[0].Other = 1
			case "removed":
				d.Jobs[0].IdleOnlyPoolIDs = nil
			}
			s.Pools[pool] = candidate
			p.Repositories[0].Pools[0].WarmIdle = 1
			plan, err := advisoryStock(s, p, d, o, map[string]bool{"host": true})
			if err != nil || len(plan.Assignments) != 0 || len(plan.CoveredByGrant) != 0 || len(plan.DeferredByInflight) != 0 {
				t.Fatalf("disabled pool became eligible: %+v %v", plan, err)
			}
			expected := 0
			if kind == "idle" {
				expected = 1
			}
			if len(plan.CoveredByIdle) != expected || len(plan.RemainingJobs) != 1-expected {
				t.Fatal(plan)
			}
			if len(s.Charges) != 1 || s.Charges[0].Resources != size || !reflect.DeepEqual(s.Pools[pool], candidate) {
				t.Fatal("input debt mutated")
			}
			original, err := Stock(s, p, d, o)
			if err != nil || len(original.CoveredByIdle) != 0 || len(original.CoveredByGrant) != 0 || len(original.DeferredByInflight) != 0 || len(original.Assignments) != 0 || len(original.RemainingJobs) != 1 {
				t.Fatalf("grant-path semantics changed: %+v %v", original, err)
			}
		})
	}
}
