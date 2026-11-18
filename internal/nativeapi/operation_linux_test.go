//go:build linux

package nativeapi

import (
	"encoding/json"
	"testing"
)

func operationFixture() map[string]any {
	return map[string]any{"id": "12345678-1234-1234-1234-123456789abc", "class": "task", "resources": map[string]any{"instances": []string{"/1.0/instances/garm-native-1"}}, "status_code": 103, "err": ""}
}
func TestOperationRequiresExactUUIDAndResource(t *testing.T) {
	p, _ := fixturePolicy(t)
	encode := func(m map[string]any) []byte { b, _ := json.Marshal(m); return b }
	op, e := parseOperation(encode(operationFixture()), p.DomainID(), p.Digest(), "garm-native-1")
	if e != nil || !op.Valid() || op.Outcome() != "accepted" {
		t.Fatal(op, e)
	}
	for i, mutate := range []func(map[string]any){func(m map[string]any) { m["id"] = "" }, func(m map[string]any) { m["class"] = "websocket" }, func(m map[string]any) { m["resources"] = map[string]any{"instances": []string{"/1.0/instances/other"}} }, func(m map[string]any) {
		m["resources"] = map[string]any{"instances": []string{"/1.0/instances/garm-native-1", "/1.0/instances/other"}}
	}, func(m map[string]any) { m["status_code"] = 0 }, func(m map[string]any) { m["err"] = "failed" }} {
		m := operationFixture()
		mutate(m)
		if _, e := parseOperation(encode(m), p.DomainID(), p.Digest(), "garm-native-1"); e == nil {
			t.Fatal("accepted", i)
		}
	}
	for code, want := range map[int]string{100: "accepted", 105: "accepted", 200: "succeeded", 400: "failed", 401: "failed"} {
		m := operationFixture()
		m["status_code"] = code
		o, e := parseOperation(encode(m), p.DomainID(), p.Digest(), "garm-native-1")
		if e != nil || o.Outcome() != want {
			t.Fatal(code, e)
		}
	}
}
func TestZeroNativeMutationInputsNeverSubmit(t *testing.T) {
	var c *Client
	if _, e := c.CreateStopped(t.Context(), Prepared{}); e == nil {
		t.Fatal("zero create")
	}
	if _, e := c.Start(t.Context(), Prepared{}, StoppedQuota{}); e == nil {
		t.Fatal("zero start")
	}
	if _, e := c.ObserveOperation(t.Context(), Operation{}); e == nil {
		t.Fatal("zero observation")
	}
}
