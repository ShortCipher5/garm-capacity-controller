package admission

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func detectFixture(t *testing.T) *SQLiteStore {
	t.Helper()
	budget := Resources{CPU: 4000, RAM: 8192, Disk: 102400, Runners: 4}
	config := Config{Global: budget, Hosts: map[string]Host{"host-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func auditKinds(snapshot SQLiteSnapshot) map[string]int {
	kinds := map[string]int{}
	for _, event := range snapshot.Audit {
		kinds[event.Kind]++
	}
	return kinds
}

func TestEnrollHostRegistersNewHostInInitialSafeMode(t *testing.T) {
	ctx := context.Background()
	store := detectFixture(t)
	budget := Resources{CPU: 2000, RAM: 4096, Disk: 51200, Runners: 2}
	if err := store.EnrollHost(ctx, "host-b", budget, "disabled"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host, ok := snapshot.Config.Hosts["host-b"]
	if !ok || host.Mode != "disabled" || host.Healthy || host.Detected != nil || host.Budget != budget || host.ObservedAt.IsZero() {
		t.Fatalf("enrolled host policy: %+v", host)
	}
	if snapshot.Ready {
		t.Fatal("enrollment did not close readiness")
	}
	if auditKinds(snapshot)["host_enrolled"] != 1 {
		t.Fatal("enrollment audit missing")
	}
	if err := store.EnrollHost(ctx, "host-c", budget, "draining"); err != nil {
		t.Fatal(err)
	}
	if snapshot, err = store.Snapshot(ctx); err != nil || snapshot.Config.Hosts["host-c"].Mode != "draining" {
		t.Fatalf("draining enrollment rejected: %+v %v", snapshot.Config.Hosts["host-c"], err)
	}
}

func TestEnrollHostFailsOnDuplicateAndNeverAdopts(t *testing.T) {
	ctx := context.Background()
	store := detectFixture(t)
	budget := Resources{CPU: 2000, RAM: 4096, Runners: 2}
	if err := store.EnrollHost(ctx, "host-b", budget, "disabled"); err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conflictBudget := Resources{CPU: 9999, RAM: 9999, Runners: 9}
	if err := store.EnrollHost(ctx, "host-b", conflictBudget, "disabled"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate enrollment: %v", err)
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Config.Hosts["host-b"].Budget != budget {
		t.Fatal("duplicate enrollment silently adopted the host")
	}
	before.Audit, after.Audit = nil, nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected enrollment changed ledger state")
	}
}

func TestEnrollHostRejectsEnabledModeAndInvalidInput(t *testing.T) {
	ctx := context.Background()
	store := detectFixture(t)
	budget := Resources{CPU: 2000, RAM: 4096, Runners: 2}
	for name, enroll := range map[string]func() error{
		"enabled mode": func() error { return store.EnrollHost(ctx, "host-b", budget, "enabled") },
		"unknown mode": func() error { return store.EnrollHost(ctx, "host-b", budget, "drain") },
		"empty name":   func() error { return store.EnrollHost(ctx, "", budget, "disabled") },
		"control name": func() error { return store.EnrollHost(ctx, "bad\nname", budget, "disabled") },
		"long name":    func() error { return store.EnrollHost(ctx, string(make([]byte, 257)), budget, "disabled") },
		"negative cpu": func() error {
			return store.EnrollHost(ctx, "host-b", Resources{CPU: -1, RAM: 4096, Runners: 2}, "disabled")
		},
		"huge budget": func() error {
			return store.EnrollHost(ctx, "host-b", Resources{CPU: 9007199254740992, RAM: 4096, Runners: 2}, "disabled")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := enroll(); !errors.Is(err, ErrDenied) {
				t.Fatalf("enrollment accepted: %v", err)
			}
		})
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Config.Hosts) != 1 || auditKinds(snapshot)["host_enrolled"] != 0 {
		t.Fatal("invalid enrollment mutated policy or audited")
	}
}

func TestSetHostDetectedRecordsTotalsOnly(t *testing.T) {
	ctx := context.Background()
	store := detectFixture(t)
	budget := Resources{CPU: 2000, RAM: 4096, Runners: 2}
	if err := store.EnrollHost(ctx, "host-b", budget, "disabled"); err != nil {
		t.Fatal(err)
	}
	before := sqliteSnapshot(t, store).Config.Hosts["host-b"]
	detected := Resources{CPU: 8000, RAM: 32768, Disk: 524288}
	observedAt := time.Now().Add(-time.Second).UTC()
	if err := store.SetHostDetected(ctx, "host-b", &detected, observedAt); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := snapshot.Config.Hosts["host-b"]
	if host.Detected == nil || *host.Detected != detected || host.Healthy != before.Healthy || !host.ObservedAt.Equal(before.ObservedAt) || host.Mode != "disabled" || host.Budget != budget {
		t.Fatalf("detected totals not recorded: %+v", host)
	}
	if auditKinds(snapshot)["host_totals_observed"] != 1 {
		t.Fatal("detected audit missing")
	}
}

func TestSetHostDetectedLeavesReadinessFencesAndChargesUntouched(t *testing.T) {
	ctx := context.Background()
	store := detectFixture(t)
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = store.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: []HostObservation{{Host: "host-a", ObservedAt: time.Now(), Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Ready || before.Epoch != 1 {
		t.Fatalf("baseline not ready: %+v", before)
	}
	detected := Resources{CPU: 8000, RAM: 32768}
	if err := store.SetHostDetected(ctx, "host-a", &detected, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Ready || after.Epoch != before.Epoch || !reflect.DeepEqual(after.Allocated, before.Allocated) || after.Config.Global != before.Config.Global {
		t.Fatal("detected totals changed readiness/fence/accounting")
	}
	if after.Config.Hosts["host-a"].Detected == nil {
		t.Fatal("detected totals not stored")
	}
}

func TestSetHostDetectedRejectsStaleFutureNegativeAndUnknown(t *testing.T) {
	ctx := context.Background()
	store := detectFixture(t)
	budget := Resources{CPU: 2000, RAM: 4096, Runners: 2}
	if err := store.EnrollHost(ctx, "host-b", budget, "disabled"); err != nil {
		t.Fatal(err)
	}
	detected := Resources{CPU: 8000, RAM: 32768}
	for name, call := range map[string]func() error{
		"unknown host": func() error { return store.SetHostDetected(ctx, "host-missing", &detected, time.Now()) },
		"stale":        func() error { return store.SetHostDetected(ctx, "host-b", &detected, time.Now().Add(-2*time.Minute)) },
		"future":       func() error { return store.SetHostDetected(ctx, "host-b", &detected, time.Now().Add(time.Minute)) },
		"zero time":    func() error { return store.SetHostDetected(ctx, "host-b", &detected, time.Time{}) },
		"negative": func() error {
			negative := Resources{CPU: -1}
			return store.SetHostDetected(ctx, "host-b", &negative, time.Now())
		},
		"control name": func() error { return store.SetHostDetected(ctx, "bad\nname", &detected, time.Now()) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrDenied) {
				t.Fatalf("SetHostDetected accepted: %v", err)
			}
		})
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Config.Hosts["host-b"].Detected != nil || snapshot.Config.Hosts["host-b"].Healthy || auditKinds(snapshot)["host_totals_observed"] != 0 {
		t.Fatal("rejected detected update mutated policy or audited")
	}
}

func TestSetHostDetectedNilClearsTotals(t *testing.T) {
	ctx := context.Background()
	store := detectFixture(t)
	budget := Resources{CPU: 2000, RAM: 4096, Runners: 2}
	if err := store.EnrollHost(ctx, "host-b", budget, "disabled"); err != nil {
		t.Fatal(err)
	}
	detected := Resources{CPU: 8000, RAM: 32768}
	if err := store.SetHostDetected(ctx, "host-b", &detected, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHostDetected(ctx, "host-b", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Config.Hosts["host-b"].Detected != nil {
		t.Fatal("nil detected did not clear totals")
	}
}
