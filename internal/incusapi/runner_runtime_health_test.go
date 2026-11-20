package incusapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	healthUUID = "33333333-3333-4333-8333-333333333333"
	healthGen  = "44444444-4444-4444-8444-444444444444"
)

var healthExpected = RunnerRuntimeExpected{
	RunnerRootVolumeExpected: runnerRootExpectedFixture, UUID: healthUUID, Generation: healthGen,
	CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
}

func healthGuest() map[string]any {
	tags := func() map[string]string {
		return map[string]string{
			stockRunnerControllerKey: healthExpected.ControllerID, stockRunnerPoolKey: healthExpected.PoolID,
			runnerUUIDKey: healthUUID, runnerGenerationKey: healthGen, "user.user-data": "unretained-fixture-bootstrap",
		}
	}
	return map[string]any{
		"name": healthExpected.Name, "project": "fixture-project", "type": "container", "created_at": healthExpected.CreatedAt,
		"status": "Running", "status_code": 103, "config": tags(), "expanded_config": tags(),
	}
}

func healthState(cpu, memory int64) map[string]any {
	return map[string]any{
		"status": "Running", "status_code": 103, "pid": 41, "started_at": healthExpected.CreatedAt.Add(time.Minute),
		"cpu": map[string]int64{"usage": cpu}, "memory": map[string]int64{"usage": memory},
		"disk": map[string]any{"root": map[string]any{"usage": 989898}}, "network": "unretained-fixture-network",
	}
}

type healthFixture struct {
	client *Client
	mu     sync.Mutex
	calls  []string
}

func runtimeHealthFixture(t *testing.T, version string, change func(string, int, map[string]any)) *healthFixture {
	t.Helper()
	const base = "/1.0/instances/fixture-runner"
	f := &healthFixture{}
	counts := make(map[string]int)
	var server *httptest.Server
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery != "project=fixture-project" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("non-read, broad query or unverified peer")
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.Path)
		counts[r.URL.Path]++
		n := counts[r.URL.Path]
		f.mu.Unlock()
		var data map[string]any
		switch r.URL.Path {
		case "/1.0":
			data = physicalRoot(server)
			data["environment"].(map[string]any)["server_version"] = version
		case base:
			data = healthGuest()
		case base + "/state":
			data = healthState(int64(n*10000), int64(4096-n*100))
		default:
			t.Error("unexpected Incus path")
			w.WriteHeader(404)
			return
		}
		if change != nil {
			change(r.URL.Path, n, data)
		}
		writeSync(w, data)
	}))
	c, err := New(server.URL, "fixture-project", credentials)
	if err != nil {
		t.Fatal(err)
	}
	f.client = c
	t.Cleanup(c.Close)
	return f
}

func (f *healthFixture) pin(t *testing.T) {
	t.Helper()
	ref, err := f.client.PhysicalIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.PinPhysicalIdentity(ref); err != nil {
		t.Fatal(err)
	}
}
func (f *healthFixture) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestRunnerRuntimeHealthReadsOnlyPinnedNamedGuestAndRedacts(t *testing.T) {
	f := runtimeHealthFixture(t, runnerHealthVersion, nil)
	if got, err := f.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected); !errors.Is(err, ErrConfiguration) || got != (RunnerRuntimeHealth{}) || len(f.paths()) != 0 {
		t.Fatal("unpinned client made a read")
	}
	f.pin(t)
	earliest := time.Now()
	got, err := f.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected)
	if err != nil || got.CPUCumulativeNanoseconds == nil || *got.CPUCumulativeNanoseconds != 20000 ||
		got.MemoryCurrentBytes == nil || *got.MemoryCurrentBytes != 3896 ||
		got.SampledAt.Before(earliest) || got.SampledAt.After(time.Now()) {
		t.Fatalf("bad sampled counters: %+v / %v", got, err)
	}
	const base = "/1.0/instances/fixture-runner"
	want := []string{"/1.0", "/1.0", base, base + "/state", base + "/state", base, "/1.0"}
	if got := f.paths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected GET sequence: %v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "unretained") || strings.Contains(string(encoded), "fixture-runner") ||
		strings.Contains(string(encoded), "989898") || strings.Contains(string(encoded), "pid") || strings.Contains(string(encoded), healthUUID) {
		t.Fatalf("counter projection exposed extraneous contents: %v", err)
	}
}

func TestRunnerRuntimeHealthRefusesDriftAndAmbiguousCounters(t *testing.T) {
	const base = "/1.0/instances/fixture-runner"
	for name, change := range map[string]func(string, int, map[string]any){
		"other project": func(path string, n int, d map[string]any) {
			if path == base && n == 1 {
				d["project"] = "other"
			}
		},
		"replacement": func(path string, n int, d map[string]any) {
			if path == base && n == 2 {
				d["config"].(map[string]string)[runnerUUIDKey] = healthGen
			}
		},
		"generation drift": func(path string, n int, d map[string]any) {
			if path == base && n == 2 {
				d["expanded_config"].(map[string]string)[runnerGenerationKey] = healthUUID
			}
		},
		"changed start": func(path string, n int, d map[string]any) {
			if path == base+"/state" && n == 2 {
				d["started_at"] = healthExpected.CreatedAt.Add(2 * time.Minute)
			}
		},
		"changed PID": func(path string, n int, d map[string]any) {
			if path == base+"/state" && n == 2 {
				d["pid"] = 42
			}
		},
		"counter decreases under same process": func(path string, n int, d map[string]any) {
			if path == base+"/state" && n == 2 {
				d["cpu"] = map[string]int64{"usage": 9}
			}
		},
		"non-running": func(path string, n int, d map[string]any) {
			if path == base+"/state" && n == 2 {
				d["status_code"], d["status"] = 102, "Stopped"
			}
		},
		"unsupported process time": func(path string, n int, d map[string]any) {
			if path == base+"/state" {
				d["started_at"] = time.Time{}
			}
		},
		"no available counters": func(path string, n int, d map[string]any) {
			if path == base+"/state" {
				d["cpu"] = map[string]int64{"usage": -1}
				d["memory"] = map[string]int64{"usage": 0}
			}
		},
		"ambiguous alias": func(path string, n int, d map[string]any) {
			if path == base+"/state" {
				d["cpu"] = map[string]int64{"usage": 100, "Usage": 101}
			}
		},
		"wrong usage type": func(path string, n int, d map[string]any) {
			if path == base+"/state" {
				d["memory"] = map[string]string{"usage": "100"}
			}
		},
		"standalone drift": func(path string, n int, d map[string]any) {
			if path == "/1.0" && n == 3 {
				d["environment"].(map[string]any)["server_clustered"] = true
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := runtimeHealthFixture(t, runnerHealthVersion, change)
			f.pin(t)
			if got, err := f.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected); err == nil || got != (RunnerRuntimeHealth{}) {
				t.Fatalf("drift exposed counters: %+v / %v", got, err)
			}
			if name == "other project" && len(f.paths()) != 3 {
				t.Fatal("wrong project reached runtime sampling")
			}
		})
	}
}

func TestRunnerRuntimeHealthOnlyKeepsPositiveAvailableMetrics(t *testing.T) {
	f := runtimeHealthFixture(t, runnerHealthVersion, func(path string, n int, d map[string]any) {
		if strings.HasSuffix(path, "/state") {
			d["cpu"] = map[string]int64{"usage": 0}
		}
	})
	f.pin(t)
	got, err := f.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected)
	if err != nil || got.MemoryCurrentBytes == nil || *got.MemoryCurrentBytes != 3896 || got.CPUCumulativeNanoseconds != nil {
		t.Fatalf("unavailable CPU became zero or hid measured memory: %+v / %v", got, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "cpu_cumulative_nanoseconds") {
		t.Fatal("unavailable CPU was published")
	}

	unsupported := runtimeHealthFixture(t, "6.0.4", nil)
	unsupported.pin(t)
	if got, err := unsupported.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected); !errors.Is(err, ErrUnsupported) || got != (RunnerRuntimeHealth{}) || len(unsupported.paths()) != 1 {
		t.Fatal("unqualified server version was sampled")
	}
}

func TestRunnerRuntimeHealthCgroupPeakIsDistinctAndNotCarriedForward(t *testing.T) {
	for _, tc := range []struct {
		name    string
		memory  map[string]any
		peak    int64 // zero means absent in the projected result
		current int64
	}{
		{"measured pair", map[string]any{"usage": 3896, "usage_peak": 8500}, 8500, 3896},
		{"peak only", map[string]any{"usage": 0, "usage_peak": 8500}, 8500, 0},
		{"missing", map[string]any{"usage": 3896}, 0, 3896},
		{"null", map[string]any{"usage": 3896, "usage_peak": nil}, 0, 3896},
		{"zero", map[string]any{"usage": 3896, "usage_peak": 0}, 0, 3896},
		{"unavailable", map[string]any{"usage": 3896, "usage_peak": -1}, 0, 3896},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := runtimeHealthFixture(t, runnerHealthVersion, func(path string, n int, d map[string]any) {
				if !strings.HasSuffix(path, "/state") {
					return
				}
				d["cpu"] = nil
				if n == 1 {
					d["memory"] = map[string]int64{"usage": 3996, "usage_peak": 8400}
				} else {
					d["memory"] = tc.memory
				}
			})
			f.pin(t)
			got, err := f.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected)
			match := func(got *int64, want int64) bool {
				if want == 0 {
					return got == nil
				}
				return got != nil && *got == want
			}
			if err != nil || !match(got.MemoryCgroupPeakBytes, tc.peak) || !match(got.MemoryCurrentBytes, tc.current) || got.CPUCumulativeNanoseconds != nil {
				t.Fatalf("peak/current unavailable or conflated: %+v / %v", got, err)
			}
			wire, err := json.Marshal(got)
			if err != nil || strings.Contains(string(wire), "memory_cgroup_peak_bytes") != (tc.peak > 0) {
				t.Fatalf("incorrect peak projection: %s / %v", wire, err)
			}
		})
	}
}

func TestRunnerRuntimeHealthRefusesInconsistentCgroupPeak(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second map[string]any
	}{
		{"below current", map[string]any{"usage": 6000}, map[string]any{"usage": 7400, "usage_peak": 7000}},
		{"below earlier current", map[string]any{"usage": 7500}, map[string]any{"usage": 6000, "usage_peak": 7000}},
		{"peak decreases", map[string]any{"usage": 6000, "usage_peak": 7500}, map[string]any{"usage": 6000, "usage_peak": 7000}},
		{"wrong type", nil, map[string]any{"usage_peak": "7500"}},
		{"invalid negative", nil, map[string]any{"usage_peak": -2}},
		{"alias", nil, map[string]any{"usage_peak": 7500, "Usage_Peak": 7501}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := runtimeHealthFixture(t, runnerHealthVersion, func(path string, n int, d map[string]any) {
				if strings.HasSuffix(path, "/state") {
					if n == 1 {
						d["memory"] = tc.first
					} else {
						d["memory"] = tc.second
					}
				}
			})
			f.pin(t)
			if got, err := f.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected); !errors.Is(err, ErrResponse) || got != (RunnerRuntimeHealth{}) {
				t.Fatalf("inconsistent peak returned metrics: %+v / %v", got, err)
			}
		})
	}
}
