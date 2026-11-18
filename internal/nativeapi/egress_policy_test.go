package nativeapi

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"net/http"
	"strings"
	"testing"
)

func egressFixture(t *testing.T) (Policy, nativeegress.Policy) {
	t.Helper()
	n, e := nativeegress.Compile(nativeegress.Specification{Mode: nativeegress.PublicInternet, TransitCIDR: "10.251.0.0/30", RunnerCIDR: "10.252.0.0/24", DNS: []string{"1.1.1.1"}, ControlPlaneCIDRs: []string{"8.8.8.8/32"}})
	if e != nil {
		t.Fatal(e)
	}
	base := resourcePolicy(t, nil)
	s := base.spec
	s.Version = EgressPolicyVersion
	s.EgressDigest = n.Digest()
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("4", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	v := s.Pools["pool"]
	v.ProtectedImage = &image
	s.Pools["pool"] = v
	p, e := NewPolicy(s)
	if e != nil {
		t.Fatal(e)
	}
	return p, n
}
func TestExplicitEgressPolicyPinsProtectedBootAndBridge(t *testing.T) {
	p, n := egressFixture(t)
	if !p.Protected() {
		t.Fatal("lost protected")
	}
	if _, ok := p.ProtectedImage("pool"); !ok {
		t.Fatal("image")
	}
	if _, e := p.PlanFreshResources("tank/owned"); e == nil {
		t.Fatal("silent isolated fallback")
	}
	plan, e := p.planFreshResources("tank/owned", &n)
	if e != nil || !plan.uplink {
		t.Fatal(e)
	}
	var b struct{ Config map[string]string }
	json.Unmarshal([]byte(plan.requests[1].Body), &b)
	for k, v := range map[string]string{"ipv4.address": "10.252.0.1/24", "ipv4.nat": "true", "ipv4.nat.address": "10.251.0.2", "dns.nameservers": "1.1.1.1", "ipv4.dhcp": "true", "ipv6.address": "none"} {
		if b.Config[k] != v {
			t.Fatal(k, b.Config)
		}
	}
	if _, ok := b.Config["raw.dnsmasq"]; ok {
		t.Fatal("AppArmor bypass")
	}
	s := p.spec
	s.EgressDigest = ""
	if _, e = NewPolicy(s); e == nil {
		t.Fatal("unbound v4")
	}
	s = p.spec
	s.Version = ProtectedPolicyVersion
	if _, e = NewPolicy(s); e == nil {
		t.Fatal("silent v3 upgrade")
	}
}
func TestEgressFreshResourcesNeedActualExpectedUplink(t *testing.T) {
	p, n := egressFixture(t)
	plan, _ := p.planFreshResources("tank/owned", &n)
	for _, uplink := range []bool{false, true} {
		f := &resourceFixture{objects: map[string]map[string]any{}, uplink: uplink}
		e := resourceBootstrap(context.Background(), plan, &http.Client{Transport: f}, func(context.Context) error { return nil }, func(string) error { f.consumed = true; return nil }, func(string) error { f.completed = true; return nil })
		if uplink && (e != nil || !f.completed || f.posts != 2) {
			t.Fatal("with uplink", e)
		}
		if !uplink && (e == nil || f.posts != 0) {
			t.Fatal("missing uplink accepted")
		}
	}
}

func TestPartitionPolicyRetainsExactFreshEgress(t *testing.T) {
	legacy, network := egressFixture(t)
	spec := legacy.spec
	spec.Version = PartitionPolicyVersion
	policy, err := NewPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.PlanFreshResources("tank/owned"); err == nil {
		t.Fatal("isolated fallback")
	}
	if _, err := policy.planFreshResources("tank/owned", &network); err != nil {
		t.Fatal(err)
	}
	spec.EgressDigest = strings.Repeat("a", 64)
	changed, err := NewPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changed.planFreshResources("tank/owned", &network); err == nil {
		t.Fatal("foreign egress accepted")
	}
}
