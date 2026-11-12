package admission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sqliteFixtureStore(t *testing.T, config Config, ready bool) *SQLiteStore {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if ready {
		reconcileSQLite(t, s, nil)
	}
	return s
}

func observations(config Config, now time.Time) []HostObservation {
	var result []HostObservation
	for host, policy := range config.Hosts {
		result = append(result, HostObservation{Host: host, ObservedAt: now, Healthy: policy.Healthy, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true})
	}
	return result
}

func reconcileSQLite(t *testing.T, s *SQLiteStore, resources []InventoryResource) Fence {
	t.Helper()
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReconciliation(context.Background(), fence, InventorySnapshot{Hosts: observations(state.Config, s.now()), Resources: resources}); err != nil {
		t.Fatal(err)
	}
	return fence
}

func sqliteSnapshot(t *testing.T, s *SQLiteStore) SQLiteSnapshot {
	t.Helper()
	state, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func resource(id, key string, cpu, ram int64) InventoryResource {
	return InventoryResource{ID: id, Host: "a", Pool: "app", ReservationKey: key, Resources: Resources{CPU: cpu, RAM: ram, Runners: 1}, State: "idle"}
}

func TestSQLiteRequiresBaselineAndPersistsBarrier(t *testing.T) {
	config := fixture(time.Now())
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(context.Background(), request("before")); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("unreconciled database admitted: %v", err)
	}
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Admit(context.Background(), request("after-crash")); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("barrier lost on reopen: %v", err)
	}
	if err := reopened.CompleteReconciliation(context.Background(), fence, InventorySnapshot{Hosts: observations(config, time.Now())}); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Admit(context.Background(), request("after")); err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, reopened)
	if !state.Ready || len(state.Reservations) != 1 {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestSQLiteConcurrentHandlesCrossPoolBudget(t *testing.T) {
	config := fixture(time.Now())
	path := filepath.Join(t.TempDir(), "ledger.db")
	var stores []*SQLiteStore
	for i := 0; i < 4; i++ {
		s, err := OpenSQLite(path, config)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		stores = append(stores, s)
	}
	reconcileSQLite(t, stores[0], nil)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request(fmt.Sprint(i))
			if i%2 == 0 {
				r.Pool, r.Host = "api", "b"
			}
			_, err := stores[i%len(stores)].Admit(context.Background(), r)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrDenied) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	state := sqliteSnapshot(t, stores[0])
	if successes.Load() != 4 || state.Allocated.Global != config.Global || len(state.Reservations) != 4 {
		t.Fatalf("global cap bypass: admitted=%d allocated=%+v", successes.Load(), state.Allocated.Global)
	}
}

func TestSQLiteIdempotencyAndConflictAcrossReopen(t *testing.T) {
	config := fixture(time.Now())
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	reconcileSQLite(t, s, nil)
	first, err := s.Admit(context.Background(), request("stable"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenSQLite(path, Config{}) // Existing persisted policy wins.
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.BeginReconciliation(context.Background()); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Admit(context.Background(), request("stable"))
	if err != nil || !replay.Simulation || !replay.Replay || !replay.Reservation.CreatedAt.Equal(first.Reservation.CreatedAt) {
		t.Fatalf("replay lookup failed: %+v %v", replay, err)
	}
	changed := request("stable")
	changed.Host = "b"
	if _, err := s.Admit(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request accepted: %v", err)
	}
	if len(sqliteSnapshot(t, s).Reservations) != 1 {
		t.Fatal("duplicate reservation")
	}
}

func TestSQLiteReservationAndAuditAreAtomic(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	_, err := s.db.Exec(`CREATE TRIGGER inject_audit_failure BEFORE INSERT ON audit WHEN NEW.kind='reserved' BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(context.Background(), request("rolled-back")); err == nil {
		t.Fatal("failed audit returned admission")
	}
	state := sqliteSnapshot(t, s)
	if len(state.Reservations) != 0 || state.Allocated.Global.Runners != 0 {
		t.Fatal("reservation survived failed audit")
	}
	if _, err := s.db.Exec("DROP TRIGGER inject_audit_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(context.Background(), request("rolled-back")); err != nil {
		t.Fatal(err)
	}
	state = sqliteSnapshot(t, s)
	if state.Audit[0].Kind != "reserved" || state.Audit[0].RequestKey != "rolled-back" || len(state.Reservations) != 1 {
		t.Fatal("missing committed audit/reservation pair")
	}
}

func TestSQLiteCommitAmbiguityPoisonsHandle(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	// Commit inside the callback to simulate an already-visible commit followed
	// by an unavailable final commit result. The wrapper must never retry it.
	err := s.transaction(context.Background(), func(tx *sql.Tx) error {
		if err := sqliteAudit(tx, "injected_commit", "", s.now()); err != nil {
			return err
		}
		return tx.Commit()
	})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("ambiguous commit not classified: %v", err)
	}
	if _, err := s.Admit(context.Background(), request("blocked")); !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertain handle still admitted: %v", err)
	}
}

func TestSQLiteFenceOverlapConsumptionAndNonce(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	first, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, s)
	snapshot := InventorySnapshot{Hosts: observations(state.Config, time.Now())}
	if err := s.CompleteReconciliation(context.Background(), first, snapshot); !errors.Is(err, ErrFence) {
		t.Fatalf("old epoch accepted: %v", err)
	}
	wrong := second
	wrong.Nonce = first.Nonce
	if err := s.CompleteReconciliation(context.Background(), wrong, snapshot); !errors.Is(err, ErrFence) {
		t.Fatalf("wrong nonce accepted: %v", err)
	}
	if err := s.CompleteReconciliation(context.Background(), second, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReconciliation(context.Background(), second, snapshot); !errors.Is(err, ErrFence) {
		t.Fatalf("consumed epoch accepted: %v", err)
	}
}

func TestSQLiteRejectsIncompleteOrPreBarrierSnapshots(t *testing.T) {
	cases := map[string]func(*InventorySnapshot){
		"missing host": func(s *InventorySnapshot) { s.Hosts = s.Hosts[:1] },
		"duplicate host": func(s *InventorySnapshot) { s.Hosts[1] = s.Hosts[0] },
		"garm partial": func(s *InventorySnapshot) { s.Hosts[0].GarmComplete = false },
		"incus partial": func(s *InventorySnapshot) { s.Hosts[0].IncusComplete = false },
		"operations partial": func(s *InventorySnapshot) { s.Hosts[0].OperationsComplete = false },
		"unbounded": func(s *InventorySnapshot) { s.Hosts[0].Bounded = false },
		"old": func(s *InventorySnapshot) { s.Hosts[0].ObservedAt = time.Now().Add(-2 * time.Minute) },
		"future": func(s *InventorySnapshot) { s.Hosts[0].ObservedAt = time.Now().Add(time.Minute) },
		"pre-barrier": func(s *InventorySnapshot) { s.Hosts[0].ObservedAt = time.Now().Add(-time.Second) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := sqliteFixtureStore(t, fixture(time.Now()), true)
			fence, err := s.BeginReconciliation(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			state := sqliteSnapshot(t, s)
			snapshot := InventorySnapshot{Hosts: observations(state.Config, time.Now())}
			mutate(&snapshot)
			if err := s.CompleteReconciliation(context.Background(), fence, snapshot); !errors.Is(err, ErrReconciliation) {
				t.Fatalf("bad snapshot accepted: %v", err)
			}
			if _, err := s.Admit(context.Background(), request("blocked")); !errors.Is(err, ErrReconciliation) {
				t.Fatalf("failed reconciliation reopened admission: %v", err)
			}
		})
	}
}

func TestSQLiteGlobalFreshnessExpiryConsumesReadiness(t *testing.T) {
	config := fixture(time.Now())
	s := sqliteFixtureStore(t, config, true)
	fence := reconcileSQLite(t, s, nil)
	state := sqliteSnapshot(t, s)
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := s.Admit(context.Background(), request("expired")); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("expired view admitted: %v", err)
	}
	s.now = time.Now
	if _, err := s.Admit(context.Background(), request("clock-restored")); !errors.Is(err, ErrReconciliation) {
		t.Fatal("expired readiness silently restored")
	}
	if err := s.CompleteReconciliation(context.Background(), fence, InventorySnapshot{Hosts: observations(state.Config, time.Now())}); !errors.Is(err, ErrFence) {
		t.Fatalf("consumed fence reopened expired readiness: %v", err)
	}
	reconcileSQLite(t, s, nil)
	if _, err := s.Admit(context.Background(), request("fresh")); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteOtherHostStalenessBlocksGlobalBudget(t *testing.T) {
	config := fixture(time.Now())
	s := sqliteFixtureStore(t, config, false)
	now := time.Now()
	s.now = func() time.Time { return now }
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := InventorySnapshot{Hosts: observations(config, now)}
	for i := range snapshot.Hosts {
		if snapshot.Hosts[i].Host == "a" {
			snapshot.Hosts[i].ObservedAt = now.Add(30 * time.Second)
		}
	}
	s.now = func() time.Time { return now.Add(30 * time.Second) }
	if err := s.CompleteReconciliation(context.Background(), fence, snapshot); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(70 * time.Second) }
	if _, err := s.Admit(context.Background(), request("a-fresh-b-stale")); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("stale other host ignored: %v", err)
	}
}

func TestSQLiteLinkedInventoryDeduplicatesConservatively(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if _, err := s.Admit(context.Background(), request("linked")); err != nil {
		t.Fatal(err)
	}
	resources := []InventoryResource{resource("project/instance-1", "linked", 2000, 1024), resource("project/orphan", "", 500, 512)}
	reconcileSQLite(t, s, resources)
	state := sqliteSnapshot(t, s)
	want := Resources{CPU: 2500, RAM: 2560, Runners: 2}
	if state.Allocated.Global != want {
		t.Fatalf("wrong deduplicated allocation: got %+v want %+v", state.Allocated.Global, want)
	}
	resources[0].Resources.CPU = 500
	resources[0].Resources.RAM = 256
	resources[0].State = "failed"
	resources[1].State = "unknown"
	reconcileSQLite(t, s, resources)
	if sqliteSnapshot(t, s).Allocated.Global != want {
		t.Fatal("decreased observation reclaimed capacity")
	}
	reconcileSQLite(t, s, nil)
	state = sqliteSnapshot(t, s)
	if state.Allocated.Global != want || state.Inventory[0].State != "unknown" || state.Inventory[1].State != "unknown" {
		t.Fatal("missing inventory reclaimed capacity")
	}
}

func TestSQLiteAllReservationStatesRemainCharged(t *testing.T) {
	for _, state := range []string{"creating", "idle", "running", "failed", "unknown", "deleting"} {
		t.Run(state, func(t *testing.T) {
			config := fixture(time.Now())
			config.Global.Runners = 1
			s := sqliteFixtureStore(t, config, true)
			if _, err := s.Admit(context.Background(), request("one")); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordState(context.Background(), "one", state); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Admit(context.Background(), request("two")); !errors.Is(err, ErrDenied) {
				t.Fatalf("state %s released capacity: %v", state, err)
			}
			if err := s.RecordState(context.Background(), "one", "deleted"); err == nil {
				t.Fatal("unproven release accepted")
			}
		})
	}
}

func TestSQLiteIdentityConflictsLeaveBarrierClosed(t *testing.T) {
	cases := map[string]func([]InventoryResource) []InventoryResource{
		"duplicate resource": func(r []InventoryResource) []InventoryResource { return append(r, r[0]) },
		"duplicate link": func(r []InventoryResource) []InventoryResource { duplicate := r[0]; duplicate.ID = "project/other"; return append(r, duplicate) },
		"unlink": func(r []InventoryResource) []InventoryResource { r[0].ReservationKey = ""; return r },
		"pool changed": func(r []InventoryResource) []InventoryResource { r[0].Pool = "api"; return r },
		"unknown link": func(r []InventoryResource) []InventoryResource { r[0].ReservationKey = "missing"; return r },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := sqliteFixtureStore(t, fixture(time.Now()), true)
			if _, err := s.Admit(context.Background(), request("bound")); err != nil {
				t.Fatal(err)
			}
			original := []InventoryResource{resource("project/original", "bound", 1000, 2048)}
			reconcileSQLite(t, s, original)
			before := sqliteSnapshot(t, s)
			fence, err := s.BeginReconciliation(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			snapshot := InventorySnapshot{Hosts: observations(before.Config, time.Now()), Resources: mutate(original)}
			if err := s.CompleteReconciliation(context.Background(), fence, snapshot); err == nil {
				t.Fatal("conflicting identity accepted")
			}
			after := sqliteSnapshot(t, s)
			if after.Ready || after.Allocated.Global != before.Allocated.Global || len(after.Inventory) != len(before.Inventory) {
				t.Fatal("failed reconciliation changed accounting or reopened admission")
			}
		})
	}
}

func TestSQLiteOrphanCannotAcquireLateBinding(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if _, err := s.Admit(context.Background(), request("pending")); err != nil {
		t.Fatal(err)
	}
	orphan := resource("project/orphan", "", 1000, 2048)
	reconcileSQLite(t, s, []InventoryResource{orphan})
	before := sqliteSnapshot(t, s)
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	orphan.ReservationKey = "pending"
	err = s.CompleteReconciliation(context.Background(), fence, InventorySnapshot{Hosts: observations(before.Config, time.Now()), Resources: []InventoryResource{orphan}})
	if err == nil || sqliteSnapshot(t, s).Allocated.Global != before.Allocated.Global {
		t.Fatal("late binding silently deduplicated charged inventory")
	}
}

func TestSQLiteReconciliationPreservesPolicyAndDeniesUnhealthyHost(t *testing.T) {
	for _, mode := range []string{"draining", "disabled", "enabled"} {
		t.Run(mode, func(t *testing.T) {
			config := fixture(time.Now())
			h := config.Hosts["a"]
			h.Mode = mode
			if mode == "enabled" {
				h.Healthy = false
			}
			config.Hosts["a"] = h
			s := sqliteFixtureStore(t, config, true)
			if sqliteSnapshot(t, s).Config.Hosts["a"].Mode != mode {
				t.Fatal("observation changed policy mode")
			}
			if _, err := s.Admit(context.Background(), request("denied")); !errors.Is(err, ErrDenied) {
				t.Fatalf("ineligible host admitted: %v", err)
			}
		})
	}
}

func TestSQLiteOverflowAndPartialImportRollBack(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, s)
	const max = int64(1<<63 - 1)
	snapshot := InventorySnapshot{Hosts: observations(state.Config, time.Now()), Resources: []InventoryResource{resource("huge-1", "", max, 1), resource("huge-2", "", max, 1)}}
	if err := s.CompleteReconciliation(context.Background(), fence, snapshot); err == nil {
		t.Fatal("overflowed inventory accepted")
	}
	after := sqliteSnapshot(t, s)
	if after.Ready || len(after.Inventory) != 0 || after.Allocated.Global.Runners != 0 {
		t.Fatal("failed snapshot partially imported")
	}
}

func TestSQLiteBusyAndCanceledAdmissionFailClosed(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Admit(ctx, request("canceled")); err == nil {
		t.Fatal("canceled admission returned success")
	}
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	other, err := OpenSQLite(path, fixture(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.db.Exec("PRAGMA busy_timeout=20"); err != nil {
		t.Fatal(err)
	}
	lock, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Admit(context.Background(), request("busy")); err == nil {
		lock.Rollback()
		t.Fatal("busy database returned admission")
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(sqliteSnapshot(t, s).Reservations) != 0 {
		t.Fatal("failed admission persisted")
	}
}

func TestSQLiteRejectsUnknownSchemaAndUnsafeFile(t *testing.T) {
	config := fixture(time.Now())
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reopened, err := OpenSQLite(path, config); err == nil {
		reopened.Close()
		t.Fatal("future schema silently migrated")
	}
	unsafe := filepath.Join(t.TempDir(), "world-readable.db")
	if err := os.WriteFile(unsafe, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenSQLite(unsafe, config); err == nil {
		opened.Close()
		t.Fatal("world-readable database accepted")
	}
}

func TestSQLiteProcessWorker(t *testing.T) {
	path := os.Getenv("CAPACITY_SQLITE_TEST_PATH")
	if path == "" {
		return
	}
	s, err := OpenSQLite(path, fixture(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := request(os.Getenv("CAPACITY_SQLITE_TEST_KEY"))
	if os.Getenv("CAPACITY_SQLITE_TEST_OTHER") == "1" {
		r.Host, r.Pool = "b", "api"
	}
	if _, err := s.Admit(context.Background(), r); err != nil && !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestSQLiteCrossProcessAdmission(t *testing.T) {
	config := fixture(time.Now())
	config.Global.Runners = 2
	s := sqliteFixtureStore(t, config, true)
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteProcessWorker$")
			cmd.Env = append(os.Environ(), "CAPACITY_SQLITE_TEST_PATH="+path, "CAPACITY_SQLITE_TEST_KEY="+fmt.Sprint(i), fmt.Sprintf("CAPACITY_SQLITE_TEST_OTHER=%d", i%2))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("worker: %v %s", err, out)
			}
		}(i)
	}
	wg.Wait()
	state := sqliteSnapshot(t, s)
	if len(state.Reservations) != 2 || state.Allocated.Global.Runners != 2 {
		t.Fatalf("cross-process global cap bypass: %+v", state.Allocated)
	}
}
