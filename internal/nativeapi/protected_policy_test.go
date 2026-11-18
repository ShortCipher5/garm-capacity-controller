package nativeapi

import (
	"encoding/base64"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"strings"
	"testing"
)

func TestProtectedPolicyPinsImageAndCanonicalBootPayload(t *testing.T) {
	base, a := fixturePolicy(t)
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("4", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	spec := base.spec
	spec.Version = ProtectedPolicyVersion
	pool := spec.Pools[a.Pool]
	pool.ProtectedImage = &image
	spec.Pools[a.Pool] = pool
	p, e := NewPolicy(spec)
	if e != nil {
		t.Fatal(e)
	}
	if e := p.ValidateAllocation(a); e != nil {
		t.Fatalf("template must validate without JIT: %v", e)
	}
	invalid := a
	invalid.Fingerprint = strings.Repeat("9", 64)
	if p.ValidateAllocation(invalid) == nil {
		t.Fatal("changed image accepted in template")
	}
	if _, e := p.Compile(a, "profile-validation"); e == nil {
		t.Fatal("template placeholder accepted as executable bootstrap")
	}
	got, ok := p.ProtectedImage(a.Pool)
	if !ok || !p.Protected() {
		t.Fatal("missing descriptor")
	}
	got.Files["bin/Runner.Listener"] = "changed"
	again, _ := p.ProtectedImage(a.Pool)
	if again.Files["bin/Runner.Listener"] != files["bin/Runner.Listener"] {
		t.Fatal("mutable inventory")
	}
	settings, _ := json.Marshal(map[string]any{"agentName": a.Name, "workFolder": "_work", "ephemeral": true, "disableUpdate": true})
	jit, _ := json.Marshal(map[string]string{".runner": base64.StdEncoding.EncodeToString(settings), ".credentials": base64.StdEncoding.EncodeToString([]byte(`{"scheme":"OAuth"}`)), ".credentials_rsaparams": base64.StdEncoding.EncodeToString([]byte(`{"d":"fixture"}`))})
	payload := protectedboot.Payload{Version: protectedboot.Version, RunnerName: a.Name, Image: image, JIT: base64.StdEncoding.EncodeToString(jit)}
	body, _ := json.Marshal(payload)
	if _, e := p.Compile(a, string(body)); e != nil {
		t.Fatal(e)
	}
	// V5 retains canonical protected boot and adds only closed sibling paths.
	v5spec := spec
	v5spec.Version = PartitionPolicyVersion
	v5spec.EgressDigest = strings.Repeat("a", 64)
	v5, err := NewPolicy(v5spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := v5.Compile(a, string(body))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct{ Config map[string]string }
	if json.Unmarshal(prepared.Body(), &decoded) != nil {
		t.Fatal("body")
	}
	want := "lxc.cgroup.relative = 0\nlxc.cgroup.dir.container = guests/" + a.Name + "\nlxc.cgroup.dir.container.inner = payload\nlxc.cgroup.dir.monitor = monitors/" + a.Name + "\nlxc.cgroup.dir.monitor.pivot = monitors\n"
	if decoded.Config["raw.lxc"] != want || !v5.Protected() {
		t.Fatal("unbound cgroup paths")
	}
	if _, ok := v5.ProtectedImage(a.Pool); !ok {
		t.Fatal("lost protected image")
	}
	for _, bad := range []string{"../escape", "runner\nlxc.cgroup.relative=1", "runner/payload"} {
		changed := a
		changed.Name = bad
		if _, err := v5.Compile(changed, string(body)); err == nil {
			t.Fatal("path injection accepted")
		}
	}
	legacy, err := p.Compile(a, string(body))
	if err != nil || legacy.Digest() == prepared.Digest() {
		t.Fatal("version not bound")
	}
	json.Unmarshal(legacy.Body(), &decoded)
	var legacyBody struct{ Config map[string]string }
	json.Unmarshal(legacy.Body(), &legacyBody)
	if _, exists := legacyBody.Config["raw.lxc"]; exists {
		t.Fatal("legacy policy silently changed")
	}
	v5spec.EgressDigest = ""
	if _, err := NewPolicy(v5spec); err == nil {
		t.Fatal("v5 without egress")
	}
	if _, e := p.Compile(a, "#cloud-config\n"); e == nil {
		t.Fatal("assumed cloud-init")
	}
	payload.Image.InitSHA256 = strings.Repeat("f", 64)
	body, _ = json.Marshal(payload)
	if _, e := p.Compile(a, string(body)); e == nil {
		t.Fatal("changed init")
	}
	spec.Version = PolicyVersion
	if _, e := NewPolicy(spec); e == nil {
		t.Fatal("v2 descriptor allowed")
	}
	spec.Version = ProtectedPolicyVersion
	pool.ProtectedImage = nil
	spec.Pools[a.Pool] = pool
	if _, e := NewPolicy(spec); e == nil {
		t.Fatal("v3 missing descriptor")
	}
}
