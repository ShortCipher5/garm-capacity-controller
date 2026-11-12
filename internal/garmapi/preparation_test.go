package garmapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPinnedPreparationShapeRequiresEverySafetyField(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"canonical-omitted-zero": func(map[string]any) {}, "explicit-zero": func(row map[string]any) { row["agent_id"] = 0; row["provider_id"] = ""; row["job"] = nil },
		"creating-not-pending": func(row map[string]any) { row["status"] = "creating" }, "missing-runner": func(row map[string]any) { delete(row, "runner_status") }, "active-runner": func(row map[string]any) { row["runner_status"] = "active" }, "provider-linked": func(row map[string]any) { row["provider_id"] = "runner-1" }, "agent-assigned": func(row map[string]any) { row["agent_id"] = 12 }, "job-present": func(row map[string]any) { row["job"] = map[string]any{"private": "not-retained"} }, "scaleset": func(row map[string]any) { row["scale_set_id"] = uint64(5) },
	} {
		t.Run(name, func(t *testing.T) {
			row := map[string]any{"provider_name": "capacity", "name": "runner-1", "pool_id": "pool-a", "status": "pending_create", "runner_status": "pending"}
			change(row)
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode([]any{row}) }))
			client, err := New(server.URL, "fixture-token", trust, []string{"capacity"}, map[uint64]string{5: "pool-a"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			rows, err := client.Instances(context.Background())
			if err != nil || len(rows) != 1 {
				t.Fatal(err)
			}
			qualified := name == "canonical-omitted-zero" || name == "explicit-zero"
			if rows[0].PreparationPending != qualified {
				t.Fatalf("preparation=%v want%v", rows[0].PreparationPending, qualified)
			}
			encoded, _ := json.Marshal(rows)
			if strings.Contains(string(encoded), "not-retained") {
				t.Fatal("private job escaped")
			}
		})
	}
}
func TestMalformedPreparationFieldsNeverBecomeZeroProof(t *testing.T) {
	for name, field := range map[string]any{"null": nil, "negative": -1, "string": "0", "fraction": 0.5, "overflow": uint64(1) << 63} {
		t.Run(name, func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode([]any{map[string]any{"provider_name": "capacity", "name": "runner-1", "pool_id": "pool-a", "status": "pending_create", "runner_status": "pending", "agent_id": field}})
			}))
			client, err := New(server.URL, "fixture", trust, []string{"capacity"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err = client.Instances(context.Background()); err == nil {
				t.Fatal("malformed field became no-agent proof")
			}
		})
	}
	for _, key := range []string{"Agent_ID", "Provider_ID", "Runner_Status", "Job", "Scale_Set_ID"} {
		t.Run(key, func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode([]any{map[string]any{"provider_name": "capacity", "name": "runner-1", "pool_id": "pool-a", "status": "pending_create", "runner_status": "pending", key: 0}})
			}))
			client, err := New(server.URL, "fixture", trust, []string{"capacity"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err = client.Instances(context.Background()); err == nil {
				t.Fatal("wrong-case safety field ignored")
			}
		})
	}
}
