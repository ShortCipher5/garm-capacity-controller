package garmapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func scaleSetRow(id uint64, provider string) map[string]any {
	return map[string]any{"id": id, "scale_set_id": 999, "name": "existing-set", "provider_name": provider, "state": "created", "generation": 1, "enabled": true, "max_runners": 4, "min_idle_runners": 1, "tags": []any{map[string]any{"name": "linux"}}, "org_id": managedRepoID, "extra_specs": map[string]any{"secret": "private-secret"}, "status_messages": []any{"private-error"}, "instances": []any{"private-job"}}
}
func TestScaleSetDiscoveryExactScopeAndRefresh(t *testing.T) {
	var mu sync.Mutex
	rows := []map[string]any{scaleSetRow(12, "capacity"), scaleSetRow(3, "capacity"), scaleSetRow(7, "other")}
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "GET" || r.URL.Path != "/api/v1/scalesets" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("unexpected request")
		}
		json.NewEncoder(w).Encode(rows)
	}))
	c, err := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first, err := c.DiscoverScaleSets(context.Background())
	if err != nil || len(first.ScaleSets) != 2 {
		t.Fatalf("%+v %v", first, err)
	}
	one := first.ScaleSets[0]
	if one.ID != 3 || one.OwnerKind != "organization" || one.OwnerID != managedRepoID || one.Name != "existing-set" || one.Tags[0] != "linux" {
		t.Fatal("wrong identity")
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "scale_set_id") {
		t.Fatal("private or alternate identity exposed")
	}
	mu.Lock()
	rows[1]["extra_specs"] = "different-secret"
	mu.Unlock()
	secret, err := c.DiscoverScaleSets(context.Background())
	if err != nil || secret.ScaleSets[0].DiscoveryFingerprint != one.DiscoveryFingerprint {
		t.Fatal("secret fingerprint")
	}
	mu.Lock()
	rows[1]["max_runners"] = 8
	mu.Unlock()
	changed, err := c.DiscoverScaleSets(context.Background())
	if err != nil || changed.ScaleSets[0].MaxRunners != 8 || changed.ScaleSets[0].DiscoveryFingerprint == one.DiscoveryFingerprint {
		t.Fatal("missed direct edit")
	}
	if len(c.management.pools) != 0 || len(c.management.client.scalesets) != 0 || c.management.writes {
		t.Fatal("discovery enrolled or enabled")
	}
}
func TestScaleSetDiscoveryUnavailableNotEmpty(t *testing.T) {
	for _, code := range []int{401, 403, 404, 500, 307} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://other.invalid/")
				w.WriteHeader(code)
				w.Write([]byte("private-error"))
			}))
			c, _ := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
			defer c.Close()
			result, err := c.DiscoverScaleSets(context.Background())
			if err == nil || result.ScaleSets != nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("error became empty success/leak")
			}
		})
	}
}
func TestScaleSetDiscoveryNoPartialMalformed(t *testing.T) {
	valid, _ := json.Marshal(scaleSetRow(1, "capacity"))
	for name, mutate := range map[string]func(map[string]any){
		"uuid": func(m map[string]any) { m["id"] = managedPoolID }, "unsafe-js": func(m map[string]any) { m["id"] = uint64(9007199254740992) }, "zero": func(m map[string]any) { m["id"] = 0 }, "duplicate": func(m map[string]any) { m["id"] = 1 }, "name-alias": func(m map[string]any) { m["Name"] = "other" }, "owner-ambiguous": func(m map[string]any) { m["repo_id"] = otherID }, "state-unknown": func(m map[string]any) { m["state"] = "future" }, "tag-alias": func(m map[string]any) { m["tags"] = []any{map[string]any{"name": "linux", "Name": "windows"}} }, "enabled-null": func(m map[string]any) { m["enabled"] = nil }, "bad-bounds": func(m map[string]any) { m["min_idle_runners"] = 9 }, "missing-generation": func(m map[string]any) { delete(m, "generation") },
	} {
		t.Run(name, func(t *testing.T) {
			bad := scaleSetRow(2, "capacity")
			mutate(bad)
			raw, _ := json.Marshal(bad)
			body := "[" + string(valid) + "," + string(raw) + "]"
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			c, _ := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
			defer c.Close()
			out, err := c.DiscoverScaleSets(context.Background())
			if err == nil || out.ScaleSets != nil {
				t.Fatal("partial/malformed accepted")
			}
		})
	}
}
func TestScaleSetDiscoveryEmptyAndGateCancellation(t *testing.T) {
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) }))
	c, _ := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
	defer c.Close()
	result, err := c.DiscoverScaleSets(context.Background())
	if err != nil || result.ScaleSets == nil || len(result.ScaleSets) != 0 {
		t.Fatal("empty collection unavailable")
	}
	c.management.discoveryGate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.DiscoverScaleSets(ctx); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ignored cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("unbounded gate wait")
	}
	<-c.management.discoveryGate
}
