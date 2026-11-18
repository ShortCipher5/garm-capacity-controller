//go:build linux

package nativeapi

import (
	"encoding/json"
	"testing"
)

func TestRetirementAdaptersRejectUnqualifiedInputs(t *testing.T) {
	var c *Client
	if _, e := c.StopForRetirement(t.Context(), Prepared{}, StoppedQuota{}, nil); e == nil {
		t.Fatal("zero stop")
	}
	if _, e := c.DeleteForRetirement(t.Context(), Prepared{}, RetirementOperation{}, nil); e == nil {
		t.Fatal("zero delete")
	}
	if _, e := c.ObserveRetirementOperation(t.Context(), Prepared{}, RetirementOperation{}); e == nil {
		t.Fatal("zero operation")
	}
	if _, e := c.ObserveDeletedInstance(t.Context(), Prepared{}, RetirementOperation{}); e == nil {
		t.Fatal("zero absence")
	}
	if (DeletedInstanceObservation{}).ReleaseAuthorized() {
		t.Fatal("absence released")
	}
}
func TestFixedGracefulRetirementStop(t *testing.T) {
	var v struct {
		Action          string
		Timeout         int
		Force, Stateful bool
	}
	if json.Unmarshal([]byte(RetirementStopBody), &v) != nil || v.Action != "stop" || v.Timeout != 30 || v.Force || v.Stateful {
		t.Fatal(v)
	}
}
func TestNativeAbsenceIsStrictAndNotPhysicalRelease(t *testing.T) {
	good := []byte(`{"type":"error","error_code":404,"error":"not found"}`)
	if !exactNativeAbsent(404, good) {
		t.Fatal("native404")
	}
	for _, code := range []int{200, 202, 403, 500} {
		if exactNativeAbsent(code, good) {
			t.Fatal(code)
		}
	}
	for _, raw := range []string{``, `{"type":"sync","error_code":404}`, `{"type":"error","error_code":403}`, `{"type":"error","error_code":404,"error_code":403}`} {
		if exactNativeAbsent(404, []byte(raw)) {
			t.Fatal(raw)
		}
	}
	if (DeletedInstanceObservation{valid: true}).ReleaseAuthorized() {
		t.Fatal("API-only observation authorized release")
	}
}
