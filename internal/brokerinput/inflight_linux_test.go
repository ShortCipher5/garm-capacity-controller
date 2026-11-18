package brokerinput

import (
	"reflect"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func pendingFixture(t *testing.T) (capacitybroker.Snapshot, admission.BrokerGrantState, garmapi.DemandObservation, capacitybroker.Options) {
	t.Helper()
	state, profiles, opt := ledgerFixture()
	req := admission.Request{Key: "request", Pool: pool, Host: "host", Resources: profiles[pool].Size}
	state.Reservations = map[string]admission.Reservation{"request": {Request: req, State: "creating"}}
	state.ExecutionPlans = map[string]admission.ExecutionPlan{"request": {Request: req, Execution: admission.ExecutionImage{Controller: "controller", Name: "runner"}}}
	state.Grants = []admission.BrokerGrant{{State: "consumed", RequestKey: "request", Spec: admission.BrokerGrantSpec{Authority: admission.StockProviderAuthority{Controller: "controller", PoolID: pool, ProviderName: "gated"}}}}
	charges := []admission.BrokerPlanningCharge{{ID: "reservation:request", Pool: pool, Host: "host", Resources: req.Resources}}
	snapshot, err := ledgerSnapshot(state, charges, profiles, opt)
	if err != nil {
		t.Fatal(err)
	}
	_, _, observation, _ := fixture()
	return snapshot, state, observation, opt
}

func TestInflightIdentityDedupAndRepeatedUnknownDeferral(t *testing.T) {
	for _, kind := range []string{"missing", "creating", "other", "idle", "busy"} {
		t.Run(kind, func(t *testing.T) {
			s, state, observation, opt := pendingFixture(t)
			r := state.Reservations["request"]
			r.State = "unknown"
			state.Reservations["request"] = r
			if kind != "missing" {
				observation.Runners = []garmapi.ObservedRunner{{RowID: "row", Name: "runner", ProviderName: "gated", PoolID: pool, Kind: kind}}
				switch kind {
				case "creating":
					observation.Supply[0].Creating = 1
				case "other":
					observation.Supply[0].Other = 1
				case "idle":
					observation.Supply[0].Idle = 1
				case "busy":
					observation.Supply[0].Busy = 1
				}
			}
			if err := inflight(&s, state, observation); err != nil {
				t.Fatal(err)
			}
			first, err := Stock(s, state.Policy, observation, opt)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Stock(s, state.Policy, observation, opt)
			if err != nil || !reflect.DeepEqual(first, second) {
				t.Fatal("repeated poll changed plan")
			}
			switch kind {
			case "idle":
				if len(first.CoveredByIdle) != 1 || len(first.DeferredByInflight) != 0 || len(first.Assignments) != 0 {
					t.Fatalf("double idle/startup: %+v", first)
				}
			case "busy":
				if len(first.DeferredByInflight) != 0 || len(first.CoveredByIdle) != 0 || len(first.Assignments) != 1 {
					t.Fatalf("busy treated as pending service: %+v", first)
				}
			default:
				if len(first.DeferredByInflight) != 1 || len(first.Assignments) != 0 || len(first.CoveredByIdle) != 0 || len(first.CoveredByGrant) != 0 {
					t.Fatalf("unknown presented satisfied/reissued: %+v", first)
				}
			}
			if len(s.Charges) != 1 || s.Charges[0].Resources[3] != 1 {
				t.Fatal("startup charge released")
			}
		})
	}
}
func TestInflightRejectsAmbiguousIdentityAndUnbackedStockCounts(t *testing.T) {
	for _, mode := range []string{"unidentified", "missing-plan", "wrong-name", "wrong-provider", "wrong-pool", "counts-only", "duplicate-row"} {
		t.Run(mode, func(t *testing.T) {
			s, state, o, _ := pendingFixture(t)
			switch mode {
			case "unidentified":
				state.Grants = nil
			case "missing-plan":
				state.ExecutionPlans = nil
			case "counts-only":
				o.Supply[0].Creating = 1
			default:
				row := garmapi.ObservedRunner{RowID: "row", Name: "runner", ProviderName: "gated", PoolID: pool, Kind: "creating"}
				switch mode {
				case "wrong-name":
					row.Name = "different"
				case "wrong-provider":
					row.ProviderName = "different"
				case "wrong-pool":
					row.PoolID = "different"
				}
				o.Runners = []garmapi.ObservedRunner{row}
				o.Supply[0].Creating = 1
				if mode == "duplicate-row" {
					o.Runners = append(o.Runners, row)
					o.Supply[0].Creating = 2
				}
			}
			if err := inflight(&s, state, o); err == nil {
				t.Fatal("ambiguous identity accepted")
			}
		})
	}
}
