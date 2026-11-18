package protectedboot

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func fixture(t *testing.T) Payload {
	t.Helper()
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("a", 64), "bin/Runner.Listener.dll": strings.Repeat("b", 64)}
	b, _ := json.Marshal(files)
	cfg := map[string]string{".runner": base64.StdEncoding.EncodeToString([]byte(`{"agentName":"runner-1","workFolder":"_work","ephemeral":true,"disableUpdate":true}`)), ".credentials": base64.StdEncoding.EncodeToString([]byte(`{"scheme":"OAuth"}`)), ".credentials_rsaparams": base64.StdEncoding.EncodeToString([]byte(`{"d":"fixture"}`))}
	j, _ := json.Marshal(cfg)
	return Payload{Version, "runner-1", ImageDescriptor{strings.Repeat("c", 64), strings.Repeat("d", 64), Hash(b), "2.321.0", 1001, 1001, files}, base64.StdEncoding.EncodeToString(j)}
}
func TestCanonicalPayloadAndDerivedIncarnation(t *testing.T) {
	p := fixture(t)
	raw, _ := json.Marshal(p)
	got, e := Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	binding := Binding{strings.Repeat("a", 64), strings.Repeat("b", 64), "11111111-1111-4111-8111-111111111111", strings.Repeat("e", 64)}
	f, e := Materialize(got, binding)
	if e != nil || f[0].Path != ManifestPath || f[1].Path != JITPath || f[0].SHA256 != Hash(f[0].Data) || f[1].SHA256 != Hash(f[1].Data) {
		t.Fatal(e)
	}
	binding.ResourceID = "22222222-2222-4222-8222-222222222222"
	other, e := Materialize(got, binding)
	if e != nil || other[0].SHA256 == f[0].SHA256 || other[1].SHA256 != f[1].SHA256 {
		t.Fatal("incarnation binding")
	}
}
func TestRefuseUncanonicalAmbiguousAndUnsafePayloads(t *testing.T) {
	p := fixture(t)
	b, _ := json.Marshal(p)
	for _, raw := range [][]byte{append(b, ' '), []byte(strings.Replace(string(b), `"version":`, `"version":"bad","version":`, 1)), []byte(strings.Replace(string(b), `"runner_name":`, `"Runner_Name":`, 1))} {
		if _, e := Decode(raw); e == nil {
			t.Fatal("ambiguous payload")
		}
	}
	for _, mode := range []string{"files", "hash", "uid", "jit", "name"} {
		p = fixture(t)
		switch mode {
		case "files":
			p.Image.Files["bin/../../escape"] = strings.Repeat("a", 64)
		case "hash":
			p.Image.BundleDigest = strings.Repeat("f", 64)
		case "uid":
			p.Image.UID = 0
		case "jit":
			p.JIT = base64.StdEncoding.EncodeToString([]byte(`{"../escape":"YQ=="}`))
		case "name":
			p.RunnerName = "other"
		}
		raw, _ := json.Marshal(p)
		if _, e := Decode(raw); e == nil {
			t.Fatal("unsafe", mode)
		}
	}
}
func TestDescriptorCloneOwnsInventory(t *testing.T) {
	p := fixture(t)
	d := CloneDescriptor(p.Image)
	d.Files["bin/Runner.Listener"] = "bad"
	if p.Image.Files["bin/Runner.Listener"] == "bad" {
		t.Fatal("alias")
	}
}
