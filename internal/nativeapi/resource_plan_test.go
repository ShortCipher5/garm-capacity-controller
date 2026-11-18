package nativeapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func resourcePolicy(t *testing.T, change func(*PolicySpec)) Policy {
	t.Helper()
	p, _ := fixturePolicy(t)
	s := p.spec
	v := s.Pools["pool"]
	v.ZFSDataset = "tank/owned/pool"
	s.Pools["pool"] = v
	if change != nil {
		change(&s)
	}
	p, e := NewPolicy(s)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestResourcePlanDeterministicDetachedAndNoEffects(t *testing.T) {
	p := resourcePolicy(t, nil)
	a, e := p.PlanFreshResources("tank/owned")
	if e != nil {
		t.Fatal(e)
	}
	b, e := p.PlanFreshResources("tank/owned")
	if e != nil || a.Digest() != b.Digest() || !hash(a.Digest()) {
		t.Fatal("unstable plan")
	}
	req := a.Requests()
	if len(req) != 2 || req[0].Path != "/1.0/storage-pools" || req[1].Path != "/1.0/networks" {
		t.Fatal(req)
	}
	for _, r := range req {
		if r.Method != "POST" || !json.Valid([]byte(r.Body)) {
			t.Fatal(r)
		}
	}
	if strings.Contains(req[0].Body, "wipe") || strings.Contains(req[0].Body, "size") || !strings.Contains(req[0].Body, `"source":"tank/owned/pool"`) {
		t.Fatal(req[0])
	}
	var network struct {
		Config map[string]string `json:"config"`
	}
	json.Unmarshal([]byte(req[1].Body), &network)
	if network.Config["ipv4.address"] != "none" || network.Config["ipv6.address"] != "none" || network.Config["ipv4.nat"] != "false" || network.Config["dns.mode"] != "none" || len(network.Config) != 6 {
		t.Fatal(network)
	}
	req[0].Body = "unsafe"
	if a.Requests()[0].Body == "unsafe" {
		t.Fatal("mutable plan")
	}
	if (ResourcePlan{}).Digest() != "" || len((ResourcePlan{}).Requests()) != 0 {
		t.Fatal("zero plan")
	}
}
func TestResourcePlanRejectsParentEscapeAndAmbiguousOwnership(t *testing.T) {
	for _, dataset := range []string{"tank/owned", "tank/owned-other/pool", "tank/owned/../pool", "tank/owned/pool/child", "/dev/sda", "tank/owned/pool@snapshot", "tank/owned/pool,other", "tank/owned/runtime"} {
		p := resourcePolicy(t, func(s *PolicySpec) { v := s.Pools["pool"]; v.ZFSDataset = dataset; s.Pools["pool"] = v })
		if _, e := p.PlanFreshResources("tank/owned"); e == nil {
			t.Fatal(dataset)
		}
	}
	for _, change := range []func(*PolicySpec){func(s *PolicySpec) { v := s.Pools["pool"]; v.ZFSDataset = "tank/owned/other"; s.Pools["other"] = v }, func(s *PolicySpec) { v := s.Pools["pool"]; v.StoragePool = "other"; s.Pools["other"] = v }, func(s *PolicySpec) { v := s.Pools["pool"]; v.Bridge = strings.Repeat("b", 16); s.Pools["pool"] = v }} {
		if _, e := resourcePolicy(t, change).PlanFreshResources("tank/owned"); e == nil {
			t.Fatal("ambiguous plan")
		}
	}
	if _, e := (Policy{}).PlanFreshResources("tank/owned"); e == nil {
		t.Fatal("zero policy")
	}
}
