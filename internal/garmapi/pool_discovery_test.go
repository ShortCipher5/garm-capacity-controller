package garmapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func discoveryPool(id, provider string) map[string]any {
	return map[string]any{"id": id, "provider_name": provider, "generation": 1, "max_runners": 4, "tags": []any{map[string]any{"name": "linux"}}, "extra_specs": map[string]any{"secret": "private-secret"}, "instances": []any{map[string]any{"job": "private-job"}}}
}
func TestDiscoverPoolsRefreshWithoutScopeExpansion(t *testing.T) {
	var mu sync.Mutex
	pools := []map[string]any{discoveryPool(otherID, "capacity"), discoveryPool(managedPoolID, "capacity"), discoveryPool(managedRepoID, "unrelated")}
	gets := 0
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "GET" || r.URL.Path != "/api/v1/pools" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("unexpected request")
			w.WriteHeader(500)
			return
		}
		gets++
		json.NewEncoder(w).Encode(pools)
	}))
	m, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	first, err := m.DiscoverPools(context.Background())
	if err != nil || len(first.Pools) != 2 || len(first.MissingConfiguredPoolIDs) != 0 {
		t.Fatalf("%+v %v", first, err)
	}
	if first.Pools[0].ID != managedPoolID || !first.Pools[0].Configured || first.Pools[1].Configured || first.Pools[0].Fingerprint != "" {
		t.Fatal("scope/fingerprint")
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "extra_specs") {
		t.Fatal("secret output")
	}
	mu.Lock()
	pools[1]["extra_specs"] = map[string]any{"secret": "different-secret"}
	mu.Unlock()
	secretOnly, err := m.DiscoverPools(context.Background())
	if err != nil || secretOnly.Pools[0].DiscoveryFingerprint != first.Pools[0].DiscoveryFingerprint {
		t.Fatal("discovery hashes opaque secret")
	}
	mu.Lock()
	pools[1]["max_runners"] = 8
	mu.Unlock()
	changed, err := m.DiscoverPools(context.Background())
	if err != nil || changed.Pools[0].MaxRunners != 8 || changed.Pools[0].DiscoveryFingerprint == first.Pools[0].DiscoveryFingerprint {
		t.Fatal("external change not refreshed")
	}
	if _, err = m.Pool(context.Background(), otherID); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatal("discovery expanded read/edit target scope")
	}
	if _, err = m.UpdatePool(context.Background(), otherID, changed.Pools[1].DiscoveryFingerprint, PoolUpdate{Priority: value(uint64(2))}); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatal("discovery expanded write scope")
	}
	mu.Lock()
	pools = pools[:1]
	mu.Unlock()
	removed, err := m.DiscoverPools(context.Background())
	if err != nil || len(removed.MissingConfiguredPoolIDs) != 1 || removed.MissingConfiguredPoolIDs[0] != managedPoolID {
		t.Fatalf("missing %+v %v", removed, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gets != 4 {
		t.Fatalf("unexpected request count%d", gets)
	}
}
func TestDiscoverPoolsRefusesPartialOrMalformed(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[] trailing`, `[{"id":"bad","provider_name":"capacity"}]`,
		`[{"id":"` + managedPoolID + `","provider_name":"capacity","generation":1},{"id":"` + managedPoolID + `","provider_name":"other","generation":1}]`,
		`[{"id":"` + managedPoolID + `","provider_name":"capacity","generation":1},{"id":"` + otherID + `","provider_name":"capacity","generation":"bad"}]`} {
		t.Run(body, func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			m, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			result, err := m.DiscoverPools(context.Background())
			if err == nil || result.Pools != nil || result.MissingConfiguredPoolIDs != nil {
				t.Fatal("partial/malformed result")
			}
		})
	}
}

func TestPoolDiscoveryOnlyZeroEnrolledPools(t *testing.T) {
	var requests atomic.Int32
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/pools" {
			t.Error("non-discovery request")
		}
		json.NewEncoder(w).Encode([]map[string]any{discoveryPool(otherID, "capacity")})
	}))
	client, err := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.DiscoverPools(context.Background())
	if err != nil || len(result.Pools) != 1 || result.Pools[0].Configured || len(result.MissingConfiguredPoolIDs) != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	if _, ok := any(client).(interface {
		UpdatePool(context.Context, string, string, PoolUpdate) (ManagedPool, error)
	}); ok {
		t.Fatal("write API exposed")
	}
	if client.management.writes || client.management.disabledPoolCreation || len(client.management.pools) != 0 || len(client.management.repositories) != 0 {
		t.Fatal("enrollment expanded")
	}
	if requests.Load() != 1 {
		t.Fatal("unexpected request count")
	}
	for _, scope := range [][]string{nil, {""}, {" "}, {"capacity", "capacity"}, {"capacity\n"}, {strings.Repeat("x", 257)}} {
		bad, err := NewPoolDiscovery(server.URL, "token", trust, scope)
		if err == nil {
			bad.Close()
			t.Fatalf("bad scope accepted %q", scope)
		}
	}
	var absent *PoolDiscoveryClient
	if _, err := absent.DiscoverPools(context.Background()); err == nil {
		t.Fatal("nil discovery")
	}
}
