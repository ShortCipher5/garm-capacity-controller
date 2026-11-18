package nativeegress

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func fixture(t *testing.T) Policy {
	t.Helper()
	p, e := Compile(Specification{Mode: PublicInternet, TransitCIDR: "10.251.0.0/30", RunnerCIDR: "10.252.0.0/24", DNS: []string{"1.1.1.1"}, ControlPlaneCIDRs: []string{"8.8.8.8/32"}})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPublicModeStillDenyHostPrivateAndControl(t *testing.T) {
	p := fixture(t)
	id := strings.Repeat("a", 64)
	fw, e := p.firewall(id)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(object{"nftables": fw})
	if !sameFirewall(raw, fw) {
		t.Fatal("self")
	}
	for _, v := range []string{"host_input", "host_output", "inbound", "outbound", "masquerade", "8.8.8.8", "10.0.0.0", "169.254.0.0", "ipv4"} {
		if !bytes.Contains(raw, []byte(v)) {
			t.Fatal(v)
		}
	}
	if bytes.Contains(raw, []byte("flush")) {
		t.Fatal("global mutation")
	}
	fw[1]["chain"].(map[string]any)["policy"] = "drop"
	if sameFirewall(raw, fw) {
		t.Fatal("drift")
	}
}
func TestPolicyRejectsAmbiguityAndUnsafeDestinations(t *testing.T) {
	for _, mut := range []func(*Specification){func(s *Specification) { s.Mode = "anything" }, func(s *Specification) { s.TransitCIDR = "10.251.0.1/30" }, func(s *Specification) { s.RunnerCIDR = "10.251.0.0/24" }, func(s *Specification) { s.DNS = []string{"127.0.0.1"} }, func(s *Specification) { s.DNS = []string{"::1"} }, func(s *Specification) { s.ControlPlaneCIDRs = nil }, func(s *Specification) { s.DNS = []string{"8.8.8.8"} }, func(s *Specification) { s.Mode = Restricted; s.Rules = []Rule{{"0.0.0.0/0", "tcp", 443}} }, func(s *Specification) { s.Mode = Restricted; s.Rules = []Rule{{"10.0.0.0/8", "tcp", 443}} }, func(s *Specification) { s.Mode = Restricted; s.Rules = []Rule{{"8.8.8.8/32", "tcp", 443}} }, func(s *Specification) { s.Mode = Restricted; s.Rules = []Rule{{"9.9.9.0/24", "tcp;drop", 443}} }} {
		s := fixture(t).spec
		mut(&s)
		if _, e := Compile(s); e == nil {
			t.Fatal("unsafe accepted", s)
		}
	}
}
func TestRestrictedPolicyDetachedAndExact(t *testing.T) {
	s := fixture(t).spec
	s.Mode = Restricted
	s.Rules = []Rule{{"9.9.9.0/24", "tcp", 443}}
	p, e := Compile(s)
	if e != nil {
		t.Fatal(e)
	}
	s.Rules[0].CIDR = "0.0.0.0/0"
	if p.spec.Rules[0].CIDR != "9.9.9.0/24" {
		t.Fatal("aliased")
	}
	a := p.DNS()
	a[0] = "127.0.0.1"
	if p.DNS()[0] != "1.1.1.1" {
		t.Fatal("dns alias")
	}
}
func TestNoForeignFlowtableOrInterfaceAdoption(t *testing.T) {
	if rulesetSafe([]byte(`{"nftables":[{"flowtable":{}}]}`), "owned") || rulesetSafe([]byte(`{"nftables":[{"table":{"name":"owned"}}]}`), "owned") {
		t.Fatal("foreign")
	}
	if !rulesetSafe([]byte(`{"nftables":[]}`), "owned") {
		t.Fatal("empty")
	}
	if !onlyLoopback([]byte(`[{"ifname":"lo"}]`)) || onlyLoopback([]byte(`[{"ifname":"lo"},{"ifname":"eth0"}]`)) {
		t.Fatal("guest")
	}
	if hostAddressesSafe([]byte(`[{"ifname":"eth0","addr_info":[{"family":"inet","local":"10.251.0.1","prefixlen":30}]}]`), fixture(t), "gcabc") {
		t.Fatal("overlap")
	}
}
func TestVethMessageUsesNamespaceFDNotPID(t *testing.T) {
	b := vethMessage("gcaaaaaaaaaaaa", 73)
	if binary.NativeEndian.Uint16(b[4:]) != 16 || !bytes.Contains(b, []byte("garm0\x00")) || !bytes.Contains(b, attr(28, u32(73))) {
		t.Fatal("namespace descriptor missing")
	}
	if bytes.Contains(b, []byte("/proc")) {
		t.Fatal("path lookup")
	}
}
func TestRejectVethEnslavedToHostOrGuestBridge(t *testing.T) {
	for _, master := range []string{`"br0"`, `0`, `null`} {
		raw := []byte(`[{"ifindex":10,"link_index":20,"ifname":"garm0","master":` + master + `,"flags":["UP"],"linkinfo":{"info_kind":"veth"},"addr_info":[{"family":"inet","local":"10.251.0.2","prefixlen":30}]}]`)
		if _, e := observeLink(raw, "garm0", "10.251.0.2"); e == nil {
			t.Fatal("bridge membership")
		}
	}
}
func TestHostOutputRelatedICMPAfterConntrack(t *testing.T) {
	fw, _ := fixture(t).firewall(strings.Repeat("a", 64))
	found := false
	for _, o := range fw {
		c, ok := o["chain"].(map[string]any)
		if ok && c["name"] == "host_output" {
			found = true
			if c["prio"].(int) <= -200 {
				t.Fatal("before conntrack")
			}
		}
	}
	if !found {
		t.Fatal("missing output protection")
	}
}
func TestBridgeGuardCoversAllBridgedDirectionsWithoutAccept(t *testing.T) {
	p := fixture(t)
	b, e := p.bridgeGuard(strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, o := range b {
		if r, ok := o["rule"].(map[string]any); ok {
			expr := r["expr"].([]object)
			if len(expr) != 2 {
				t.Fatal("unexpected guard")
			}
			if _, ok := expr[1]["drop"]; !ok {
				t.Fatal("non-drop")
			}
			key := expr[0]["match"].(map[string]any)["left"].(map[string]any)["meta"].(map[string]any)["key"].(string)
			seen[r["chain"].(string)+"/"+key] = true
		}
	}
	for _, v := range []string{"input/iifname", "output/oifname", "forward/iifname", "forward/oifname"} {
		if !seen[v] {
			t.Fatal(v)
		}
	}
}
func TestMissingBridgeGuardPreventsCreation(t *testing.T) {
	p := fixture(t)
	id := strings.Repeat("a", 64)
	fw, _ := p.firewall(id)
	raw, _ := json.Marshal(object{"nftables": fw})
	calls := 0
	e := verifyFirewalls(p, id, func(_ int, args []string, _ []byte) ([]byte, error) {
		calls++
		if args[4] == "bridge" {
			return []byte(`{"nftables":[]}`), nil
		}
		return raw, nil
	})
	if e == nil || calls != 2 {
		t.Fatal("missing second guard accepted", e, calls)
	}
}
func TestNoExistingRouteCanBeShadowed(t *testing.T) {
	p := fixture(t)
	for _, raw := range []string{`[{"dst":"10.251.0.0/16"}]`, `[{"dst":"10.251.0.2"}]`, `[{"dst":"10.252.0.0/24"}]`, `[{"dst":"0.0.0.0/1"}]`, `[{"dst":"malformed"}]`} {
		if hostRoutesSafe([]byte(raw), p) {
			t.Fatal(raw)
		}
	}
	if !hostRoutesSafe([]byte(`[{"dst":"default"},{"dst":"192.168.0.0/24"}]`), p) {
		t.Fatal("unrelated routes")
	}
}
