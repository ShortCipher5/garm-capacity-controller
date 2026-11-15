package garmapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestDiscoveryPoolOwnerTypesAndChange(t *testing.T) {
	for field, kind := range map[string]string{"repo_id": "repository", "org_id": "organization", "enterprise_id": "enterprise", "": "unknown"} {
		t.Run(kind, func(t *testing.T) {
			var mu sync.Mutex
			pool := discoveryPool(managedPoolID, "capacity")
			if field != "" {
				pool[field] = managedRepoID
			}
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method != "GET" || r.URL.Path != "/api/v1/pools" {
					t.Error("not observation")
				}
				json.NewEncoder(w).Encode([]any{pool})
			}))
			c, err := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			first, err := c.DiscoverPools(context.Background())
			if err != nil || len(first.Pools) != 1 || first.Pools[0].OwnerKind != kind {
				t.Fatalf("%+v %v", first, err)
			}
			if field == "" {
				if first.Pools[0].OwnerID != "" {
					t.Fatal("invented owner")
				}
				return
			}
			if first.Pools[0].OwnerID != managedRepoID {
				t.Fatal("wrong owner ID")
			}
			mu.Lock()
			pool[field] = otherID
			mu.Unlock()
			second, err := c.DiscoverPools(context.Background())
			if err != nil || second.Pools[0].OwnerID != otherID || second.Pools[0].DiscoveryFingerprint == first.Pools[0].DiscoveryFingerprint || second.Pools[0].Configured {
				t.Fatal("owner change hidden or enrolled")
			}
		})
	}
}
func TestDiscoveryPoolOwnerMalformedOrAmbiguous(t *testing.T) {
	for _, extra := range []string{`"org_id":null`, `"enterprise_id":17`, `"org_id":"not-an-id"`, `"Org_ID":"` + managedRepoID + `"`, `"repo_id":"` + managedRepoID + `","org_id":"` + otherID + `"`, `"org_id":"` + managedRepoID + `","enterprise_id":"` + otherID + `"`} {
		t.Run(extra, func(t *testing.T) {
			body := `[{"id":"` + managedPoolID + `","provider_name":"capacity","generation":1,` + extra + `}]`
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			c, err := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			result, err := c.DiscoverPools(context.Background())
			if err == nil || result.Pools != nil {
				t.Fatal("ambiguous ownership accepted")
			}
			if strings.Contains(err.Error(), managedRepoID) {
				t.Fatal("upstream detail leaked")
			}
		})
	}
}
