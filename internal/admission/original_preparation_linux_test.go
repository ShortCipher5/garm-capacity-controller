package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
)

// Real SQLite receipt tests. Runtime route/source authentication and actual Garm
// visibility are separately qualified, never inferred from this trusted helper.
func originalReceiptFixture(t *testing.T) (*SQLiteStore, ExecutionPlan, garmprotocol.PreparationRequest, PreparationAuthority) {
	t.Helper()
	s := placementFixture(t)
	p, err := s.AdmitPlacement(context.Background(), placementRequest("original"))
	if err != nil {
		t.Fatal(err)
	}
	r := garmprotocol.PreparationRequest{Protocol: garmprotocol.PreparationVersion, Name: p.Execution.Name, PoolID: p.Request.Pool, InstanceID: "11111111-1111-4111-8111-111111111111", AgentID: 42, Generation: 0, ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github-fixture", JITEnabled: true}
	a := PreparationAuthority{Controller: p.Execution.Controller, SourceScopeFingerprint: bindingHash("configured endpoint route"), ClientFingerprint: bindingHash("authenticated route certificate"), Role: "source", PoolID: r.PoolID, ProviderName: r.ProviderName, EntityType: r.EntityType, EntityID: r.EntityID, ForgeEndpointName: r.ForgeEndpointName, Generation: r.Generation, JITEnabled: r.JITEnabled}
	return s, p, r, a
}
func TestOriginalPreparationPersistsBeforePublicationWithoutOpeningAdmission(t *testing.T) {
	s, p, r, a := originalReceiptFixture(t)
	ctx := context.Background()
	if _, err := s.BeginReconciliation(ctx); err != nil {
		t.Fatal(err)
	}
	before := sqliteSnapshot(t, s)
	receipt, err := s.RecordOriginalPreparation(ctx, r, a)
	if err != nil || receipt.Replay || receipt.RecordSequence <= 0 || receipt.HostBindingSequence != p.HostBindingSequence {
		t.Fatalf("receipt %+v %v", receipt, err)
	}
	after := sqliteSnapshot(t, s)
	if after.Ready || after.Allocated.Global != before.Allocated.Global || len(after.Reservations) != len(before.Reservations) || len(after.Inventory) != 0 {
		t.Fatal("metadata ack allocated/released/opened")
	}
	if _, err := s.LookupDispatchReadOnly(ctx, p.Request.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("metadata ack granted dispatch")
	}
	loaded, err := s.LookupOriginalPreparation(ctx, p.Request.Key)
	if err != nil || !reflect.DeepEqual(loaded, receipt) {
		t.Fatalf("lost original %+v %v", loaded, err)
	}
	// Simulate a committed receipt whose transport ACK was lost. Exact retry returns
	// the SAME sequence/issuer, never a new registration or physical permission.
	rotated := a
	rotated.ClientFingerprint = bindingHash("other currently authorized route certificate")
	replay, err := s.RecordOriginalPreparation(ctx, r, rotated)
	if err != nil || !replay.Replay || replay.RecordSequence != receipt.RecordSequence || replay.Source.ClientFingerprint != a.ClientFingerprint {
		t.Fatalf("retry %+v %v", replay, err)
	}
	reopened, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ValidateOriginalPreparation(ctx, r, a)
	if err != nil || !got.Replay || got.RecordSequence != receipt.RecordSequence {
		t.Fatal("restart changed original")
	}
	for _, q := range []string{"UPDATE original_preparations SET source_scope='changed'", "DELETE FROM original_preparations"} {
		if _, err := s.db.Exec(q); err == nil {
			t.Fatal("mutable receipt")
		}
	}
}
func TestOriginalPreparationCannotChangeOriginalIdentityOrSourcePolicy(t *testing.T) {
	for _, kind := range []string{"agent", "row", "generation", "entity", "provider", "forge", "scope", "role", "provider-role", "issuer"} {
		t.Run(kind, func(t *testing.T) {
			s, _, r, a := originalReceiptFixture(t)
			ctx := context.Background()
			old, err := s.RecordOriginalPreparation(ctx, r, a)
			if err != nil {
				t.Fatal(err)
			}
			changed := r
			source := a
			switch kind {
			case "agent":
				changed.AgentID++
			case "row":
				changed.InstanceID = "44444444-4444-4444-8444-444444444444"
			case "generation":
				changed.Generation++
				source.Generation++
			case "entity":
				changed.EntityID = "44444444-4444-4444-8444-444444444444"
				source.EntityID = changed.EntityID
			case "provider":
				changed.ProviderName = "other"
				source.ProviderName = changed.ProviderName
			case "forge":
				changed.ForgeEndpointName = "other"
				source.ForgeEndpointName = changed.ForgeEndpointName
			case "scope":
				source.SourceScopeFingerprint = bindingHash("other source")
			case "role":
				source.Role = "admin"
			case "provider-role":
				source.Role = "provider"
			case "issuer":
				source.ClientFingerprint = "not-a-fingerprint"
			}
			if _, err := s.RecordOriginalPreparation(ctx, changed, source); err == nil {
				t.Fatal("identity overwritten")
			}
			got, err := s.LookupOriginalPreparation(ctx, old.Request.Key)
			if err != nil || !reflect.DeepEqual(got, old) {
				t.Fatal("failed request changed original")
			}
		})
	}
}
func TestOriginalPreparationRejectsRowAndOriginalAgentReuse(t *testing.T) {
	s, _, r, a := originalReceiptFixture(t)
	ctx := context.Background()
	if _, err := s.RecordOriginalPreparation(ctx, r, a); err != nil {
		t.Fatal(err)
	}
	p, err := s.AdmitPlacement(ctx, placementRequest("other-original"))
	if err != nil {
		t.Fatal(err)
	}
	other := r
	other.Name = p.Execution.Name
	if _, err := s.RecordOriginalPreparation(ctx, other, a); !errors.Is(err, ErrConflict) {
		t.Fatalf("row reuse %v", err)
	}
	other.InstanceID = "44444444-4444-4444-8444-444444444444"
	if _, err := s.RecordOriginalPreparation(ctx, other, a); !errors.Is(err, ErrConflict) {
		t.Fatalf("agent reuse %v", err)
	}
	other.AgentID = 43
	if _, err := s.RecordOriginalPreparation(ctx, other, a); err != nil {
		t.Fatal(err)
	}
}
func TestOriginalPreparationCancellationAndCommitRacesNeverResurrect(t *testing.T) {
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, p, r, a := originalReceiptFixture(t)
			ctx := context.Background()
			other, err := OpenSQLite(s.path, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			gate := make(chan struct{})
			errs := make(chan error, 2)
			go func() { <-gate; _, err := s.RecordOriginalPreparation(ctx, r, a); errs <- err }()
			go func() { <-gate; _, err := other.CancelUndispatched(ctx, p.Request); errs <- err }()
			close(gate)
			for j := 0; j < 2; j++ {
				err := <-errs
				if err != nil && !errors.Is(err, ErrCancelled) {
					t.Fatalf("race %v", err)
				}
			}
			if _, err := s.RecordOriginalPreparation(ctx, r, a); !errors.Is(err, ErrCancelled) {
				t.Fatalf("cancel resurrected %v", err)
			}
			if _, err := s.ValidateOriginalPreparation(ctx, r, a); !errors.Is(err, ErrCancelled) {
				t.Fatalf("cancel validated %v", err)
			}
			if _, err := s.AdmitPlacement(ctx, placementRequest("original")); !errors.Is(err, ErrCancelled) {
				t.Fatal("cancelled identity readmitted")
			}
			if sqliteSnapshot(t, s).Allocated.Global.CPU != 0 {
				t.Fatal("never-dispatched cancellation charged twice")
			}
		})
	}
}
func TestOriginalPreparationRefusesPostDispatchAndAmbiguousHistory(t *testing.T) {
	for _, kind := range []string{"dispatch-before", "dispatch-after", "inventory", "migration"} {
		t.Run(kind, func(t *testing.T) {
			s, p, r, a := originalReceiptFixture(t)
			ctx := context.Background()
			if kind == "dispatch-after" {
				if _, err := s.RecordOriginalPreparation(ctx, r, a); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "dispatch-before", "dispatch-after":
				binding := sqliteSnapshot(t, s).HostBindings[p.Request.Host]
				if _, err := s.ExecutePlannedDispatch(ctx, p, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
					return DispatchReceipt{State: "unknown"}, errors.New("lost response")
				}); err == nil {
					t.Fatal("expected uncertainty")
				}
			case "inventory":
				fence, err := s.BeginReconciliation(ctx)
				if err != nil {
					t.Fatal(err)
				}
				state := sqliteSnapshot(t, s)
				if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(state.Config, s.now()), Resources: []InventoryResource{{ID: r.Name, Host: p.Request.Host, Pool: p.Request.Pool, ReservationKey: p.Request.Key, Resources: p.Request.Resources, State: "unknown"}}}); err != nil {
					t.Fatal(err)
				}
			case "migration":
				if _, err := s.db.Exec("INSERT INTO original_preparation_history_exclusions VALUES(?)", p.Request.Key); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.RecordOriginalPreparation(ctx, r, a); err == nil {
				t.Fatal("history retroactively blessed")
			}
			if kind == "dispatch-after" {
				if _, err := s.ValidateOriginalPreparation(ctx, r, a); err != nil {
					t.Fatal("metadata matching must not be mistaken for no-dispatch proof")
				}
			}
			if sqliteSnapshot(t, s).Allocated.Global.CPU != 1000 {
				t.Fatal("history released")
			}
		})
	}
}
func TestOriginalPreparationAuditRollbackAndAmbiguousCommitFailClosed(t *testing.T) {
	for _, kind := range []string{"rollback", "commit"} {
		t.Run(kind, func(t *testing.T) {
			s, p, r, a := originalReceiptFixture(t)
			ctx := context.Background()
			statement := `CREATE TRIGGER preparation_audit_fail BEFORE INSERT ON audit WHEN NEW.kind='original_preparation_recorded' BEGIN SELECT RAISE(ABORT,'fixture'); END;`
			if kind == "commit" {
				statement = `CREATE TABLE preparation_commit_parent(id INTEGER PRIMARY KEY); CREATE TABLE preparation_commit_child(id INTEGER REFERENCES preparation_commit_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER preparation_commit_fail AFTER INSERT ON original_preparations BEGIN INSERT INTO preparation_commit_child VALUES(99); END;`
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			_, err := s.RecordOriginalPreparation(ctx, r, a)
			if err == nil {
				t.Fatal("ignored persistence failure")
			}
			if kind == "commit" {
				if !errors.Is(err, ErrUncertain) || !s.poisoned.Load() {
					t.Fatalf("commit not blocked %v", err)
				}
				if _, err := s.Snapshot(ctx); !errors.Is(err, ErrUncertain) {
					t.Fatal("poisoned writer recovered silently")
				}
			} else {
				if _, err := s.LookupOriginalPreparation(ctx, p.Request.Key); !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("partial audit receipt")
				}
				if sqliteSnapshot(t, s).Allocated.Global.CPU != 1000 {
					t.Fatal("rollback lost charge")
				}
			}
		})
	}
}
func TestOriginalPreparationConcurrentHandlesStoreExactlyOneOriginal(t *testing.T) {
	s, _, r, a := originalReceiptFixture(t)
	ctx := context.Background()
	other, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	var fresh atomic.Int32
	var sequence atomic.Int64
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 == 1 {
				store = other
			}
			receipt, err := store.RecordOriginalPreparation(ctx, r, a)
			if err != nil {
				t.Error(err)
				return
			}
			if !receipt.Replay {
				fresh.Add(1)
			}
			sequence.CompareAndSwap(0, receipt.RecordSequence)
			if sequence.Load() != receipt.RecordSequence {
				t.Error("different original sequence")
			}
		}(i)
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatalf("fresh %d", fresh.Load())
	}
}
func TestOriginalPreparationConsistentExportPreservesImmutableReceipt(t *testing.T) {
	s, p, r, a := originalReceiptFixture(t)
	ctx := context.Background()
	original, err := s.RecordOriginalPreparation(ctx, r, a)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sqliteBackupDir(t), "receipt.db")
	if err := s.BackupSQLite(ctx, path); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	got, err := copy.LookupOriginalPreparation(ctx, p.Request.Key)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("export receipt %+v %v", got, err)
	}
	if sqliteSnapshot(t, copy).Ready || sqliteSnapshot(t, copy).Allocated.Global.CPU != 1000 {
		t.Fatal("export opened/released")
	}
}

func TestOriginalPreparationReadCancellationCannotPoisonWriter(t *testing.T) {
	s, _, r, a := originalReceiptFixture(t)
	ctx := context.Background()
	if _, err := s.RecordOriginalPreparation(ctx, r, a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.ValidateOriginalPreparation(cancelled, r, a); err == nil {
			t.Fatal("cancelled read succeeded")
		}
	}
	if s.poisoned.Load() {
		t.Fatal("read token cancellation poisoned writer")
	}
	if _, err := s.RecordOriginalPreparation(ctx, r, a); err != nil {
		t.Fatalf("writer blocked %v", err)
	}
}
func TestOriginalPreparationCorruptionCannotBecomeSourceEvidence(t *testing.T) {
	for _, kind := range []string{"unknown-payload", "scope-column", "audit-subject", "agent-column"} {
		t.Run(kind, func(t *testing.T) {
			s, p, r, a := originalReceiptFixture(t)
			ctx := context.Background()
			receipt, err := s.RecordOriginalPreparation(ctx, r, a)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("DROP TRIGGER original_preparations_no_update"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown-payload":
				var raw string
				if err := s.db.QueryRow("SELECT payload FROM original_preparations").Scan(&raw); err != nil {
					t.Fatal(err)
				}
				raw = "{\"ignored\":true," + raw[1:]
				sum := sha256.Sum256([]byte(raw))
				if _, err := s.db.Exec("UPDATE original_preparations SET payload=?,payload_hash=?", raw, hex.EncodeToString(sum[:])); err != nil {
					t.Fatal(err)
				}
			case "scope-column":
				if _, err := s.db.Exec("UPDATE original_preparations SET source_scope=?", bindingHash("tampered")); err != nil {
					t.Fatal(err)
				}
			case "audit-subject":
				if _, err := s.db.Exec("UPDATE audit SET request_key='different' WHERE sequence=?", receipt.RecordSequence); err != nil {
					t.Fatal(err)
				}
			case "agent-column":
				if _, err := s.db.Exec("UPDATE original_preparations SET agent_id=43"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.LookupOriginalPreparation(ctx, p.Request.Key); !errors.Is(err, ErrOriginalPreparation) {
				t.Fatalf("corruption blessed %v", err)
			}
		})
	}
}

func TestOriginalPreparationV7MigrationNeverBlessesPreviouslyChargedHistory(t *testing.T) {
	s, p, r, a := originalReceiptFixture(t)
	ctx := context.Background()
	oldFence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"DROP TABLE pool_routes", "DROP INDEX pool_route_execution_history", "DROP TRIGGER inactive_pool_no_new_reservation", "DROP TRIGGER inactive_pool_no_new_dispatch", "DROP TRIGGER active_dispatch_requires_preparation", "DROP TRIGGER dispatch_preparation_sequence_immutable", "DROP TRIGGER original_preparations_no_update", "DROP TRIGGER original_preparations_no_delete", "DROP TRIGGER original_preparation_exclusions_no_update", "DROP TRIGGER original_preparation_exclusions_no_delete", "ALTER TABLE dispatches DROP COLUMN original_preparation_sequence", "DROP TABLE original_preparations", "DROP TABLE original_preparation_history_exclusions", "PRAGMA user_version=7"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(statement, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	snapshot := sqliteSnapshot(t, migrated)
	if snapshot.Ready || snapshot.Allocated.Global.CPU != 1000 || snapshot.Reservations[p.Request.Key].Request != p.Request {
		t.Fatal("migration changed charge or opened readiness")
	}
	if err := migrated.CompleteReconciliation(ctx, oldFence, InventorySnapshot{Hosts: observations(snapshot.Config, migrated.now())}); !errors.Is(err, ErrFence) {
		t.Fatal("migration reused old fence", err)
	}
	fresh, err := migrated.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrated.CompleteReconciliation(ctx, fresh, InventorySnapshot{Hosts: observations(snapshot.Config, migrated.now())}); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.RecordOriginalPreparation(ctx, r, a); !errors.Is(err, ErrOriginalPreparation) {
		t.Fatal("fresh inventory blessed legacy original source", err)
	}
}
func TestOriginalPreparationBackupChecksCanonicalHistoryAndDispatchReferences(t *testing.T) {
	for _, kind := range []string{"unknown", "cancelled", "corrupt-receipt", "corrupt-dispatch"} {
		t.Run(kind, func(t *testing.T) {
			s, p, b, r, row := preparedFixture(t)
			ctx := context.Background()
			switch kind {
			case "unknown":
				_, err := s.ExecutePreparedDispatch(ctx, p, b, r, row, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
					return DispatchReceipt{State: "unknown", OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: p.Execution.Name}, nil
				})
				if !errors.Is(err, ErrDispatchUnknown) {
					t.Fatal(err)
				}
			case "cancelled":
				if _, err := s.CancelUndispatched(ctx, p.Request); err != nil {
					t.Fatal(err)
				}
			case "corrupt-receipt":
				if _, err := s.db.Exec("DROP TRIGGER original_preparations_no_update; UPDATE original_preparations SET payload='{}'"); err != nil {
					t.Fatal(err)
				}
			case "corrupt-dispatch":
				if _, err := s.ExecutePreparedDispatch(ctx, p, b, r, row, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
					return DispatchReceipt{State: "unknown"}, nil
				}); !errors.Is(err, ErrDispatchUnknown) {
					t.Fatal(err)
				}
				if _, err := s.db.Exec("DROP TRIGGER dispatch_preparation_sequence_immutable; UPDATE dispatches SET original_preparation_sequence=host_binding_sequence"); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(sqliteBackupDir(t), "archive.db")
			err := s.BackupSQLite(ctx, path)
			if kind == "corrupt-receipt" || kind == "corrupt-dispatch" {
				if err == nil {
					t.Fatal("invalid canonical history exported")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			restoredPath := filepath.Join(sqliteBackupDir(t), "restored.db")
			if err := RestoreSQLite(ctx, path, restoredPath); err != nil {
				t.Fatal(err)
			}
			restored, err := OpenSQLite(restoredPath, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			loaded, err := restored.LookupOriginalPreparation(ctx, p.Request.Key)
			if err != nil || !reflect.DeepEqual(loaded, r) {
				t.Fatal("receipt lost", err)
			}
			state := sqliteSnapshot(t, restored)
			if state.Ready {
				t.Fatal("restore enabled runtime")
			}
			if kind == "unknown" {
				record, err := restored.LookupDispatchReadOnly(ctx, p.Request.Key)
				if err != nil || record.OriginalPreparationSequence != r.RecordSequence || record.OperationID == "" || state.Allocated.Global.CPU != 1000 {
					t.Fatal("unknown correlation/charge lost", err)
				}
			} else {
				if _, err := restored.ValidateOriginalPreparation(ctx, r.Original, r.Source); !errors.Is(err, ErrCancelled) {
					t.Fatal("cancelled original reauthorized", err)
				}
			}
		})
	}
}
