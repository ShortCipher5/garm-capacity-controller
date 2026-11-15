package garmapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

func TestScaleSetDesiredTargetObservedNotCapacity(t *testing.T) {
	var mu sync.Mutex
	pool := scaleSetRow(5, "capacity")
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode([]any{pool})
	}))
	c, _ := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
	defer c.Close()
	first, err := c.DiscoverScaleSets(context.Background())
	if err != nil || first.ScaleSets[0].DesiredRunnerCount != 0 {
		t.Fatal("native omitted zero rejected")
	}
	mu.Lock()
	pool["desired_runner_count"] = 17
	mu.Unlock()
	second, err := c.DiscoverScaleSets(context.Background())
	if err != nil || second.ScaleSets[0].DesiredRunnerCount != 17 || second.ScaleSets[0].DiscoveryFingerprint == first.ScaleSets[0].DiscoveryFingerprint {
		t.Fatal("demand change above max hidden")
	}
	if second.ScaleSets[0].MaxRunners != 4 || len(c.management.client.scalesets) != 0 || c.management.writes {
		t.Fatal("target changed limits or enrollment")
	}
}
func TestScaleSetDesiredTargetRejectsMalformed(t *testing.T) {
	for name, value := range map[string]any{"negative": -1, "float": 1.5, "null": nil, "string": "3", "unsafe": uint64(9007199254740992)} {
		t.Run(name, func(t *testing.T) {
			pool := scaleSetRow(5, "capacity")
			pool["desired_runner_count"] = value
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode([]any{scaleSetRow(1, "capacity"), pool})
			}))
			c, _ := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
			defer c.Close()
			out, err := c.DiscoverScaleSets(context.Background())
			if err == nil || out.ScaleSets != nil {
				t.Fatal("invalid target or partial output")
			}
		})
	}
	pool := scaleSetRow(5, "capacity")
	pool["Desired_Runner_Count"] = 5
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode([]any{pool}) }))
	c, _ := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
	defer c.Close()
	if _, err := c.DiscoverScaleSets(context.Background()); err == nil {
		t.Fatal("case alias ignored")
	}
}
