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
