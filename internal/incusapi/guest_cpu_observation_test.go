package incusapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const guestCPUFixtureName = "private-fixture-runner"

func guestCPUSweepFixture(t *testing.T, kind string, state json.RawMessage, empty bool) UtilizationSample {
	t.Helper()
	const project = "fixture-project"
	var calls atomic.Int32
	server, config := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Query().Get("project") != project {
			t.Error("unexpected non-read or wrong project")
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/1.0":
			root := rootMetadata().(map[string]any)
			root["api_extensions"] = []string{"instance_state_started_at"}
			writeSync(w, root)
		case "/1.0/instances":
			if r.URL.Query().Get("recursion") != "1" {
				t.Error("unexpected inventory query")
			}
			rows := []map[string]any{}
			if !empty {
				row := map[string]any{
					"name": guestCPUFixtureName, "status": "Running", "created_at": "2026-01-01T00:00:00Z",
					"expanded_devices": map[string]any{"system": map[string]string{"type": "disk", "path": "/", "pool": "fixture"}},
				}
				if kind != "" {
					row["type"] = kind
				}
				rows = append(rows, row)
			}
			writeSync(w, rows)
		case "/1.0/instances/" + guestCPUFixtureName + "/state":
			if empty {
				t.Error("state requested for empty inventory")
			}
			writeSync(w, state)
		default:
			t.Error("unexpected endpoint")
			http.NotFound(w, r)
		}
	}))
	client, err := New(server.URL, project, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sample, err := client.SampleUtilization(t.Context())
	wantRequests := int32(3)
	if empty {
		wantRequests = 2
	}
	if err != nil || calls.Load() != wantRequests {
		t.Fatalf("sample unavailable or request count changed: requests=%d, error=%v", calls.Load(), err)
	}
	return sample
}

func TestGuestCPUObservationUsesExistingContainerStateOnly(t *testing.T) {
	// v6.0.4/v7.0.2 native LXC fields on the already-read endpoint.
	const nativeCounters = `"cpu":{"usage":1500001},"memory":{"usage":1},"disk":{"system":{"usage":1}}`
	state := func(runtime string) json.RawMessage {
		return json.RawMessage("{" + nativeCounters + "," + runtime + "}")
	}
	const runningState = `"status":"Running","status_code":103,"pid":41,"started_at":"2026-01-02T01:02:03.000000004+01:00"`
	good := guestCPUSweepFixture(t, "container", state(runningState), false)
	start := time.Date(2026, 1, 2, 0, 2, 3, 4, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if len(good.GuestCPU) != 1 || good.GuestCPU[0] != (GuestCPUObservation{Name: guestCPUFixtureName, CreatedAt: created, StartedAt: start, PID: 41, CPUUsedNanos: 1500001}) {
		t.Fatalf("running exact native fields not retained")
	}
	b, err := json.Marshal(good)
	if err != nil || strings.Contains(string(b), "GuestCPU") || strings.Contains(string(b), guestCPUFixtureName) || strings.Contains(string(b), "1500001") {
		t.Fatal("private CPU observation escaped the utilization projection")
	}
	secondPeer := guestCPUSweepFixture(t, "container", state(runningState), false)
	if good.GuestCPUObservedScopeChanged(good) || !good.GuestCPUObservedScopeChanged(secondPeer) {
		t.Fatal("independently observed TLS keys are not an additional change signal")
	}
	for _, tc := range []struct {
		name, kind, runtime string
	}{
		{"VM despite complete host QEMU process evidence", "virtual-machine", runningState},
		{"inventory type missing", "", runningState},
		{"running without runtime fields", "container", `"status":"Running","status_code":103`},
		{"stopped with contradictory old process data", "container", `"status":"Stopped","status_code":102,"pid":41,"started_at":"2026-01-02T00:00:00Z"`},
		{"counter response missing start", "container", `"status":"Running","status_code":103,"pid":41`},
		{"future timestamp", "container", `"status":"Running","status_code":103,"pid":41,"started_at":"9999-01-02T00:00:00Z"`},
		{"start before creation", "container", `"status":"Running","status_code":103,"pid":41,"started_at":"2025-01-02T00:00:00Z"`},
		{"malformed start", "container", `"status":"Running","status_code":103,"pid":41,"started_at":3`},
		{"nonpositive PID", "container", `"status":"Running","status_code":103,"pid":0,"started_at":"2026-01-02T00:00:00Z"`},
		{"shadowed PID", "container", runningState + `,"PID":56`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := guestCPUSweepFixture(t, tc.kind, state(tc.runtime), false)
			if sample.GuestCPU != nil || good.GuestCPUObservedScopeChanged(sample) || len(sample.Errors) != 0 ||
				sample.Guests != 1 || sample.CPUUsedMillis != 1 || sample.RAMUsedMiB != 1 || sample.DiskUsedMiB != 1 {
				t.Fatal("missing process proof changed ordinary gauges or implied continuity")
			}
		})
	}
	empty := guestCPUSweepFixture(t, "", nil, true)
	if empty.GuestCPU == nil || len(empty.GuestCPU) != 0 || len(empty.Errors) != 0 || empty.Guests != 0 {
		t.Fatal("known empty inventory and unavailable runtime proof were conflated")
	}
}
