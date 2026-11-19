package managedprojection

import (
	"strings"
	"testing"
)

func TestInventoryProjectionPreservesChargesAndStripsSecrets(t *testing.T) {
	raw := []byte(`[{"provider_name":"owned","pool_id":"unknown","name":"runner","job":{"secret":"private"},"jit_config":"secret","provider_id":"native"},{"provider_name":"other","name":"omit"}]`)
	out, err := Instances(raw, map[string]bool{"owned": true})
	if err != nil || strings.Contains(string(out), "secret") || strings.Contains(string(out), "omit") || !strings.Contains(string(out), `"job":{}`) || !strings.Contains(string(out), "unknown") {
		t.Fatalf("%s %v", out, err)
	}
	for _, v := range []string{`[{"provider_name":"owned","Provider_ID":"bad"}]`, `[{"provider_name":"owned","provider_name":"other"}]`, `null`, `{}`} {
		if _, err := Instances([]byte(v), map[string]bool{"owned": true}); err == nil {
			t.Fatal("bad inventory accepted")
		}
	}
}
