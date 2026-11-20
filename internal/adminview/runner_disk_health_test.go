package adminview

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fixedRunnerDiskHealth map[string][]RunnerDiskHealthReading

func (s fixedRunnerDiskHealth) SnapshotRunnerDiskHealth() map[string][]RunnerDiskHealthReading {
	return s
}

func TestRunnerDiskHealthOverviewIsOptionalScopedAndReadOnly(t *testing.T) {
	h, store, token := fixture(t)
	const route = "/admin/api/v1/overview"
	legacy := request(t, h, http.MethodGet, route, token)
	if legacy.Code != http.StatusOK || strings.Contains(legacy.Body.String(), "runner_disk_health") {
		t.Fatalf("no source should not add disk claims: %d", legacy.Code)
	}
	now := time.Now().UTC()
	h.SetRunnerDiskHealthSource(fixedRunnerDiskHealth{
		"host-a": {
			{StoragePool: "past-pool", ObservedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute), TotalBytes: 100, AvailableBytes: 99, TotalInodes: 100, AvailableInodes: 99},
			{StoragePool: "fixture-pool", ObservedAt: now, ExpiresAt: now.Add(time.Minute), TotalBytes: math.MaxInt64, AvailableBytes: 0, TotalInodes: 100, AvailableInodes: 0},
		},
		"other-host": {{StoragePool: "not-enrolled", ObservedAt: now, ExpiresAt: now.Add(time.Minute), TotalBytes: 12345, TotalInodes: 10}},
	})
	before, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if denied := request(t, h, http.MethodGet, route, ""); denied.Code != http.StatusUnauthorized || strings.Contains(denied.Body.String(), "fixture-pool") {
		t.Fatal("unauthenticated caller got disk overview")
	}
	read := request(t, h, http.MethodGet, route, token)
	if read.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", read.Code, read.Body.String())
	}
	var v Overview
	if err := json.Unmarshal(read.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Hosts) != 1 || len(v.Hosts[0].RunnerDiskHealth) != 2 || strings.Contains(read.Body.String(), "not-enrolled") {
		t.Fatal("incorrect host scope")
	}
	a, b := v.Hosts[0].RunnerDiskHealth[0], v.Hosts[0].RunnerDiskHealth[1]
	if a.StoragePool != "fixture-pool" || a.State != "current" || a.TotalBytes != "9223372036854775807" || a.AvailableBytes != "0" || a.TotalInodes != "100" || a.AvailableInodes != "0" ||
		b.StoragePool != "past-pool" || b.State != "stale" || b.TotalBytes != "" || b.AvailableInodes != "" {
		t.Fatalf("incorrect disk precision/freshness: %+v %+v", a, b)
	}
	after, err := store.ReadSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("overview did not stay read only", err)
	}
}

func TestRunnerDiskHealthSkipsUntrustworthyDisplayRows(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	good := RunnerDiskHealthReading{StoragePool: "fixture", ObservedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), TotalBytes: 1024, AvailableBytes: 100, TotalInodes: 100, AvailableInodes: 10}
	for _, mutate := range []func(*RunnerDiskHealthReading){
		func(r *RunnerDiskHealthReading) { r.StoragePool = "/host/root" },
		func(r *RunnerDiskHealthReading) { r.ObservedAt = now.Add(time.Second) },
		func(r *RunnerDiskHealthReading) { r.ObservedAt = time.Time{} },
		func(r *RunnerDiskHealthReading) { r.ExpiresAt = r.ObservedAt },
		func(r *RunnerDiskHealthReading) { r.ExpiresAt = r.ObservedAt.Add(5*time.Minute + time.Nanosecond) },
		func(r *RunnerDiskHealthReading) { r.AvailableBytes = r.TotalBytes + 1 },
		func(r *RunnerDiskHealthReading) { r.TotalInodes = 0 },
		func(r *RunnerDiskHealthReading) { r.AvailableInodes = -1 },
	} {
		bad := good
		mutate(&bad)
		if rows := projectRunnerDiskHealth([]RunnerDiskHealthReading{bad}, now); len(rows) != 0 {
			t.Fatalf("invalid evidence escaped %+v", good)
		}
	}
	if rows := projectRunnerDiskHealth([]RunnerDiskHealthReading{good, good}, now); len(rows) != 0 {
		t.Fatal("duplicate pool accepted")
	}
	good.ExpiresAt = now
	rows := projectRunnerDiskHealth([]RunnerDiskHealthReading{good}, now)
	if len(rows) != 1 || rows[0].State != "stale" || rows[0].AvailableBytes != "" {
		t.Fatal("expiry equality promoted stale data")
	}
}

func TestRunnerDiskUnmarshalableOptionalTimesCannotBreakOverview(t *testing.T) {
	h, _, token := fixture(t)
	const route = "/admin/api/v1/overview"
	// The first case reproduces the previous endpoint failure: a stale valid
	// interval in year -1 made time.Time.MarshalJSON fail for the whole view.
	early := time.Date(-1, time.January, 1, 0, 0, 0, 0, time.UTC)
	yearZero := time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC)
	yearOne := time.Date(1, time.January, 1, 0, 1, 0, 0, time.UTC)
	lastMinute := time.Date(9999, time.December, 31, 23, 59, 0, 0, time.UTC)
	for _, times := range [][2]time.Time{
		{early, early.Add(time.Minute)},
		{yearZero, yearZero.Add(time.Minute)},
		{lastMinute, lastMinute.Add(time.Minute)},
		{yearOne.Add(-2 * time.Minute), yearOne},
	} {
		h.SetRunnerDiskHealthSource(fixedRunnerDiskHealth{
			"host-a": {{StoragePool: "invalid-time", ObservedAt: times[0], ExpiresAt: times[1], TotalBytes: 2, AvailableBytes: 1, TotalInodes: 2, AvailableInodes: 1}},
		})
		got := request(t, h, http.MethodGet, route, token)
		if got.Code != http.StatusOK || strings.Contains(got.Body.String(), "runner_disk_health") || !strings.Contains(got.Body.String(), "host-a") {
			t.Fatalf("bad optional timestamps disrupted core overview: %v: %d %s", times, got.Code, got.Body.String())
		}
		if runnerDiskWireTime(times[0]) && runnerDiskWireTime(times[1]) {
			t.Fatal("fixture did not contain an unsupported UTC time")
		}
	}
	// Drive now into the final valid UTC year so the future observation check
	// cannot mask expiry validation.
	r := RunnerDiskHealthReading{StoragePool: "invalid-expiry", ObservedAt: lastMinute, ExpiresAt: lastMinute.Add(time.Minute), TotalBytes: 2, TotalInodes: 2}
	if got := projectRunnerDiskHealth([]RunnerDiskHealthReading{r}, lastMinute.Add(time.Second)); len(got) != 0 {
		t.Fatal("unmarshalable expiry escaped when the observation was current")
	}
}
