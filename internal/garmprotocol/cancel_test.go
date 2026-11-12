package garmprotocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCancellationExactProtocol(t *testing.T) {
	no := false
	req := testRequest()
	base := CancellationResponse{Protocol: Version, Outcome: "cancelled", Name: req.Name, PoolID: req.PoolID, Dispatched: &no, ReservationKey: "key-1", Replay: &no}
	data, err := EncodeCancellation(base, req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCancellation(0, data, req)
	if err != nil || *got.Replay {
		t.Fatalf("roundtrip %+v %v", got, err)
	}
	for _, exit := range []int{1, 75, -1} {
		if _, err := ParseCancellation(exit, data, req); err == nil {
			t.Fatal("wrong exit accepted")
		}
	}
	for _, mutate := range []func(*CancellationResponse){func(r *CancellationResponse) { r.Protocol = "other" }, func(r *CancellationResponse) { r.Outcome = "reserved" }, func(r *CancellationResponse) { r.Name = "other" }, func(r *CancellationResponse) { r.PoolID = "other" }, func(r *CancellationResponse) { r.ReservationKey = "" }, func(r *CancellationResponse) { r.Dispatched = nil }, func(r *CancellationResponse) { yes := true; r.Dispatched = &yes }, func(r *CancellationResponse) { r.Replay = nil }} {
		r := base
		mutate(&r)
		b, _ := json.Marshal(r)
		if _, err := ParseCancellation(0, b, req); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	raw := strings.TrimSpace(string(data))
	for _, bad := range []string{"null", raw + " {}", strings.Replace(raw, `"replay":false`, `"replay":false,"replay":true`, 1), strings.Replace(raw, `"replay":false`, `"Replay":false`, 1), strings.Replace(raw, `"replay":false`, `"replay":null`, 1), strings.Replace(raw, `"replay":false`, `"secret":"private-must-not-echo"`, 1), strings.Repeat("x", MaximumResponseBytes+1)} {
		if _, err := ParseCancellation(0, []byte(bad), req); err == nil || strings.Contains(err.Error(), "private-must-not-echo") {
			t.Fatal("ambiguous response accepted or leaked")
		}
	}
}
