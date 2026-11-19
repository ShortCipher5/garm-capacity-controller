package garmapi

import (
	"context"
	"testing"
)

func TestDisabledPoolDemandSeparatesActualIdleFromCreation(t *testing.T) {
	for _, kind := range []string{"idle", "busy", "creating", "error", "removed", "wrong-label"} {
		t.Run(kind, func(t *testing.T) {
			client := demandFixture(t, encodeJobs(t, demandJob(1)), func(path string, n int) any {
				if path == "/api/v1/pools" {
					p := demandPool(managedPoolID)
					p["enabled"] = false
					if kind == "wrong-label" {
						p["tags"] = []map[string]string{{"name": "windows"}}
					}
					return []any{p}
				}
				if path == "/api/v1/instances" {
					if kind == "removed" {
						return []any{}
					}
					row := map[string]any{"name": "observed", "provider_name": "capacity", "pool_id": managedPoolID, "status": "running", "runner_status": "idle"}
					switch kind {
					case "busy":
						row["runner_status"] = "active"
					case "creating":
						row["status"] = "pending_create"
						row["runner_status"] = "pending"
					case "error":
						row["status"] = "error"
					}
					return []any{row}
				}
				return nil
			})
			observed, err := client.Observe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(observed.Jobs) != 1 || len(observed.Jobs[0].CandidatePoolIDs) != 0 {
				t.Fatal("disabled new eligibility", observed)
			}
			expected := 0
			if kind == "idle" {
				expected = 1
			}
			if len(observed.Jobs[0].IdleOnlyPoolIDs) != expected {
				t.Fatalf("unexpected existing-idle visibility: %+v", observed.Jobs)
			}
		})
	}
}
