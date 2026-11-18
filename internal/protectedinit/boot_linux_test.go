//go:build linux

package protectedinit

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"os"
	"strings"
	"testing"
)

func TestBootManifestExactCodecAndPayloadBinding(t *testing.T) {
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("a", 64), "bin/Runner.Listener.dll": strings.Repeat("b", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("c", 64), LauncherSHA256: strings.Repeat("c", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	jit := jitFixture(`{"agentName":"runner-1","workFolder":"_work","ephemeral":true,"disableUpdate":true}`)
	p := protectedboot.Payload{Version: protectedboot.Version, RunnerName: "runner-1", Image: image, JIT: string(jit)}
	binding := protectedboot.Binding{DomainID: strings.Repeat("d", 64), PolicyDigest: strings.Repeat("e", 64), ResourceID: "11111111-1111-4111-8111-111111111111", ImageFingerprint: strings.Repeat("f", 64)}
	materialized, e := protectedboot.Materialize(p, binding)
	if e != nil {
		t.Fatal(e)
	}
	m, got, e := parseBoot(materialized[0].Data, materialized[1].Data)
	if e != nil || m.ResourceID != binding.ResourceID || got.JIT != string(jit) {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{append(append([]byte{}, materialized[0].Data...), ' '), []byte(strings.Replace(string(materialized[0].Data), `"resource_id":`, `"resource_id":"bogus","resource_id":`, 1)), []byte(strings.Replace(string(materialized[0].Data), "runner-1", "other", 1))} {
		if _, _, e := parseBoot(bad, jit); e == nil {
			t.Fatal("mismatched manifest accepted")
		}
	}
	if _, _, e := parseBoot(materialized[0].Data, append(jit, ' ')); e == nil {
		t.Fatal("changed JIT accepted")
	}
}
func TestBootRefusesOutsideDedicatedPID1(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("PID1")
	}
	if Boot() == nil {
		t.Fatal("unqualified boot")
	}
}
