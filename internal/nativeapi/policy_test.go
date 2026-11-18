package nativeapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixturePolicy(t *testing.T) (Policy, Allocation) {
	t.Helper()
	spec := PolicySpec{Version: PolicyVersion, Controller: "controller", Host: "host", DomainID: strings.Repeat("a", 64), NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: NativeVersion, Pools: map[string]Pool{"pool": {Flavor: "small", Image: "runner", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"legacy-label"}, StoragePool: "storage", Bridge: "bridge"}}}
	p, e := NewPolicy(spec)
	if e != nil {
		t.Fatal(e)
	}
	a := Allocation{Controller: "controller", Host: "host", Pool: "pool", Name: "garm-native-1", RequestKey: strings.Repeat("e", 64), CPU: 2000, RAM: 4096, Disk: 8192, Runners: 1, Flavor: "small", Image: "runner", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", Profiles: []string{"legacy-label"}}
	return p, a
}
func TestClosedPolicyHasNoMutableInheritanceOrArbitraryDeviceMaps(t *testing.T) {
	p, a := fixturePolicy(t)
	prepared, e := p.Compile(a, "#cloud-config\nruncmd: []\n")
	if e != nil {
		t.Fatal(e)
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(prepared.Body(), &body) != nil {
		t.Fatal("payload")
	}
	if string(body["start"]) != "false" {
		t.Fatal("create must remain stopped until quota verification")
	}
	if string(body["profiles"]) != "[]" {
		t.Fatal("mutable profile inherited")
	}
	var config map[string]string
	json.Unmarshal(body["config"], &config)
	if config["limits.memory"] != "4096MiB" || config["limits.cpu"] != "2" || config["boot.autostart"] != "false" || config["boot.autorestart"] != "false" || config["security.guestapi"] != "false" || config["security.privileged"] != "false" {
		t.Fatal(config)
	}
	var devices map[string]map[string]string
	json.Unmarshal(body["devices"], &devices)
	if len(devices) != 2 || devices["root"]["pool"] != "storage" || devices["root"]["size"] != "8192MiB" || devices["root"]["path"] != "/" || devices["eth0"]["parent"] != "bridge" {
		t.Fatal(devices)
	}
	for _, d := range devices {
		for k := range d {
			if k == "source" || k == "raw" {
				t.Fatal("host source injection")
			}
		}
	}
	if !hash(prepared.Digest()) || prepared.Name() != a.Name {
		t.Fatal("identity/digest")
	}
	copy := prepared.Body()
	copy[0] = 'x'
	if prepared.Body()[0] != '{' {
		t.Fatal("caller mutated compiled body")
	}
}
func TestPolicyRejectsUnchargedOrChangedShape(t *testing.T) {
	p, a := fixturePolicy(t)
	mutations := []func(*Allocation){func(v *Allocation) { v.Host = "another" }, func(v *Allocation) { v.Controller = "other" }, func(v *Allocation) { v.Pool = "other" }, func(v *Allocation) { v.CPU = 1500 }, func(v *Allocation) { v.RAM = 0 }, func(v *Allocation) { v.Disk = 0 }, func(v *Allocation) { v.Runners = 2 }, func(v *Allocation) { v.Name = "../../other" }, func(v *Allocation) { v.Profiles = []string{"default"} }, func(v *Allocation) { v.Type = "virtual-machine" }, func(v *Allocation) { v.Fingerprint = strings.Repeat("f", 64) }, func(v *Allocation) { v.Arch = "arm64" }}
	for i, mutate := range mutations {
		b := a
		mutate(&b)
		if e := p.ValidateAllocation(b); e != ErrPolicy {
			t.Fatal("template accepted mismatch", i, e)
		}
		if _, e := p.Compile(b, "data"); e != ErrPolicy {
			t.Fatal("accepted mismatch", i, e)
		}
	}
	if _, e := p.Compile(a, ""); e != ErrPolicy {
		t.Fatal("empty bootstrap")
	}
	if _, e := p.Compile(a, strings.Repeat("a", 1<<20+1)); e != ErrPolicy {
		t.Fatal("unbounded bootstrap")
	}
}
func TestPolicySnapshotsCallerMapsAndHasStableDigest(t *testing.T) {
	p, a := fixturePolicy(t)
	spec := p.spec
	copy, e := NewPolicy(spec)
	if e != nil {
		t.Fatal(e)
	}
	if p.Digest() != copy.Digest() {
		t.Fatal("nondeterministic digest")
	}
	pool := spec.Pools["pool"]
	pool.Bridge = "rogue"
	pool.ExpectedProfiles[0] = "rogue"
	spec.Pools["pool"] = pool
	prepared, e := copy.Compile(a, "data")
	if e != nil {
		t.Fatal("external maps changed policy", e)
	}
	if strings.Contains(string(prepared.Body()), "rogue") {
		t.Fatal("caller injected new policy")
	}
}
func TestContainerCompilerRequestsExplicitRestrictionsButGrantsNoExecution(t *testing.T) {
	p, a := fixturePolicy(t)
	spec := p.spec
	pool := spec.Pools["pool"]
	pool.Type = "container"
	spec.Pools["pool"] = pool
	p, e := NewPolicy(spec)
	if e != nil {
		t.Fatal(e)
	}
	a.Type = "container"
	body, e := p.Compile(a, "data")
	if e != nil {
		t.Fatal(e)
	}
	var payload struct {
		Config map[string]string `json:"config"`
	}
	json.Unmarshal(body.Body(), &payload)
	for key, value := range map[string]string{"security.privileged": "false", "security.nesting": "false", "security.idmap.isolated": "true", "limits.memory.enforce": "hard", "limits.memory.swap": "false"} {
		if payload.Config[key] != value {
			t.Fatal(key)
		}
	}
	// Compilation is data only; the adapter currently exposes no mutation entry.
}
