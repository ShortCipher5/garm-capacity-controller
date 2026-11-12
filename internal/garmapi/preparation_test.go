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

func TestOriginalSourcePoolMetadataIsStrictAndDoesNotSilentlyDiscardTemplates(t *testing.T) {
	base := `{"id":"pool-a","provider_name":"capacity","generation":0,"repo_id":"22222222-2222-4222-8222-222222222222","endpoint":{"name":"github"},"enable_shell":false,"enabled":true,"image":"linux","flavor":"small","os_type":"linux","os_arch":"amd64"}`
	cases := map[string]struct {
		body                  string
		err, defaultBootstrap bool
	}{
		"canonical-zero-omitted":     {base, false, true},
		"explicit-zero":              {strings.TrimSuffix(base, "}") + `,"template_id":0,"extra_specs":{}}`, false, true},
		"custom-template":            {strings.TrimSuffix(base, "}") + `,"template_id":5}`, false, false},
		"custom-script":              {strings.TrimSuffix(base, "}") + `,"extra_specs":{"runner_install_template":"private-do-not-retain"}}`, false, false},
		"null-template":              {strings.TrimSuffix(base, "}") + `,"template_id":null}`, true, false},
		"missing-generation":         {strings.Replace(base, `"generation":0,`, "", 1), true, false},
		"null-generation":            {strings.Replace(base, `"generation":0`, `"generation":null`, 1), true, false},
		"generation-alias":           {strings.Replace(base, `"generation":0`, `"Generation":0`, 1), true, false},
		"generation-duplicate":       {strings.Replace(base, `"generation":0`, `"generation":0,"generation":1`, 1), true, false},
		"endpoint-alias":             {strings.Replace(base, `"name":"github"`, `"Name":"github"`, 1), true, false},
		"endpoint-duplicate":         {strings.Replace(base, `"name":"github"`, `"name":"github","name":"other"`, 1), true, false},
		"entity-ambiguous":           {strings.TrimSuffix(base, "}") + `,"org_id":"44444444-4444-4444-8444-444444444444"}`, true, false},
		"wrong-id":                   {strings.Replace(base, `"id":"pool-a"`, `"id":"pool-b"`, 1), true, false},
		"wrong-provider":             {strings.Replace(base, `"provider_name":"capacity"`, `"provider_name":"other"`, 1), true, false},
		"enabled-absent-is-disabled": {strings.Replace(base, `,"enabled":true`, "", 1), false, true},
		"enabled-false-is-disabled":  {strings.Replace(base, `"enabled":true`, `"enabled":false`, 1), false, true},
		"enabled-null":               {strings.Replace(base, `"enabled":true`, `"enabled":null`, 1), true, false},
		"enabled-alias":              {strings.Replace(base, `"enabled":true`, `"Enabled":true`, 1), true, false},
		"enabled-coercion":           {strings.Replace(base, `"enabled":true`, `"enabled":"true"`, 1), true, false},
		"shell-enabled":              {strings.Replace(base, `"enable_shell":false`, `"enable_shell":true`, 1), false, false},
		"shell-missing":              {strings.Replace(base, `,"enable_shell":false`, "", 1), true, false},
		"shell-null":                 {strings.Replace(base, `"enable_shell":false`, `"enable_shell":null`, 1), true, false},
		"shell-alias":                {strings.Replace(base, `"enable_shell":false`, `"Enable_Shell":false`, 1), true, false},
		"trailing":                   {base + ` {}`, true, false},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/pools/pool-a" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("wrong actual metadata route")
				}
				w.Write([]byte(item.body))
			}))
			client, err := New(server.URL, "fixture-token", trust, []string{"capacity"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			observation, err := client.PreparationPool(context.Background(), "pool-a")
			if (err != nil) != item.err {
				t.Fatalf("metadata error %v", err)
			}
			if err == nil && (!observation.FieldsKnown || observation.DefaultBootstrap != item.defaultBootstrap) {
				t.Fatalf("metadata %+v", observation)
			}
			raw, _ := json.Marshal(observation)
			if strings.Contains(string(raw), "private-do-not-retain") {
				t.Fatal("script leaked into observation")
			}
		})
	}
}
func TestPositiveAgentRowIsOnlyShapeNotOriginalAuthority(t *testing.T) {
	for _, state := range []string{"pending_create", "creating"} {
		t.Run(state, func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode([]any{map[string]any{"id": "11111111-1111-4111-8111-111111111111", "name": "runner-1", "pool_id": "pool-a", "provider_name": "capacity", "status": state, "runner_status": "pending", "agent_id": 42, "generation": 0}})
			}))
			client, err := New(server.URL, "fixture", trust, []string{"capacity"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			rows, err := client.Instances(context.Background())
			if err != nil || len(rows) != 1 || !rows[0].IdentityFieldsKnown || !rows[0].SourcePreparationPending || rows[0].PreparationPending {
				t.Fatalf("row %+v %v", rows, err)
			}
		})
	}
}
