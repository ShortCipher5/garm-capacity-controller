package admission

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func stockAuthority() StockProviderAuthority {
	return StockProviderAuthority{Controller: "controller", PoolID: "app", ProviderName: "stock-provider", ClientFingerprint: strings.Repeat("c", 64)}
}
func stockDigest() string { return strings.Repeat("d", 64) }
func stockPlan(t *testing.T, s *SQLiteStore, name string) (ExecutionPlan, HostBinding) {
	t.Helper()
	issueStockTestGrant(t, s, name)
	p, e := s.AdmitStockProviderPlacement(context.Background(), placementRequest(name), stockAuthority(), stockDigest())
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.LookupHostBinding(context.Background(), p.Request.Host)
	if e != nil {
		t.Fatal(e)
	}
	return p, b
}
func stockSuccess(context.Context, DispatchPermit) (DispatchReceipt, error) {
	return DispatchReceipt{State: "confirmed", OperationID: "11111111-1111-4111-8111-111111111111", ResourceID: "stock-runner"}, nil
}

func TestStockOriginSharesCapacityAndFreezesProvenance(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	p, _ := stockPlan(t, s, "stock")
	if _, e := s.AdmitPlacement(ctx, placementRequest("legacy")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.AdmitStockProviderPlacement(ctx, placementRequest("overflow"), stockAuthority(), stockDigest()); !errors.Is(e, ErrDenied) {
		t.Fatalf("oversubscription: %v", e)
	}
	if got := sqliteSnapshot(t, s).Allocated.Global.Runners; got != 2 {
		t.Fatalf("charge=%d", got)
	}
	if _, e := s.AdmitPlacement(ctx, placementRequest("stock")); !errors.Is(e, ErrStockProviderOrigin) {
		t.Fatalf("stock adopted as legacy: %v", e)
	}
	if _, e := s.AdmitStockProviderPlacement(ctx, placementRequest("legacy"), stockAuthority(), stockDigest()); !errors.Is(e, ErrStockProviderOrigin) {
		t.Fatalf("legacy adopted: %v", e)
	}
	retry, e := s.AdmitStockProviderPlacement(ctx, placementRequest("stock"), stockAuthority(), stockDigest())
	if e != nil || !retry.Replay || retry.Request != p.Request {
		t.Fatalf("replay: %+v %v", retry, e)
	}
	for _, field := range []string{"controller", "pool", "provider", "client", "digest"} {
		t.Run(field, func(t *testing.T) {
			a := stockAuthority()
			d := stockDigest()
			switch field {
			case "controller":
				a.Controller = "other"
			case "pool":
				a.PoolID = "other"
			case "provider":
				a.ProviderName = "other"
			case "client":
				a.ClientFingerprint = strings.Repeat("f", 64)
			case "digest":
				d = strings.Repeat("e", 64)
			}
			if _, e := s.AdmitStockProviderPlacement(ctx, placementRequest("stock"), a, d); !errors.Is(e, ErrStockProviderOrigin) {
				t.Fatalf("origin changed: %v", e)
			}
		})
	}
	for _, q := range []string{"DELETE FROM stock_provider_origins", "UPDATE stock_provider_origins SET payload='{}'"} {
		if _, e := s.db.Exec(q); e == nil {
			t.Fatal("mutable origin")
		}
	}
}

func TestStockOriginCanonicalDispatchBlocksCancellationAndReplay(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	p, b := stockPlan(t, s, "stock")
	var calls atomic.Int32
	callback := func(ctx context.Context, permit DispatchPermit) (DispatchReceipt, error) {
		calls.Add(1)
		if _, e := s.CancelUndispatched(ctx, p.Request); !errors.Is(e, ErrDispatchClaimed) {
			t.Errorf("cancelled dispatched charge: %v", e)
		}
		return stockSuccess(ctx, permit)
	}
	r, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), callback)
	if e != nil || r.StockProviderSequence <= 0 || r.OriginalPreparationSequence != 0 {
		t.Fatalf("dispatch: %+v %v", r, e)
	}
	if _, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), callback); !errors.Is(e, ErrDispatchClaimed) {
		t.Fatalf("redispatch: %v", e)
	}
	if _, e := s.ExecutePlannedDispatch(ctx, p, b, callback); !errors.Is(e, ErrStockProviderOrigin) {
		t.Fatalf("legacy dispatch stock: %v", e)
	}
	if calls.Load() != 1 {
		t.Fatal("not one-shot")
	}
	if _, e := s.ClaimRetirement(ctx, p.Request); !errors.Is(e, ErrRetirementAuthority) {
		t.Fatalf("release activated: %v", e)
	}
}

func TestStockOriginUnknownSurvivesRestartAndArchive(t *testing.T) {
	for _, mode := range []string{"error", "panic", "invalid-receipt", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s := placementFixture(t)
			p, b := stockPlan(t, s, "stock")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(context.Context, DispatchPermit) (DispatchReceipt, error) {
				switch mode {
				case "error":
					return DispatchReceipt{}, errors.New("secret backend error")
				case "panic":
					panic("secret panic")
				case "cancel":
					cancel()
					return DispatchReceipt{}, ctx.Err()
				default:
					return DispatchReceipt{State: "invented"}, nil
				}
			})
			if !errors.Is(e, ErrDispatchUnknown) {
				t.Fatalf("unknown: %v", e)
			}
			reopened, e := OpenSQLite(s.path, Config{})
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			if _, e := reopened.ExecuteStockProviderDispatch(context.Background(), p, b, stockAuthority(), stockDigest(), stockSuccess); !errors.Is(e, ErrDispatchClaimed) {
				t.Fatalf("restart replay: %v", e)
			}
			if _, e := reopened.CancelUndispatched(context.Background(), p.Request); !errors.Is(e, ErrDispatchClaimed) {
				t.Fatalf("released unknown: %v", e)
			}
			if got := sqliteSnapshot(t, reopened).Allocated.Global.Runners; got != 1 {
				t.Fatal("unknown charge lost")
			}
			target := filepath.Join(sqliteBackupDir(t), "archive.db")
			if e := s.BackupSQLite(context.Background(), target); e != nil {
				t.Fatal(e)
			}
			archive, e := OpenSQLite(target, Config{})
			if e != nil {
				t.Fatal(e)
			}
			defer archive.Close()
			r, e := archive.LookupDispatchReadOnly(context.Background(), p.Request.Key)
			if e != nil || r.StockProviderSequence <= 0 {
				t.Fatalf("archive provenance: %+v %v", r, e)
			}
		})
	}
}

func TestStockOriginCancellationClaimRaceAcrossHandles(t *testing.T) {
	for i := 0; i < 12; i++ {
		s := placementFixture(t)
		p, b := stockPlan(t, s, "stock")
		other, e := OpenSQLite(s.path, Config{})
		if e != nil {
			t.Fatal(e)
		}
		var calls atomic.Int32
		var cancelErr, dispatchErr error
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, cancelErr = s.CancelUndispatched(context.Background(), p.Request) }()
		go func() {
			defer wg.Done()
			<-start
			_, dispatchErr = other.ExecuteStockProviderDispatch(context.Background(), p, b, stockAuthority(), stockDigest(), func(ctx context.Context, p DispatchPermit) (DispatchReceipt, error) {
				calls.Add(1)
				return stockSuccess(ctx, p)
			})
		}()
		close(start)
		wg.Wait()
		other.Close()
		if cancelErr == nil {
			if calls.Load() != 0 || !errors.Is(dispatchErr, ErrCancelled) {
				t.Fatalf("cancel winner dispatched: %d %v", calls.Load(), dispatchErr)
			}
		} else if !errors.Is(cancelErr, ErrDispatchClaimed) || dispatchErr != nil || calls.Load() != 1 {
			t.Fatalf("invalid race %v %v %d", cancelErr, dispatchErr, calls.Load())
		}
	}
}

func TestActiveRuntimeDeniesStockAndRequiresOriginalPreparation(t *testing.T) {
	path, id := runtimeFixture(t)
	s, e := OpenRuntimeSQLite(path, id)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	fence, bindings := bindingFixture(t, s)
	if e = s.BindHostIdentities(context.Background(), fence, bindings); e != nil {
		t.Fatal(e)
	}
	snap := sqliteSnapshot(t, s)
	if e = s.CompleteReconciliation(context.Background(), fence, InventorySnapshot{Hosts: observations(snap.Config, s.now())}); e != nil {
		t.Fatal(e)
	}
	ctx := t.Context()
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	stock := placementRequest("stock")
	execution := stock.Execution
	execution.Name = ""
	batch := BrokerGrantBatch{
		ID: strings.Repeat("a", 64), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision,
		Grants: []BrokerGrantSpec{{Authority: stockAuthority(), Host: "a", HostBindingSequence: state.HostBindings["a"].Sequence,
			Resources: stock.Resources, Execution: execution, ExpiresAt: s.now().Add(time.Minute)}},
	}
	generic, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer generic.Close()
	for _, handle := range []*SQLiteStore{s, generic} {
		if _, e := handle.IssueBrokerGrants(ctx, batch); !errors.Is(e, ErrBrokerGrant) {
			t.Fatalf("installed ledger granted stock capacity: %v", e)
		}
		if _, e := handle.AdmitStockProviderPlacement(ctx, stock, stockAuthority(), stockDigest()); !errors.Is(e, ErrBrokerGrant) {
			t.Fatalf("installed ledger admitted stock placement: %v", e)
		}
	}
	after, e := s.ReadBrokerGrantState(ctx)
	if e != nil || len(after.Grants) != 0 || after.Allocated.Global != state.Allocated.Global || after.Sequence != state.Sequence {
		t.Fatalf("denied stock operation changed the ledger: %+v %v", after, e)
	}

	legacy, e := s.AdmitPlacement(context.Background(), placementRequest("legacy"))
	if e != nil {
		t.Fatal(e)
	}
	lb, e := s.LookupHostBinding(context.Background(), legacy.Request.Host)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExecutePlannedDispatch(context.Background(), legacy, lb, stockSuccess); !errors.Is(e, ErrOriginalPreparation) {
		t.Fatalf("original gate weakened: %v", e)
	}
	if _, e = s.ExecuteStockProviderDispatch(context.Background(), legacy, lb, stockAuthority(), stockDigest(), stockSuccess); !errors.Is(e, ErrStockProviderOrigin) {
		t.Fatalf("adopted legacy: %v", e)
	}
}

func TestStockOriginRollsBackWithAuditAndRejectsAlteredPlan(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	grant := issueStockTestGrant(t, s, "rollback")
	if _, e := s.db.Exec(`CREATE TRIGGER fail_stock_audit BEFORE INSERT ON audit WHEN NEW.kind='stock_provider_origin_recorded' BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.AdmitStockProviderPlacement(ctx, placementRequest("rollback"), stockAuthority(), stockDigest()); e == nil {
		t.Fatal("audit failure accepted")
	}
	if got := sqliteSnapshot(t, s).Allocated.Global.Runners; got != 1 {
		t.Fatal("grant charge lost or duplicated")
	}
	s.db.Exec("DROP TRIGGER fail_stock_audit")
	if e := s.RetireUnusedBrokerGrant(ctx, grant.ID, false); e != nil {
		t.Fatal(e)
	}
	p, b := stockPlan(t, s, "stock")
	p.Execution.Image = "changed"
	if _, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), stockSuccess); !errors.Is(e, ErrExecutionPlan) {
		t.Fatalf("altered plan: %v", e)
	}
	if _, e := s.LookupDispatchReadOnly(ctx, p.Request.Key); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("invalid intent recorded")
	}
}

func TestStockOriginFreshnessAndDrainStillBlockDispatch(t *testing.T) {
	for _, mode := range []string{"stale", "reconciliation"} {
		t.Run(mode, func(t *testing.T) {
			s := placementFixture(t)
			p, b := stockPlan(t, s, "stock")
			if mode == "stale" {
				old := s.now()
				s.now = func() time.Time { return old.Add(24 * time.Hour) }
			} else {
				if _, e := s.BeginReconciliation(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.ExecuteStockProviderDispatch(context.Background(), p, b, stockAuthority(), stockDigest(), stockSuccess); !errors.Is(e, ErrReconciliation) {
				t.Fatalf("gate: %v", e)
			}
		})
	}
}

// Historical migration fixtures must remove the newly added empty schema too.
// This restores the exact pre-v16 preparation trigger, not a production downgrade.
const stockMigrationDowngradeSQL = brokerGrantDowngradeSQL + `
DROP TRIGGER active_dispatch_requires_preparation;
DROP TRIGGER dispatch_stock_origin_exact;
DROP TRIGGER dispatch_stock_sequence_immutable;
DROP TRIGGER preparation_no_stock_origin;
DROP TABLE stock_provider_origins;
ALTER TABLE dispatches DROP COLUMN stock_provider_sequence;
CREATE TRIGGER active_dispatch_requires_preparation BEFORE INSERT ON dispatches
WHEN EXISTS(SELECT 1 FROM ledger_lineage WHERE id=1 AND runtime_kind='active')
BEGIN SELECT CASE WHEN NEW.original_preparation_sequence IS NULL OR NOT EXISTS(
 SELECT 1 FROM original_preparations p WHERE p.request_key=NEW.reservation_key
 AND p.record_sequence=NEW.original_preparation_sequence AND p.host_binding_sequence=NEW.host_binding_sequence)
 THEN RAISE(ABORT,'original preparation required') END; END;
`

func TestStockOriginConcurrentDispatchOneNativeAttempt(t *testing.T) {
	s := placementFixture(t)
	p, b := stockPlan(t, s, "stock")
	other, e := OpenSQLite(s.path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var calls, successes atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		store := s
		if i%2 == 0 {
			store = other
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.ExecuteStockProviderDispatch(context.Background(), p, b, stockAuthority(), stockDigest(), func(ctx context.Context, permit DispatchPermit) (DispatchReceipt, error) {
				calls.Add(1)
				return stockSuccess(ctx, permit)
			})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrDispatchClaimed) {
				t.Errorf("unexpected contention error %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 || successes.Load() != 1 {
		t.Fatalf("calls=%d successes=%d", calls.Load(), successes.Load())
	}
}

func TestStockOriginMissingHistoryCannotBecomePrepared(t *testing.T) {
	s := placementFixture(t)
	p, _ := stockPlan(t, s, "stock")
	if _, e := s.db.Exec("DROP TRIGGER stock_provider_origin_no_delete; DELETE FROM stock_provider_origins"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.AdmitPlacement(context.Background(), placementRequest("stock")); !errors.Is(e, ErrStockProviderOrigin) {
		t.Fatalf("missing stock history adopted: %v", e)
	}
	if e := sqliteCheckStockProviderOrigins(context.Background(), s); !errors.Is(e, ErrStockProviderOrigin) {
		t.Fatalf("corrupted archive accepted %v", e)
	}
	if _, e := s.LookupExecutionPlan(context.Background(), p.Request.Key); e != nil {
		t.Fatal("plan should remain inspectable", e)
	}
}

func TestStockOriginMigrationRetainsLegacyChargeAndClosesReadiness(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	p, e := s.AdmitPlacement(ctx, placementRequest("legacy"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec(stockMigrationDowngradeSQL + "PRAGMA user_version=15"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	migrated, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer migrated.Close()
	snap := sqliteSnapshot(t, migrated)
	if snap.Ready || snap.Allocated.Global.Runners != 1 {
		t.Fatalf("migration changed charge/readiness %+v", snap)
	}
	if _, e := migrated.AdmitStockProviderPlacement(ctx, placementRequest("legacy"), stockAuthority(), stockDigest()); !errors.Is(e, ErrStockProviderOrigin) {
		t.Fatalf("legacy adopted on migration %v", e)
	}
	if _, e := migrated.LookupExecutionPlan(ctx, p.Request.Key); e != nil {
		t.Fatal(e)
	}
}
