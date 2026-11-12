package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func placementFixture(t *testing.T) *SQLiteStore {
	t.Helper()
	config := fixture(time.Now())
	config.Global = Resources{CPU: 2000, RAM: 2048, Runners: 2}
	for id, h := range config.Hosts {
		h.Budget = Resources{CPU: 1000, RAM: 1024, Runners: 1}
		h.Mode = "enabled"
		h.Healthy = true
		config.Hosts[id] = h
	}
	p := config.Pools["app"]
	p.Hosts = []string{"a", "b"}
	p.Budget = config.Global
	config.Pools["app"] = p
	s := sqliteFixtureStore(t, config, false)
	fence, bindings := bindingFixture(t, s)
	if err := s.BindHostIdentities(context.Background(), fence, bindings); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReconciliation(context.Background(), fence, InventorySnapshot{Hosts: observations(config, s.now())}); err != nil {
		t.Fatal(err)
	}
	return s
}
func placementRequest(name string) PlacementRequest {
	execution := ExecutionImage{Controller: "controller", Name: name, Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("a", 64), Type: "container", Profiles: []string{"runner"}, Arch: "amd64"}
	return PlacementRequest{Key: executionKey(execution, "app"), Pool: "app", Resources: Resources{CPU: 1000, RAM: 1024, Runners: 1}, Execution: execution, Hosts: []string{"b", "a"}, Strategy: "first_fit"}
}
func TestPlacementChoosesSecondHostWithinAtomicBudget(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	first, err := s.AdmitPlacement(ctx, placementRequest("first"))
	if err != nil || first.Request.Host != "a" || first.HostBindingSequence <= 0 {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := s.AdmitPlacement(ctx, placementRequest("second"))
	if err != nil || second.Request.Host != "b" {
		t.Fatalf("second %+v %v", second, err)
	}
	if _, err := s.AdmitPlacement(ctx, placementRequest("third")); !errors.Is(err, ErrDenied) {
		t.Fatalf("overflow %v", err)
	}
	state := sqliteSnapshot(t, s)
	if len(state.Reservations) != 2 || state.Allocated.Global.CPU != 2000 {
		t.Fatal("budget skew")
	}
}
func TestPlacementReplayKeepsOriginalExecutionAcrossPolicyAndModeChanges(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	req := placementRequest("same")
	original, err := s.AdmitPlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginReconciliation(ctx); err != nil {
		t.Fatal(err)
	}
	req.Hosts = []string{"b"}
	req.Resources = Resources{CPU: 2000, RAM: 4096, Runners: 1}
	req.Execution.Fingerprint = strings.Repeat("b", 64)
	req.Execution.Profiles = []string{"different"}
	req.Execution.Image = "new"
	req.Execution.Flavor = "larger"
	replay, err := s.AdmitPlacement(ctx, req)
	if err != nil || !replay.Replay {
		t.Fatalf("replay %+v %v", replay, err)
	}
	replay.Replay = false
	if !reflect.DeepEqual(original, replay) {
		t.Fatalf("reinterpreted %+v != %+v", original, replay)
	}
	reopened, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.LookupExecutionPlan(ctx, original.Request.Key)
	if err != nil || !reflect.DeepEqual(loaded, original) {
		t.Fatalf("restart changed plan %+v %v", loaded, err)
	}
	for _, query := range []string{"UPDATE execution_plans SET payload='{}'", "DELETE FROM execution_plans"} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatal("plan mutable")
		}
	}
}
func TestPlacementSeparateHandlesContendForGlobalAndHostCaps(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	other, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	gate := make(chan struct{})
	var mu sync.Mutex
	accepted := []ExecutionPlan{}
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			store := s
			if i%2 == 1 {
				store = other
			}
			p, err := store.AdmitPlacement(ctx, placementRequest(fmt.Sprintf("contender-%d", i)))
			if err == nil {
				mu.Lock()
				accepted = append(accepted, p)
				mu.Unlock()
			} else if !errors.Is(err, ErrDenied) {
				t.Errorf("unexpected %v", err)
			}
		}(i)
	}
	close(gate)
	wg.Wait()
	if len(accepted) != 2 || accepted[0].Request.Host == accepted[1].Request.Host {
		t.Fatalf("capacity or host skew %+v", accepted)
	}
	state := sqliteSnapshot(t, s)
	if state.Allocated.Global.CPU != 2000 || len(state.Reservations) != 2 {
		t.Fatal("lost charge")
	}
}
func TestPlacementCancellationTombstoneRetainsImmutablePlanAndPreventsReuse(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	req := placementRequest("cancel")
	p, err := s.AdmitPlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelUndispatched(ctx, p.Request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupExecutionPlan(ctx, req.Key); err != nil {
		t.Fatal("lost historical plan")
	}
	req.Hosts = []string{"b"}
	if _, err := s.AdmitPlacement(ctx, req); !errors.Is(err, ErrCancelled) {
		t.Fatalf("resurrected %v", err)
	}
	if _, err := s.ClaimDispatchBound(ctx, p.Request, sqliteSnapshot(t, s).HostBindings[p.Request.Host]); !errors.Is(err, ErrCancelled) {
		t.Fatalf("claim resurrected %v", err)
	}
}
func TestPlacementUnknownCreateNeverMovesToAnotherHost(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	req := placementRequest("unknown")
	p, err := s.AdmitPlacement(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	binding := sqliteSnapshot(t, s).HostBindings[p.Request.Host]
	if _, err := s.ExecuteDispatchBound(ctx, p.Request, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		return DispatchReceipt{State: "unknown"}, errors.New("lost response")
	}); err == nil {
		t.Fatal("expected uncertain error")
	}
	req.Hosts = []string{"b"}
	replay, err := s.AdmitPlacement(ctx, req)
	if err != nil || replay.Request.Host != p.Request.Host {
		t.Fatal("unknown moved")
	}
	calls := 0
	if _, err := s.ExecuteDispatchBound(ctx, replay.Request, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		calls++
		return DispatchReceipt{State: "confirmed"}, nil
	}); !errors.Is(err, ErrDispatchClaimed) || calls != 0 {
		t.Fatalf("redispatched %d %v", calls, err)
	}
}
func TestPlacementAuditFailureRollsBackReservationAndExecutionPlan(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`CREATE TRIGGER placement_audit_failure BEFORE INSERT ON audit WHEN NEW.kind='placement_reserved' BEGIN SELECT RAISE(ABORT,'fixture'); END;`); err != nil {
		t.Fatal(err)
	}
	req := placementRequest("rollback")
	if _, err := s.AdmitPlacement(ctx, req); err == nil {
		t.Fatal("ignored audit")
	}
	if _, err := s.LookupExecutionPlan(ctx, req.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial plan %v", err)
	}
	if len(sqliteSnapshot(t, s).Reservations) != 0 {
		t.Fatal("partial reservation")
	}
}
func TestPlacementRefusesLegacyChargesAndUnboundHosts(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	req := placementRequest("legacy")
	if _, err := s.Admit(ctx, Request{Key: req.Key, Pool: req.Pool, Host: "a", Resources: req.Resources}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitPlacement(ctx, req); !errors.Is(err, ErrExecutionPlan) {
		t.Fatalf("invented legacy plan %v", err)
	}
	other := sqliteFixtureStore(t, fixture(time.Now()), true)
	if _, err := other.AdmitPlacement(ctx, placementRequest("unbound")); !errors.Is(err, ErrDenied) {
		t.Fatalf("unbound %v", err)
	}
}

func TestPlacementPlannedDispatchRejectsAlteredExecutionBeforeCallback(t *testing.T) {
	for _, mutation := range []string{"image", "profile", "resource", "binding", "host", "created"} {
		t.Run(mutation, func(t *testing.T) {
			s := placementFixture(t)
			ctx := context.Background()
			p, err := s.AdmitPlacement(ctx, placementRequest("planned"))
			if err != nil {
				t.Fatal(err)
			}
			binding := sqliteSnapshot(t, s).HostBindings[p.Request.Host]
			switch mutation {
			case "image":
				p.Execution.Fingerprint = strings.Repeat("b", 64)
			case "profile":
				p.Execution.Profiles = []string{"other"}
			case "resource":
				p.Request.Resources.CPU++
			case "binding":
				p.HostBindingSequence++
			case "host":
				p.Request.Host = "b"
			case "created":
				p.CreatedAt = p.CreatedAt.Add(time.Second)
			}
			called := false
			if _, err := s.ExecutePlannedDispatch(ctx, p, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
				called = true
				return DispatchReceipt{State: "unknown"}, nil
			}); err == nil || called {
				t.Fatalf("altered callback %v %v", called, err)
			}
			if _, err := s.LookupDispatch(ctx, p.Request.Key); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("altered intent %v", err)
			}
		})
	}
}
func TestPlacementLeastRunnersUsesChargesNotOnlyRunningGuests(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	config := sqliteSnapshot(t, s).Config
	config.Global = Resources{CPU: 4000, RAM: 4096, Runners: 4}
	for id, h := range config.Hosts {
		h.Budget = config.Global
		config.Hosts[id] = h
	}
	p := config.Pools["app"]
	p.Budget = config.Global
	config.Pools["app"] = p
	// Policy updates are test-fixture-only here; no runtime management API used.
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE metadata SET config=?", encoded); err != nil {
		t.Fatal(err)
	}
	a, err := s.AdmitPlacement(ctx, placementRequest("first"))
	if err != nil {
		t.Fatal(err)
	}
	req := placementRequest("spread")
	req.Strategy = "least_runners"
	b, err := s.AdmitPlacement(ctx, req)
	if err != nil || a.Request.Host == b.Request.Host {
		t.Fatalf("did not count creating reservation %+v %+v %v", a, b, err)
	}
}

func TestPlacementBackupRestorePreserveExecutionAndPermanentRuntimeDenial(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	request := placementRequest("persist-plan")
	p, err := s.AdmitPlacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	cancelledRequest := placementRequest("cancel-plan")
	cancelled, err := s.AdmitPlacement(ctx, cancelledRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelUndispatched(ctx, cancelled.Request); err != nil {
		t.Fatal(err)
	}
	binding := sqliteSnapshot(t, s).HostBindings[p.Request.Host]
	if _, err := s.ExecutePlannedDispatch(ctx, p, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		return DispatchReceipt{State: "unknown", OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: p.Execution.Name}, errors.New("lost wait")
	}); err == nil {
		t.Fatal("expected unknown")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backup.db")
	restored := filepath.Join(dir, "restored.db")
	if err := s.BackupSQLite(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{backup, restored} {
		copy, err := OpenSQLite(path, Config{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := copy.LookupExecutionPlan(ctx, p.Request.Key)
		if err != nil || !reflect.DeepEqual(got, p) {
			t.Fatalf("plan lost %+v %v", got, err)
		}
		cancelledCopy, err := copy.LookupExecutionPlan(ctx, cancelled.Request.Key)
		if err != nil || !reflect.DeepEqual(cancelledCopy, cancelled) {
			t.Fatal("cancelled plan history lost")
		}
		if _, err := copy.AdmitPlacement(ctx, cancelledRequest); !errors.Is(err, ErrCancelled) {
			t.Fatalf("cancelled replay %v", err)
		}
		fence, err := copy.BeginReconciliation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		state := sqliteSnapshot(t, copy)
		if err := copy.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(state.Config, copy.now())}); err != nil {
			t.Fatal(err)
		}
		// Offline readiness cannot activate a historical runtime artifact.
		var id string
		if err := copy.db.QueryRow("SELECT lineage_id FROM ledger_lineage WHERE id=1").Scan(&id); err != nil {
			t.Fatal(err)
		}
		identity := filepath.Join(dir, filepath.Base(path)+".identity")
		if err := os.WriteFile(identity, []byte(id+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if runtime, err := OpenRuntimeSQLite(path, identity); !errors.Is(err, ErrRuntimeLineage) {
			if runtime != nil {
				runtime.Close()
			}
			t.Fatalf("archive activated %v", err)
		}
		if _, err := copy.ExecutePlannedDispatch(ctx, p, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
			t.Fatal("historical redispatch")
			return DispatchReceipt{}, nil
		}); !errors.Is(err, ErrDispatchClaimed) {
			t.Fatalf("unknown replay %v", err)
		}
		copy.Close()
	}
}
func TestPlacementOrphanSameNameOnDifferentHostCannotBeRecreated(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	req := placementRequest("orphan")
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, s)
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(state.Config, s.now()), Resources: []InventoryResource{{ID: req.Execution.Name, Host: "b", Pool: "app", State: "unknown", Resources: req.Resources}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitPlacement(ctx, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("orphan duplicated %v", err)
	}
	if len(sqliteSnapshot(t, s).Reservations) != 0 {
		t.Fatal("orphan gained new plan")
	}
}

func TestPlacementCLIBackupPreservesExactOriginalExecution(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	p, err := s.AdmitPlacement(ctx, placementRequest("cli-plan"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "capacity-ledger")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", binary, "../../cmd/capacity-ledger")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s %v", out, err)
	}
	backup := filepath.Join(sqliteBackupDir(t), "backup.db")
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	for _, args := range [][]string{{"backup", "--source", s.path, "--destination", backup}, {"restore", "--source", backup, "--destination", restored, "--old-writers-fenced"}} {
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
			t.Fatalf("CLI %s %v", out, err)
		}
	}
	for _, path := range []string{backup, restored} {
		copy, err := OpenSQLite(path, Config{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := copy.LookupExecutionPlan(ctx, p.Request.Key)
		if err != nil || !reflect.DeepEqual(got, p) {
			t.Fatalf("CLI plan %+v %v", got, err)
		}
		copy.Close()
	}
}
func TestPlacementRuntimeMigratesQualifiedV6WithoutInventingOldPlans(t *testing.T) {
	path, identity := runtimeFixture(t)
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE execution_plans; PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := OpenRuntimeSQLite(path, identity)
	if err != nil {
		t.Fatalf("refused qualified6 %v", err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("migration %d %v", version, err)
	}
	if sqliteSnapshot(t, store).Ready {
		t.Fatal("migration retained readiness")
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM execution_plans").Scan(&count); err != nil || count != 0 {
		t.Fatal("invented plans")
	}
}
func TestPlacementLegacyV6ChargeCannotAcquireNewPlanAfterMigration(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	req := placementRequest("legacy-v6")
	if _, err := s.Admit(ctx, Request{Key: req.Key, Pool: req.Pool, Host: "a", Resources: req.Resources}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE execution_plans; PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	path := s.path
	s.Close()
	upgraded, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	reconcileSQLite(t, upgraded, nil)
	if _, err := upgraded.AdmitPlacement(ctx, req); !errors.Is(err, ErrExecutionPlan) {
		t.Fatalf("blessed legacy execution %v", err)
	}
	if sqliteSnapshot(t, upgraded).Allocated.Global.CPU != 1000 {
		t.Fatal("lost old charge")
	}
}
func TestPlacementCorruptPlanCannotBeExportedAsValidArtifact(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	if _, err := s.AdmitPlacement(ctx, placementRequest("corrupt")); err != nil {
		t.Fatal(err)
	}
	// A trusted-owner corruption fixture is NOT a supported mutation API.
	if _, err := s.db.Exec("DROP TRIGGER execution_plans_immutable_update; UPDATE execution_plans SET payload='{}'"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sqliteBackupDir(t), "bad.db")
	if err := s.BackupSQLite(ctx, target); err == nil {
		t.Fatal("invalid plan exported")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid artifact published")
	}
}

func TestPlacementEligibilityIsNotHistoricalAccountingScope(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		t.Run(fmt.Sprintf("orphan-%v", orphan), func(t *testing.T) {
			s := placementFixture(t)
			ctx := context.Background()
			req := placementRequest("old-a")
			resource := InventoryResource{ID: req.Execution.Name, Host: "a", Pool: "app", Resources: req.Resources, State: "idle"}
			if !orphan {
				p, err := s.AdmitPlacement(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				resource.ReservationKey = p.Request.Key
			}
			config := sqliteSnapshot(t, s).Config
			pool := config.Pools["app"]
			pool.Hosts = []string{"b"}
			config.Pools["app"] = pool
			// A trusted offline activation fixture. pool_hosts is NOT a public admin API.
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE metadata SET config=?,ready=0,nonce=''", encoded); err != nil {
				t.Fatal(err)
			}
			fence, err := s.BeginReconciliation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(config, s.now()), Resources: []InventoryResource{resource}}); err != nil {
				t.Fatalf("eligibility hid charge %v", err)
			}
			state := sqliteSnapshot(t, s)
			if !state.Ready || len(state.Inventory) != 1 || state.Allocated.Hosts["a"].CPU != 1000 || state.Allocated.Global.CPU != 1000 {
				t.Fatal("historical/orphan charge lost")
			}
			next, err := s.AdmitPlacement(ctx, placementRequest("new-b"))
			if err != nil || next.Request.Host != "b" {
				t.Fatalf("eligibility bypass %+v %v", next, err)
			}
			if !orphan {
				old, err := s.LookupExecutionPlan(ctx, req.Key)
				if err != nil {
					t.Fatal(err)
				}
				binding := state.HostBindings["a"]
				called := false
				if _, err := s.ExecutePlannedDispatch(ctx, old, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
					called = true
					return DispatchReceipt{}, nil
				}); err == nil || called {
					t.Fatal("ineligible original host dispatched")
				}
			}
		})
	}
}

func TestPlacementActiveRuntimeRefusesLegacyPlanlessDispatchAPIs(t *testing.T) {
	path, identity := runtimeFixture(t)
	s, err := OpenRuntimeSQLite(path, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	fence, bindings := bindingFixture(t, s)
	if err := s.BindHostIdentities(ctx, fence, bindings); err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, s)
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(state.Config, s.now())}); err != nil {
		t.Fatal(err)
	}
	legacy := request("legacy-runtime")
	if _, err := s.Admit(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AdmitPlacement(ctx, placementRequest("planned-runtime"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	callback := func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		calls++
		return DispatchReceipt{State: "confirmed", OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: plan.Execution.Name}, nil
	}
	state = sqliteSnapshot(t, s)
	for _, req := range []Request{legacy, plan.Request} {
		binding := state.HostBindings[req.Host]
		if _, err := s.ClaimDispatch(ctx, req); !errors.Is(err, ErrExecutionPlan) {
			t.Fatalf("raw Claim %v", err)
		}
		if _, err := s.ClaimDispatchBound(ctx, req, binding); !errors.Is(err, ErrExecutionPlan) {
			t.Fatalf("raw BoundClaim %v", err)
		}
		if _, err := s.ExecuteDispatch(ctx, req, callback); !errors.Is(err, ErrExecutionPlan) {
			t.Fatalf("raw Execute %v", err)
		}
		if _, err := s.ExecuteDispatchBound(ctx, req, binding, callback); !errors.Is(err, ErrExecutionPlan) {
			t.Fatalf("raw BoundExecute %v", err)
		}
	}
	if calls != 0 {
		t.Fatal("legacy path invoked callback")
	}
	if _, err := s.ExecutePlannedDispatch(ctx, plan, state.HostBindings[plan.Request.Host], callback); err != nil || calls != 1 {
		t.Fatalf("qualified plan %d %v", calls, err)
	}
}
func TestPlacementProcessWorker(t *testing.T) {
	path := os.Getenv("CAPACITY_PLACEMENT_TEST_PATH")
	if path == "" {
		return
	}
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := placementRequest(os.Getenv("CAPACITY_PLACEMENT_TEST_NAME"))
	req.Pool = os.Getenv("CAPACITY_PLACEMENT_TEST_POOL")
	req.Key = executionKey(req.Execution, req.Pool)
	awaitProcessStart(t)
	if _, err := s.AdmitPlacement(context.Background(), req); err != nil && !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestPlacementSeparateProcessesShareCrossPoolHostAndGlobalBudgets(t *testing.T) {
	s := placementFixture(t)
	report := runProcessContenders(t, 12, "^TestPlacementProcessWorker$", func(i int) []string {
		pool := "app"
		if i%2 == 1 {
			pool = "api"
		}
		return []string{"CAPACITY_PLACEMENT_TEST_PATH=" + s.path, fmt.Sprintf("CAPACITY_PLACEMENT_TEST_NAME=process-placement-%d", i), "CAPACITY_PLACEMENT_TEST_POOL=" + pool}
	})
	if report.Processes != 12 || report.Waves != 3 || report.Peak != 4 {
		t.Fatalf("changed contention %+v", report)
	}
	state := sqliteSnapshot(t, s)
	if len(state.Reservations) != 2 || state.Allocated.Global.CPU != 2000 || state.Allocated.Hosts["a"].CPU != 1000 || state.Allocated.Hosts["b"].CPU != 1000 {
		t.Fatalf("process skew %+v", state.Allocated)
	}
	for key, reservation := range state.Reservations {
		plan, err := s.LookupExecutionPlan(context.Background(), key)
		if err != nil || plan.Request != reservation.Request || plan.HostBindingSequence <= 0 {
			t.Fatal("partial process plan")
		}
	}
}

func TestPlacementNoncanonicalPlanCannotMasqueradeBehindMatchingHash(t *testing.T) {
	for _, kind := range []string{"unknown", "duplicate", "whitespace"} {
		t.Run(kind, func(t *testing.T) {
			s := placementFixture(t)
			ctx := context.Background()
			req := placementRequest("canonical")
			if _, err := s.AdmitPlacement(ctx, req); err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := s.db.QueryRow("SELECT payload FROM execution_plans WHERE request_key=?", req.Key).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown":
				raw = "{\"ignored\":true," + raw[1:]
			case "duplicate":
				raw = "{\"request\":{}," + raw[1:]
			case "whitespace":
				raw = " " + raw
			}
			sum := sha256.Sum256([]byte(raw))
			if _, err := s.db.Exec("DROP TRIGGER execution_plans_immutable_update"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE execution_plans SET payload=?,payload_hash=? WHERE request_key=?", raw, hex.EncodeToString(sum[:]), req.Key); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LookupExecutionPlan(ctx, req.Key); !errors.Is(err, ErrExecutionPlan) {
				t.Fatalf("accepted ambiguousplan %v", err)
			}
		})
	}
}
