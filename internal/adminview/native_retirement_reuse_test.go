package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"reflect"
	"testing"
	"time"
)

func TestRetirementReuseExplanationUsesOnlyLastCycleDiagnostics(t *testing.T) {
	status := nativecontroller.RetirementStatus{At: time.Unix(100, 0), Incomplete: true, Result: nativecontroller.RetirementCycle{Examined: 6, AwaitingEvidence: 1, OutsideScope: 1, Unresolved: 1, OperationsCompleted: 3, RetainedDiagnosticsAvailable: 1, RetainedDiagnosticsUnavailable: 1}}
	before := status
	v := explainRetirementReuse(status, "within_cycle_window")
	want := []string{"cycle_incomplete", "canonical_root_fence_missing", "outside_configured_owner_scope", "operation_outcome_unresolved", "retained_diagnostics_unavailable", "retained_diagnostics_are_not_release_proof", "retained_diagnostics_not_recorded", "physical_capacity_reuse_not_established"}
	if v == nil || v.Availability != "last_cycle_diagnostics" || v.ReuseAuthorized || !reflect.DeepEqual(v.Reasons, want) || status != before {
		t.Fatal(v)
	}
	// A fully successful protocol and retained observation still cannot refund.
	status.Incomplete = false
	status.Result = nativecontroller.RetirementCycle{Examined: 1, OperationsCompleted: 1, RetainedDiagnosticsAvailable: 1}
	v = explainRetirementReuse(status, "within_cycle_window")
	if v == nil || v.ReuseAuthorized || !reflect.DeepEqual(v.Reasons, []string{"retained_diagnostics_are_not_release_proof", "physical_capacity_reuse_not_established"}) {
		t.Fatal(v)
	}
}
func TestRetirementReuseHistoricalAndUnsupported(t *testing.T) {
	status := nativecontroller.RetirementStatus{At: time.Unix(100, 0), Result: nativecontroller.RetirementCycle{Examined: 1, Unresolved: 1}}
	for _, f := range []string{"stale", "clock_invalid"} {
		v := explainRetirementReuse(status, f)
		if v == nil || v.Availability != "historical_only" || v.ReuseAuthorized || len(v.Reasons) != 1 {
			t.Fatal(v)
		}
	}
	if explainRetirementReuse(nativecontroller.RetirementStatus{}, "no_cycle") != nil || explainRetirementReuse(status, "unsupported") != nil {
		t.Fatal("unsupported cycle explained")
	}
	for _, mutate := range []func(*nativecontroller.RetirementStatus){
		func(s *nativecontroller.RetirementStatus) { s.Result.ReleaseAuthorized = true },
		func(s *nativecontroller.RetirementStatus) { s.Result.Examined = 65 },
		func(s *nativecontroller.RetirementStatus) { s.Result.RetainedDiagnosticsAvailable = 1 },
		func(s *nativecontroller.RetirementStatus) { s.Result.Unresolved = -1 },
	} {
		bad := status
		mutate(&bad)
		if explainRetirementReuse(bad, "within_cycle_window") != nil {
			t.Fatal("invalid aggregate accepted")
		}
	}
}
func TestRetirementReuseAbsentWithoutConfiguredCycle(t *testing.T) {
	h, _, token := fixture(t)
	for _, tc := range []struct {
		method, token string
		code          int
	}{{"GET", "", 401}, {"POST", token, 405}, {"GET", token, 200}} {
		w := request(t, h, tc.method, "/admin/api/v1/native-retirement-status", tc.token)
		if w.Code != tc.code {
			t.Fatal(w.Code)
		}
		if w.Code == 200 {
			var body map[string]json.RawMessage
			if json.Unmarshal(w.Body.Bytes(), &body) != nil {
				t.Fatal("invalid response")
			}
			if _, exists := body["capacity_reuse"]; exists {
				t.Fatal("invented cycle evidence")
			}
		}
	}
}
