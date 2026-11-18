package admission

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

func retirementFixture(t *testing.T) *createEvidenceFixture {
	t.Helper()
	f := newCreateEvidenceFixture(t, "confirmed", true)
	if _, err := f.store.StoreCreateObservation(context.Background(), f.request, f.observe(t)); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRetirementIntentReplayRestartAndNoAuthority(t *testing.T) {
	ctx := context.Background()
	f := retirementFixture(t)
	before := sqliteSnapshot(t, f.store)
	first, err := f.store.RequestRetirement(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := f.store.RequestRetirement(ctx, f.request)
			if err != nil || !reflect.DeepEqual(got, first) {
				t.Error("nonexact intent replay", err)
			}
		}()
	}
	wg.Wait()
	if permit, err := f.store.ClaimRetirement(ctx, f.request); permit != nil || !errors.Is(err, ErrRetirementAuthority) {
		t.Fatal("authority manufactured")
	}
	if err := f.store.RecordRetirementSubmission(ctx, RetirementPermit{}, testOperationID); err == nil {
		t.Fatal("empty permit authorized")
	}
	wrong := f.request
	wrong.Resources.RAM++
	if _, err := f.store.RequestRetirement(ctx, wrong); err == nil {
		t.Fatal("rebound request")
	}
	after := sqliteSnapshot(t, f.store)
	if !reflect.DeepEqual(before.Allocated, after.Allocated) || !reflect.DeepEqual(before.Reservations, after.Reservations) || f.writes.Load() != 1 {
		t.Fatal("intent changed allocation or backend")
	}
	path := f.store.path
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovery, err := reopened.RecoverRetirement(ctx, f.request.Key)
	if err != nil || recovery.State != "authority_blocked" || !reflect.DeepEqual(recovery.Intent, first) {
		t.Fatal("restart lost held intent", err)
	}
}

func TestRetirementOriginalOperationRecoveryRemainsCharged(t *testing.T) {
	ctx := context.Background()
	f := retirementFixture(t)
	if _, err := f.store.RequestRetirement(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	// Simulate the durable state a FUTURE qualified host authority must commit.
	// There is intentionally no production path that issues this permit today.
	permit := RetirementPermit{key: f.request.Key, attempt: evidenceOtherGeneration}
	if _, err := f.store.db.Exec("INSERT INTO retirement_attempts(request_key,attempt_id,state) VALUES(?,?,'unknown')", permit.key, permit.attempt); err != nil {
		t.Fatal(err)
	}
	unknown, err := f.store.RecoverRetirement(ctx, permit.key)
	if err != nil || unknown.RecoveryAction != "retain_charge_resolve_lost_response_no_redispatch" {
		t.Fatal(unknown, err)
	}
	operation := "44444444-4444-4444-8444-444444444444"
	for i := 0; i < 2; i++ {
		if err := f.store.RecordRetirementSubmission(ctx, permit, operation); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.RecordRetirementSubmission(ctx, permit, testOperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("rebound operation", err)
	}
	submitted, err := f.store.RecoverRetirement(ctx, permit.key)
	if err != nil || submitted.OperationID != operation || submitted.RecoveryAction != "observe_original_operation_only" {
		t.Fatal(submitted, err)
	}
	// Actual authenticated fixture transport mints terminal observation, not JSON.
	f.kind.Store("Deleting instance")
	f.operation.Store(operation)
	identity, err := f.client.ObserveInstanceIdentity(ctx, incusapi.IdentityExpectation{Name: "runner-1", ControllerID: "controller-1", PoolID: f.request.Pool, ReservationKey: f.request.Key, CPUMillis: 1000, RAMMiB: 2048})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := f.client.CaptureTerminalOperation(ctx, identity, operation, incusapi.RecoveryDelete)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.StoreRetirementObservation(ctx, permit.key, observation); err != nil {
		t.Fatal(err)
	}
	if err := f.store.StoreRetirementObservation(ctx, permit.key, observation); err != nil {
		t.Fatal(err)
	}

	// A new capture may update local observation times, never rewrite stable
	// remote history for the same supposedly terminal operation.
	fresh, err := f.client.CaptureTerminalOperation(ctx, identity, operation, incusapi.RecoveryDelete)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.StoreRetirementObservation(ctx, permit.key, fresh); err != nil {
		t.Fatal("equivalent recapture rejected", err)
	}
	f.operationUpdated.Store("2026-01-01T00:00:01Z")
	changed, err := f.client.CaptureTerminalOperation(ctx, identity, operation, incusapi.RecoveryDelete)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.StoreRetirementObservation(ctx, permit.key, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting terminal history accepted", err)
	}
	recovery, err := f.store.RecoverRetirement(ctx, permit.key)
	if err != nil || recovery.State != "terminal_observed" || recovery.RecoveryAction != "retain_charge_await_qualified_finalizer" {
		t.Fatal(recovery, err)
	}
	if sqliteSnapshot(t, f.store).Allocated.Global.Runners != 1 || f.writes.Load() != 1 {
		t.Fatal("terminal observation freed charge or dispatched")
	}
	if _, err := f.store.LookupRelease(ctx, permit.key); err == nil {
		t.Fatal("release manufactured")
	}
	backup := filepath.Join(filepath.Dir(f.store.path), "retirement-backup.db")
	if err := f.store.BackupSQLite(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenSQLite(backup, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	again, err := restored.RecoverRetirement(ctx, permit.key)
	if err != nil || !reflect.DeepEqual(again, recovery) {
		t.Fatal("backup lost operation binding", err)
	}
	if _, err := restored.ClaimRetirement(ctx, f.request); !errors.Is(err, ErrRetirementAuthority) {
		t.Fatal("restore granted authority")
	}
	if _, err := f.store.db.Exec("DELETE FROM retirement_attempts WHERE request_key=?", permit.key); err == nil {
		t.Fatal("attempt erased")
	}
}

func TestRetirementMigrationFrom14RetainsReservations(t *testing.T) {
	ctx := context.Background()
	f := retirementFixture(t)
	// Remove only empty v15 schema to exercise a genuine v14-shaped ledger.
	for _, statement := range []string{stockMigrationDowngradeSQL, "DROP TABLE retirement_attempts", "DROP TABLE retirement_intents", "PRAGMA user_version=14"} {
		if _, err := f.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	path := f.store.path
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.RequestRetirement(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != sqliteCurrentVersion {
		t.Fatal("migration failed", err)
	}
	if sqliteSnapshot(t, reopened).Allocated.Global.Runners != 1 {
		t.Fatal("migration lost charge")
	}
	if _, err := reopened.ClaimRetirement(ctx, f.request); !errors.Is(err, ErrRetirementAuthority) {
		t.Fatal("migration enabled deletion")
	}
}
