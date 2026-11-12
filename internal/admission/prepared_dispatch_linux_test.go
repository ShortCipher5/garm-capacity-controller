package admission

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func preparedFixture(t *testing.T) (*SQLiteStore, ExecutionPlan, HostBinding, OriginalPreparationReceipt, GarmRowObservation) {
	t.Helper()
	s, p, r, a := originalReceiptFixture(t)
	ctx := context.Background()
	receipt, err := s.RecordOriginalPreparation(ctx, r, a)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.LookupHostBinding(ctx, p.Request.Host)
	if err != nil {
		t.Fatal(err)
	}
	row := GarmRowObservation{RequestKey: p.Request.Key, SourceScopeFingerprint: a.SourceScopeFingerprint, InstanceID: r.InstanceID, Name: r.Name, PoolID: r.PoolID, ProviderName: r.ProviderName, AgentID: r.AgentID, Generation: r.Generation, ObservedAt: s.now(), PreparationShape: true, CreationEligible: true}
	return s, p, binding, receipt, row
}
func TestPreparedDispatchFreshRowAndOriginalReceiptComparedAtomically(t *testing.T) {
	for _, kind := range []string{"receipt-agent", "receipt-sequence", "row-id", "row-agent", "row-generation", "row-name", "row-provider", "row-scope", "row-key", "row-pool", "row-future", "row-stale", "row-zero-time", "row-not-eligible", "binding", "plan", "cancelled", "barrier"} {
		t.Run(kind, func(t *testing.T) {
			s, p, b, r, row := preparedFixture(t)
			ctx := context.Background()
			before := sqliteSnapshot(t, s).Allocated
			switch kind {
			case "receipt-agent":
				r.Original.AgentID++
			case "receipt-sequence":
				r.RecordSequence++
			case "row-id":
				row.InstanceID = "44444444-4444-4444-8444-444444444444"
			case "row-agent":
				row.AgentID++
			case "row-generation":
				row.Generation++
			case "row-name":
				row.Name = "other"
			case "row-provider":
				row.ProviderName = "other"
			case "row-scope":
				row.SourceScopeFingerprint = bindingHash("other")
			case "row-key":
				row.RequestKey = bindingHash("other")
			case "row-pool":
				row.PoolID = "other"
			case "row-future":
				row.ObservedAt = s.now().Add(time.Minute)
			case "row-stale":
				row.ObservedAt = s.now().Add(-time.Hour)
			case "row-zero-time":
				row.ObservedAt = time.Time{}
			case "row-not-eligible":
				row.CreationEligible = false
			case "binding":
				b.Sequence++
				p.HostBindingSequence = b.Sequence
			case "plan":
				p.Execution.Image = "revoked"
			case "cancelled":
				if _, err := s.CancelUndispatched(ctx, p.Request); err != nil {
					t.Fatal(err)
				}
			case "barrier":
				if _, err := s.BeginReconciliation(ctx); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			_, err := s.ExecutePreparedDispatch(ctx, p, b, r, row, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
				calls++
				return DispatchReceipt{State: "unknown"}, nil
			})
			if err == nil || calls != 0 {
				t.Fatalf("invalid %s got callback%d error%v", kind, calls, err)
			}
			if _, err := s.LookupDispatchReadOnly(ctx, p.Request.Key); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("invalid proof created intent", err)
			}
			if kind != "cancelled" && !reflect.DeepEqual(before, sqliteSnapshot(t, s).Allocated) {
				t.Fatal("denial lost charge")
			}
		})
	}
}
func TestPreparedDispatchSeparateHandlesRetryAndUnknownNeverCreateAgain(t *testing.T) {
	s, p, b, r, row := preparedFixture(t)
	ctx := context.Background()
	other, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var calls atomic.Int32
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			store := s
			if i%2 == 1 {
				store = other
			}
			_, err := store.ExecutePreparedDispatch(ctx, p, b, r, row, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
				calls.Add(1)
				return DispatchReceipt{State: "unknown", OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: p.Execution.Name}, errors.New("lost reply")
			})
			if err != nil && !errors.Is(err, ErrDispatchClaimed) && !errors.Is(err, ErrDispatchUnknown) {
				t.Error(err)
			}
		}(i)
	}
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("callbacks %d", calls.Load())
	}
	record, err := s.LookupDispatchReadOnly(ctx, p.Request.Key)
	if err != nil || record.State != "unknown" || record.OriginalPreparationSequence != r.RecordSequence || record.OperationID == "" {
		t.Fatalf("correlation %+v %v", record, err)
	}
	reopened, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.ExecutePreparedDispatch(ctx, p, b, r, row, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		t.Fatal("restarted unknown redispatched")
		return DispatchReceipt{}, nil
	}); !errors.Is(err, ErrDispatchClaimed) {
		t.Fatal(err)
	}
	if sqliteSnapshot(t, s).Allocated.Global.CPU != 1000 {
		t.Fatal("unknown released")
	}
}

func originalPreparationSnapshot(t *testing.T, s *SQLiteStore, p ExecutionPlan, receipt OriginalPreparationReceipt, row GarmRowObservation) (Fence, InventorySnapshot) {
	t.Helper()
	fence, snapshot := preparationSnapshot(t, s, p)
	row.ObservedAt = snapshot.Preparations[0].ObservedAt
	snapshot.Preparations[0].OriginalPreparationSequence = receipt.RecordSequence
	snapshot.Preparations[0].OriginalRow = row
	return fence, snapshot
}
func TestPreparedReconciliationRechecksOriginalSequenceRowFenceAndNoIntent(t *testing.T) {
	for _, kind := range []string{"valid", "sequence", "row-agent", "row-id", "row-time", "not-pending-shape", "cancelled", "already-dispatched", "previous-inventory", "source-history-excluded", "stale-fence", "incomplete-physical"} {
		t.Run(kind, func(t *testing.T) {
			s, p, b, r, row := preparedFixture(t)
			ctx := context.Background()
			if kind == "already-dispatched" {
				if _, err := s.ExecutePreparedDispatch(ctx, p, b, r, row, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
					return DispatchReceipt{State: "unknown"}, nil
				}); !errors.Is(err, ErrDispatchUnknown) {
					t.Fatal(err)
				}
			}
			if kind == "previous-inventory" {
				fence, snapshot := preparationSnapshot(t, s, p)
				snapshot.Preparations = nil
				snapshot.Resources = []InventoryResource{{ID: p.Execution.Name, ReservationKey: p.Request.Key, Pool: p.Request.Pool, Host: p.Request.Host, Resources: p.Request.Resources, State: "creating"}}
				if err := s.CompleteReconciliation(ctx, fence, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			fence, snapshot := originalPreparationSnapshot(t, s, p, r, row)
			proof := &snapshot.Preparations[0]
			switch kind {
			case "sequence":
				proof.OriginalPreparationSequence++
			case "row-agent":
				proof.OriginalRow.AgentID++
			case "row-id":
				proof.OriginalRow.InstanceID = "44444444-4444-4444-8444-444444444444"
			case "row-time":
				proof.OriginalRow.ObservedAt = proof.ObservedAt.Add(-time.Second)
			case "not-pending-shape":
				proof.OriginalRow.PreparationShape = false
			case "cancelled": // A cancellation cannot newly occur while this barrier is closed; inject the
				// real cancellation before rebuilding the barrier in the ready baseline.
				if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: snapshot.Hosts}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.CancelUndispatched(ctx, p.Request); err != nil {
					t.Fatal(err)
				}
				fence, snapshot = originalPreparationSnapshot(t, s, p, r, row)
			case "source-history-excluded":
				if _, err := s.db.Exec("INSERT INTO original_preparation_history_exclusions VALUES(?)", p.Request.Key); err != nil {
					t.Fatal(err)
				}
			case "stale-fence":
				if _, err := s.BeginReconciliation(ctx); err != nil {
					t.Fatal(err)
				}
			case "incomplete-physical":
				for i := range snapshot.Hosts {
					snapshot.Hosts[i].OperationsComplete = false
				}
			}
			err := s.CompleteReconciliation(ctx, fence, snapshot)
			if kind == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				state := sqliteSnapshot(t, s)
				if !state.Ready || state.Allocated.Global.CPU != 1000 || len(state.Inventory) != 0 {
					t.Fatal("source proof released/duplicated charge")
				}
				return
			}
			if err == nil {
				t.Fatal("invalid original source proof completed")
			}
			if sqliteSnapshot(t, s).Ready {
				t.Fatal("failed proof opened admission")
			}
		})
	}
}
