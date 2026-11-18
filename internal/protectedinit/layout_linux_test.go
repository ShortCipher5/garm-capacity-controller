//go:build linux

package protectedinit

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func jitFixture(settings string) []byte {
	m := map[string]string{".runner": base64.StdEncoding.EncodeToString([]byte(settings)), ".credentials": base64.StdEncoding.EncodeToString([]byte(`{"scheme":"OAuth"}`)), ".credentials_rsaparams": base64.StdEncoding.EncodeToString([]byte(`{"d":"fixture"}`))}
	b, _ := json.Marshal(m)
	return []byte(base64.StdEncoding.EncodeToString(b))
}
func TestJITExactKnownFilesAndEphemeralNoUpdate(t *testing.T) {
	good := `{"agentName":"runner-1","workFolder":"_work","ephemeral":true,"disableUpdate":true}`
	if files, e := decodeJIT(jitFixture(good), "runner-1"); e != nil || len(files) != 3 {
		t.Fatal(e)
	}
	for _, s := range []string{`{"agentName":"other","workFolder":"_work","ephemeral":true,"disableUpdate":true}`, `{"agentName":"runner-1","workFolder":"/root","ephemeral":true,"disableUpdate":true}`, `{"agentName":"runner-1","workFolder":"_work","ephemeral":false,"disableUpdate":true}`, `{"agentName":"runner-1","workFolder":"_work","ephemeral":true,"disableUpdate":false}`, `{"agentName":"runner-1","workFolder":"_work","ephemeral":true,"Ephemeral":false,"disableUpdate":true}`} {
		if _, e := decodeJIT(jitFixture(s), "runner-1"); e == nil {
			t.Fatal("unsafe settings accepted")
		}
	}
	for _, raw := range []string{`{"../escape":"YQ=="}`, `{".runner":"YQ==",".runner":"Yg=="}`} {
		if _, e := decodeJIT([]byte(base64.StdEncoding.EncodeToString([]byte(raw))), "runner-1"); e == nil {
			t.Fatal("unsafe file key accepted")
		}
	}
}
func TestLayoutRefusesUnqualifiedCaller(t *testing.T) {
	if _, e := PrepareRunnerLayout(RunnerLayoutSpec{}); e == nil {
		t.Fatal("unqualified caller accepted")
	}
}
