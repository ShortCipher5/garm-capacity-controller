package hostmetrics

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func TestNativeContainerReadsDoNotHideAnIndividualCPUReset(t *testing.T) {
	const project = "test-project"
	const created = "2026-01-01T00:00:00Z"
	const started = "2026-01-02T00:00:00Z"
	var phase atomic.Int32
	var stateReads atomic.Int32
	// The third aggregate rises despite A's reset. The fourth can record
	// again from the new baseline. The fifth replaces a guest at equal count.
	aCounters := [5]int64{60, 61, 1, 2, 3}
	bCounters := [5]int64{300, 301, 701, 702, 1000}
	server, tls, _ := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("project") != project {
			t.Error("request outside the existing project-scoped GET surface")
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		i := phase.Load()
		second := "guest-b"
		if i == 4 {
			second = "guest-c"
		}
		var payload any
		switch r.URL.Path {
		case "/1.0":
			payload = map[string]any{"api_version": "1.0", "auth": "trusted", "environment": map[string]any{"server_clustered": false}}
		case "/1.0/resources":
			payload = map[string]any{"cpu": map[string]int{"total": 4}, "memory": map[string]int{"total": 1 << 30}}
		case "/1.0/instances":
			if r.URL.Query().Get("recursion") != "1" {
				t.Error("expected existing recursive inventory read")
			}
			rows := []map[string]any{}
			for _, id := range []string{"guest-a", second} {
				rows = append(rows, map[string]any{"name": id, "type": "container", "status": "Running", "created_at": created, "expanded_devices": map[string]any{"root": map[string]string{"type": "disk", "path": "/", "pool": "fixture"}}})
			}
			payload = rows
		case "/1.0/instances/guest-a/state", "/1.0/instances/" + second + "/state":
			stateReads.Add(1)
			pid, seconds := int64(1002), bCounters[i]
			if strings.Contains(r.URL.Path, "/guest-a/") {
				pid, seconds = 1001, aCounters[i]
			}
			payload = map[string]any{"status": "Running", "status_code": 103, "pid": pid, "started_at": started, "cpu": map[string]int64{"usage": seconds * int64(time.Second)}, "memory": map[string]int{"usage": 1 << 20}, "disk": map[string]any{"root": map[string]int{"usage": 1 << 20}}}
		default:
			t.Errorf("unexpected fixture request %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status_code": 200, "metadata": payload}); err != nil {
			t.Error(err)
		}
	}))
	client, err := incusapi.New(server.URL, project, tls)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	rec := &fakeRecorder{}
	collector, err := New(map[string]Source{"test-host": client}, rec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := range aCounters {
		phase.Store(int32(i))
		collector.collect(t.Context(), at.Add(time.Duration(i)*time.Minute))
	}
	rows := rec.recorded()
	if len(rows) != 2 || !rows[0].at.Equal(at.Add(time.Minute)) || !rows[1].at.Equal(at.Add(3*time.Minute)) ||
		rows[0].cpuUsedMillis != 33 || rows[1].cpuUsedMillis != 33 || rows[0].source != GuestStateSourceV2 || rows[1].source != GuestStateSourceV2 {
		t.Fatalf("native counters produced false continuity: %+v", rows)
	}
	if got := stateReads.Load(); got != 10 {
		t.Fatalf("changed guest state request budget: got %d want 10", got)
	}
}
