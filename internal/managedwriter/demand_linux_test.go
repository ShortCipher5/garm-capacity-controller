package managedwriter

import (
	"strings"
	"testing"
)

func TestDemandProjectionScopesAndSecrets(t *testing.T) {
	providers := map[string]bool{"native": true}
	repos := map[string]bool{"repo": true}
	raw := []byte(`[{"id":"pool","provider_name":"native","repo_id":"repo","image":"img","secret":"credential"},{"id":"foreign","provider_name":"native","repo_id":"other"}]`)
	out, e := projectDemand(raw, "/api/v1/pools", providers, repos)
	if e != nil || strings.Contains(string(out), "secret") || strings.Contains(string(out), "foreign") || !strings.Contains(string(out), "pool") {
		t.Fatalf("bad projection %s %v", out, e)
	}
	for _, raw := range []string{`[{"repo_id":"repo","Repo_id":"other"}]`, `[{"repo_id":"repo","repo_id":"other"}]`, `{"repo_id":"repo"}`} {
		if _, e := projectDemand([]byte(raw), "/api/v1/jobs", providers, repos); e == nil {
			t.Fatal("accepted ambiguous input")
		}
	}
	if _, e := projectDemand([]byte(`{"id":"other","repo_id":"repo","provider_name":"native"}`), "/api/v1/pools/pool", providers, repos); e == nil {
		t.Fatal("wrong pool accepted")
	}
}
