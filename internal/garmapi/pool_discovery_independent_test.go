package garmapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPoolDiscoveryIndependentFailureAndNoScopeMutation(t *testing.T) {
	for _, mode := range []string{"alias", "partial", "utf8", "unauthorized", "oversize", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer token" || r.Method != "GET" {
					t.Error("auth/method")
				}
				switch mode {
				case "alias":
					w.Write([]byte(`[{"id":"` + managedPoolID + `","provider_name":"capacity","generation":1,"Enabled":true}]`))
				case "partial":
					w.Write([]byte(`[{"id":"` + managedPoolID + `","provider_name":"capacity","generation":1},null]`))
				case "utf8":
					w.Write([]byte{'[', 0xff, ']'})
				case "unauthorized":
					w.WriteHeader(401)
					w.Write([]byte("secret-marker"))
				case "oversize":
					w.Write([]byte(strings.Repeat(" ", 8<<20) + "[]"))
				case "redirect":
					w.Header().Set("Location", "https://must-not-follow.invalid")
					w.WriteHeader(307)
				}
			}))
			m, e := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}})
			if e != nil {
				t.Fatal(e)
			}
			defer m.Close()
			before, _ := json.Marshal(m.pools)
			v, e := m.DiscoverPools(context.Background())
			after, _ := json.Marshal(m.pools)
			if e == nil || v.Pools != nil || v.MissingConfiguredPoolIDs != nil || calls.Load() != 1 || string(before) != string(after) {
				t.Fatalf("non-atomic failure or scope change: %v calls%d", e, calls.Load())
			}
			if strings.Contains(e.Error(), "secret-marker") {
				t.Fatal("upstream error leak")
			}
		})
	}
}
