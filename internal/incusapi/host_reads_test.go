package incusapi

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"testing"
)

func TestGuestStateUsesNativeDiskAndOnlyPositiveWireMeasurements(t *testing.T) {
	expected := map[string]struct{}{"os-disk": {}, "data-disk": {}}
	raw := func(body string) json.RawMessage { return json.RawMessage(body) }
	validCPUAndRAM := `"cpu":{"usage":999999},"memory":{"usage":1048577}`
	for _, tc := range []struct {
		name, fields string
		want         guestUsage
	}{
		{"complete", validCPUAndRAM + `,"disk":{"os-disk":{"usage":1048577,"total":0},"data-disk":{"usage":1}}`, guestUsage{cpuKnown: true, ramKnown: true, ramMiB: 2, diskMiB: 2, diskComplete: true}},
		{"stopped default counters are not measurements", `"status":"Stopped","status_code":102,"cpu":{"usage":0},"memory":{"usage":0},"disk":{"os-disk":{"usage":7340032},"data-disk":{"usage":1}}`, guestUsage{diskMiB: 8, diskComplete: true}},
		{"negative and absent", `"cpu":{"usage":-1},"memory":null,"disk":null`, guestUsage{}},
		{"malformed optional axes", `"cpu":{"usage":"9"},"memory":{"usage":1.1},"disk":{"os-disk":{"usage":false}}`, guestUsage{}},
		{"duplicate counter keys", `"cpu":{"usage":1,"usage":2},"memory":{"Usage":1},"disk":{"os-disk":{"usage":1},"os-disk":{"usage":2}}`, guestUsage{}},
		{"array cannot qualify native inventory", validCPUAndRAM + `,"disk":[{"usage":{"usage_bytes":2}}]`, guestUsage{cpuKnown: true, ramKnown: true, ramMiB: 2, diskMiB: 1}},
		{"empty device map", validCPUAndRAM + `,"disk":{}`, guestUsage{cpuKnown: true, ramKnown: true, ramMiB: 2}},
		{"unsupported root omitted", validCPUAndRAM + `,"disk":{"data-disk":{"usage":12}}`, guestUsage{cpuKnown: true, ramKnown: true, ramMiB: 2, diskMiB: 1}},
		{"unknown root and independent valid disk", validCPUAndRAM + `,"disk":{"os-disk":{"usage":0},"data-disk":{"usage":12}}`, guestUsage{cpuKnown: true, ramKnown: true, ramMiB: 2, diskMiB: 1}},
		{"unexpected positive device cannot substitute", validCPUAndRAM + `,"disk":{"os-disk":{"usage":1},"other":{"usage":104857600}}`, guestUsage{cpuKnown: true, ramKnown: true, ramMiB: 2, diskMiB: 1}},
		{"disk bytes overflow fails closed", validCPUAndRAM + `,"disk":{"os-disk":{"usage":9223372036854775807},"data-disk":{"usage":1}}`, guestUsage{cpuKnown: true, ramKnown: true, ramMiB: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGuestStateUsage(raw("{"+tc.fields+"}"), expected)
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}

	legacy, err := parseGuestStateUsage(raw(`{"cpu":{"usage":1000000},"memory":{"usage":1},"disk":[{"usage":{"usage_bytes":1048577}}]}`), nil)
	if err != nil || legacy != (guestUsage{cpuMillis: 1, cpuKnown: true, ramMiB: 1, ramKnown: true, diskMiB: 2, diskComplete: true}) {
		t.Fatalf("historical adapter shape changed: %+v, %v", legacy, err)
	}
	unmatched, err := parseGuestStateUsage(raw(`{"disk":{"root":{"usage":10}}}`), nil)
	if err != nil || unmatched.diskMiB != 1 || unmatched.diskComplete {
		t.Fatalf("native disk without inventory was promoted to complete: %+v, %v", unmatched, err)
	}
	value := int64(math.MaxInt64 - 1)
	if !addGuestCounter(&value, 1) || addGuestCounter(&value, 1) || value != math.MaxInt64 {
		t.Fatalf("overflowed accumulator: %d", value)
	}
}

func TestSampleUtilizationReportsPartialNativeGuestState(t *testing.T) {
	const project = "fixture-project"
	server, conf := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("project") != project {
			t.Error("unexpected request")
			http.Error(w, "fixture only", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/1.0":
			writeSync(w, rootMetadata())
		case "/1.0/instances":
			if r.URL.Query().Get("recursion") != "1" {
				t.Error("unexpected inventory query")
			}
			shared := `"created_at":"2026-01-01T00:00:00Z","expanded_devices":{"system":{"type":"disk","path":"/","pool":"dir"},"extra":{"type":"disk","path":"/mnt","source":"attached","pool":"dir"},"host-path":{"type":"disk","path":"/host","source":"/unused"},"eth0":{"type":"nic"}}`
			writeSync(w, json.RawMessage(`[{"name":"complete","status":"Running",`+shared+`},{"name":"partial","status":"Stopped",`+shared+`},{"name":"unreachable","status":"Running",`+shared+`}]`))
		case "/1.0/instances/complete/state":
			writeSync(w, json.RawMessage(`{"cpu":{"usage":1500000},"memory":{"usage":1048577},"disk":{"system":{"usage":1048577},"extra":{"usage":1}}}`))
		case "/1.0/instances/partial/state":
			writeSync(w, json.RawMessage(`{"status":"Stopped","cpu":{"usage":0},"memory":{"usage":0},"disk":{"extra":{"usage":1048577}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	client, err := New(server.URL, project, conf)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	got, err := client.SampleUtilization(t.Context())
	if err != nil || got.CPUUsedMillis != 1 || got.RAMUsedMiB != 2 || got.DiskUsedMiB != 4 || got.Guests != 2 ||
		len(got.Errors) != 2 || got.Errors[0].Guest != "partial" || !errors.Is(got.Errors[0].Err, ErrResponse) ||
		got.Errors[1].Guest != "unreachable" {
		t.Fatalf("partial support not preserved: %+v, %v", got, err)
	}
}
