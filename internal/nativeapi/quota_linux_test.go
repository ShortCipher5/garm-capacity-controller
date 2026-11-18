//go:build linux

package nativeapi

import (
	"encoding/json"
	"testing"
)

func stoppedFixture(t *testing.T) (Prepared, map[string]any) {
	t.Helper()
	p, a := fixturePolicy(t)
	prepared, e := p.Compile(a, "data")
	if e != nil {
		t.Fatal(e)
	}
	var b map[string]any
	json.Unmarshal(prepared.Body(), &b)
	config := b["config"].(map[string]any)
	config["volatile.base_image"] = a.Fingerprint
	config["volatile.uuid"] = "12345678-1234-1234-1234-123456789abc"
	return prepared, map[string]any{"name": a.Name, "type": "container", "status": "Stopped", "status_code": 102, "profiles": []string{}, "config": config, "expanded_config": config, "devices": b["devices"], "expanded_devices": b["devices"]}
}
func encodeStopped(m map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"type": "sync", "status_code": 200, "metadata": m})
	return b
}
func TestStoppedInstanceMatchesClosedPayload(t *testing.T) {
	p, m := stoppedFixture(t)
	id, e := parseStopped(encodeStopped(m), p)
	if e != nil || id != "12345678-1234-1234-1234-123456789abc" {
		t.Fatal(id, e)
	}
	mutations := []func(map[string]any){func(m map[string]any) { m["status"] = "Running" }, func(m map[string]any) { m["status_code"] = 103 }, func(m map[string]any) { m["name"] = "other" }, func(m map[string]any) { m["type"] = "virtual-machine" }, func(m map[string]any) { m["profiles"] = []string{"default"} }, func(m map[string]any) { m["config"].(map[string]any)["raw.lxc"] = "bad" }, func(m map[string]any) { m["config"].(map[string]any)["limits.memory"] = "none" }, func(m map[string]any) { m["config"].(map[string]any)["volatile.uuid"] = "" }, func(m map[string]any) {
		m["devices"].(map[string]any)["host"] = map[string]any{"type": "disk", "source": "/"}
	}, func(m map[string]any) { m["expanded_config"] = map[string]string{} }}
	for i, mutate := range mutations {
		p, m := stoppedFixture(t)
		mutate(m)
		if _, e := parseStopped(encodeStopped(m), p); e == nil {
			t.Fatal("accepted", i)
		}
	}
}
func TestQuotaZeroProofAndNoNativeStateRefused(t *testing.T) {
	if (StoppedQuota{}).Valid() {
		t.Fatal("zero")
	}
	var c *Client
	if _, e := c.VerifyStoppedQuota(t.Context(), Prepared{}, nil); e == nil {
		t.Fatal("nil native")
	}
}
