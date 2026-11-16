package incusapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestInstanceRequiresHardMemoryBound(t *testing.T) {
	for _, tc := range []struct {
		name            string
		value           any
		present, accept bool
	}{
		{name: "default", accept: true},
		{name: "unset", value: "", present: true, accept: true},
		{name: "hard", value: "hard", present: true, accept: true},
		{name: "soft", value: "soft", present: true},
		{name: "unknown", value: "future", present: true},
		{name: "null", value: nil, present: true},
		{name: "wrong-type", value: true, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{"limits.cpu": "2", "limits.memory": "1GiB"}
			if tc.present {
				config["limits.memory.enforce"] = tc.value
			}
			raw, err := json.Marshal(map[string]any{"type": "container", "name": "runner-1", "status_code": 103, "expanded_config": config})
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseInstance(raw)
			if tc.accept {
				if err != nil || got.RAMMiB != 1024 {
					t.Fatal(got, err)
				}
			} else if !errors.Is(err, ErrUnbounded) || got != (Instance{}) {
				t.Fatal("non-hard bound accepted", got, err)
			}
		})
	}
}

func TestSoftMemoryInventoryFailsWithoutPartialResult(t *testing.T) {
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.0" {
			writeSync(w, rootMetadata())
			return
		}
		hard := map[string]any{"name": "hard-runner", "status_code": 103, "expanded_config": map[string]string{"limits.cpu": "1", "limits.memory": "1GiB", "limits.memory.enforce": "hard"}}
		soft := map[string]any{"name": "soft-runner", "status_code": 103, "expanded_config": map[string]string{"limits.cpu": "1", "limits.memory": "1GiB", "limits.memory.enforce": "soft"}}
		writeSync(w, []any{hard, soft})
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	got, err := client.Instances(context.Background())
	if !errors.Is(err, ErrUnbounded) || got != nil {
		t.Fatal("partial bounded inventory returned", got, err)
	}
}

// limits.memory.enforce is container-only. Do not infer a VM safety guarantee
// from it; preserve ordinary VM accounting and conservatively reject anomalies.
func TestVMMemoryEnforcementIsNotAnIndependentGuarantee(t *testing.T) {
	for _, mode := range []string{"absent", "", "hard", "soft"} {
		t.Run(mode, func(t *testing.T) {
			config := map[string]any{"limits.cpu": "1", "limits.memory": "2GiB"}
			if mode != "absent" {
				config["limits.memory.enforce"] = mode
			}
			raw, err := json.Marshal(map[string]any{"type": "virtual-machine", "name": "vm-runner", "status_code": 103, "expanded_config": config})
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseInstance(raw)
			if mode == "soft" {
				if !errors.Is(err, ErrUnbounded) {
					t.Fatal("anomalous VM soft field accepted", err)
				}
			} else if err != nil || got.RAMMiB != 2048 {
				t.Fatal(got, err)
			}
		})
	}
}
