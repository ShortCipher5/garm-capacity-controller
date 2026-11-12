package admission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func preparationSnapshot(t *testing.T, s *SQLiteStore, plan ExecutionPlan) (Fence, InventorySnapshot) {
	t.Helper()
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	at := s.now()
	state := sqliteSnapshot(t, s)
	return fence, InventorySnapshot{Hosts: observations(state.Config, at), Preparations: []GarmPreparation{{Fence: fence, Request: plan.Request, Name: plan.Execution.Name, HostBindingSequence: plan.HostBindingSequence, ObservedAt: at}}}
}
func preparationFixture(t *testing.T) (*SQLiteStore, ExecutionPlan, Fence, InventorySnapshot) {
	t.Helper()
	s := placementFixture(t)
	plan, err := s.AdmitPlacement(context.Background(), placementRequest("preparation"))
	if err != nil {
		t.Fatal(err)
	}
	fence, snapshot := preparationSnapshot(t, s, plan)
	return s, plan, fence, snapshot
}
func assertPreparationCharge(t *testing.T, s *SQLiteStore, ready bool, runners int64) {
	t.Helper()
	state := sqliteSnapshot(t, s)
	if state.Ready != ready || state.Allocated.Global.Runners != runners {
		t.Fatalf("readiness/charge changed ready=%v allocation=%+v", state.Ready, state.Allocated)
	}
}
func TestGarmPreparationKeepsFullChargeAndPermitsOnlyOriginalPlannedDispatch(t *testing.T) {
	s, plan, fence, snapshot := preparationFixture(t)
	ctx := context.Background()
	if err := s.CompleteReconciliation(ctx, fence, snapshot); err != nil {
		t.Fatal(err)
	}
	assertPreparationCharge(t, s, true, 1)
	state := sqliteSnapshot(t, s)
	if len(state.Inventory) != 0 || len(state.Reservations) != 1 {
		t.Fatal("nonphysical preparation became inventory or lost charge")
	}
	proofs := 0
	for _, event := range state.Audit {
		if event.Kind == "garm_preparation_verified" && event.RequestKey == plan.Request.Key {
			proofs++
		}
	}
	if proofs != 1 {
		t.Fatal("proof audit missing")
	}
	binding := state.HostBindings[plan.Request.Host]
	calls := 0
	callback := func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		calls++
		return confirmedReceipt(), nil
	}
	if _, err := s.ExecutePlannedDispatch(ctx, plan, binding, callback); err != nil || calls != 1 {
		t.Fatalf("original create %v calls%d", err, calls)
	}
	if _, err := s.ExecutePlannedDispatch(ctx, plan, binding, callback); !errors.Is(err, ErrDispatchClaimed) || calls != 1 {
		t.Fatal("proof became replay permit")
	}
	assertPreparationCharge(t, s, true, 1)
}
func TestGarmPreparationRejectsStalePartialMismatchedAndPhysicalProof(t *testing.T) {
	for _, mode := range []string{"duplicate", "fence", "time", "future", "binding", "request", "name", "physical", "incomplete", "unhealthy", "legacy-key", "legacy-host"} {
		t.Run(mode, func(t *testing.T) {
			s, plan, fence, snapshot := preparationFixture(t)
			switch mode {
			case "duplicate":
				snapshot.Preparations = append(snapshot.Preparations, snapshot.Preparations[0])
			case "fence":
				snapshot.Preparations[0].Fence.Nonce = "other"
			case "time":
				snapshot.Preparations[0].ObservedAt = snapshot.Preparations[0].ObservedAt.Add(-time.Second)
			case "future":
				snapshot.Preparations[0].ObservedAt = snapshot.Preparations[0].ObservedAt.Add(time.Second)
			case "binding":
				snapshot.Preparations[0].HostBindingSequence++
			case "request":
				snapshot.Preparations[0].Request.Resources.CPU++
			case "name":
				snapshot.Preparations[0].Name = "different-name"
			case "physical":
				snapshot.Resources = []InventoryResource{{ID: plan.Execution.Name, Host: plan.Request.Host, Pool: plan.Request.Pool, ReservationKey: plan.Request.Key, Resources: plan.Request.Resources, State: "creating"}}
			case "incomplete":
				snapshot.Hosts[0].IncusComplete = false
			case "unhealthy":
				for n := range snapshot.Hosts {
					if snapshot.Hosts[n].Host == plan.Request.Host {
						snapshot.Hosts[n].Healthy = false
					}
				}
			case "legacy-key":
				if _, err := s.db.Exec("INSERT INTO cancellation_history_exclusions(request_key) VALUES(?)", plan.Request.Key); err != nil {
					t.Fatal(err)
				}
			case "legacy-host":
				if _, err := s.db.Exec("INSERT INTO host_binding_history_exclusions(host) VALUES(?)", plan.Request.Host); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.CompleteReconciliation(context.Background(), fence, snapshot); err == nil {
				t.Fatal("unsafe preparation proof accepted")
			}
			assertPreparationCharge(t, s, false, 1)
			for _, event := range sqliteSnapshot(t, s).Audit {
				if event.Kind == "garm_preparation_verified" {
					t.Fatal("failed proof audit escaped rollback")
				}
			}
		})
	}
}
func TestGarmPreparationCannotLaunderPreviouslyPersistedCreatingInventory(t *testing.T) {
	s, plan, fence, snapshot := preparationFixture(t)
	ctx := context.Background()
	snapshot.Preparations = nil
	snapshot.Resources = []InventoryResource{{ID: plan.Execution.Name, Host: plan.Request.Host, Pool: plan.Request.Pool, ReservationKey: plan.Request.Key, Resources: plan.Request.Resources, State: "creating"}}
	if err := s.CompleteReconciliation(ctx, fence, snapshot); err != nil {
		t.Fatal(err)
	}
	fence, snapshot = preparationSnapshot(t, s, plan)
	if err := s.CompleteReconciliation(ctx, fence, snapshot); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("historical row laundered %v", err)
	}
	state := sqliteSnapshot(t, s)
	if len(state.Inventory) != 1 || state.Allocated.Global.Runners != 1 || state.Ready {
		t.Fatal("prior inventory erased or charge changed")
	}
}
func TestGarmPreparationRequiresPermanentPlanAndNoCancellation(t *testing.T) {
	t.Run("no-plan", func(t *testing.T) {
		s := placementFixture(t)
		request := placementRequest("legacy-preparation")
		binding := sqliteSnapshot(t, s).HostBindings["a"]
		plan := ExecutionPlan{Request: Request{Key: request.Key, Pool: request.Pool, Host: "a", Resources: request.Resources}, Execution: request.Execution, HostBindingSequence: binding.Sequence}
		if _, err := s.Admit(context.Background(), plan.Request); err != nil {
			t.Fatal(err)
		}
		fence, snapshot := preparationSnapshot(t, s, plan)
		if err := s.CompleteReconciliation(context.Background(), fence, snapshot); !errors.Is(err, ErrReconciliation) {
			t.Fatal("legacy charge promoted")
		}
		assertPreparationCharge(t, s, false, 1)
	})
	t.Run("cancelled", func(t *testing.T) {
		s := placementFixture(t)
		plan, err := s.AdmitPlacement(context.Background(), placementRequest("cancelled-preparation"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.CancelUndispatched(context.Background(), plan.Request); err != nil {
			t.Fatal(err)
		}
		fence, snapshot := preparationSnapshot(t, s, plan)
		if err = s.CompleteReconciliation(context.Background(), fence, snapshot); !errors.Is(err, ErrReconciliation) {
			t.Fatal("cancelled plan revived")
		}
		assertPreparationCharge(t, s, false, 0)
		if _, err = s.ClaimDispatchBound(context.Background(), plan.Request, sqliteSnapshot(t, s).HostBindings[plan.Request.Host]); !errors.Is(err, ErrCancelled) {
			t.Fatal("cancelled key dispatched")
		}
	})
}
func TestGarmPreparationBarrierVersusClaimBothSerializationOrders(t *testing.T) {
	ctx := context.Background()
	t.Run("barrier-first", func(t *testing.T) {
		s, plan, fence, snapshot := preparationFixture(t)
		binding := sqliteSnapshot(t, s).HostBindings[plan.Request.Host]
		calls := 0
		callback := func(context.Context, DispatchPermit) (DispatchReceipt, error) {
			calls++
			return confirmedReceipt(), nil
		}
		if _, err := s.ExecutePlannedDispatch(ctx, plan, binding, callback); !errors.Is(err, ErrReconciliation) || calls != 0 {
			t.Fatal("claim crossed collection barrier")
		}
		if err := s.CompleteReconciliation(ctx, fence, snapshot); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ExecutePlannedDispatch(ctx, plan, binding, callback); err != nil || calls != 1 {
			t.Fatal("verified preparation did not resume")
		}
	})
	t.Run("claim-first", func(t *testing.T) {
		s := placementFixture(t)
		plan, err := s.AdmitPlacement(ctx, placementRequest("claimed-preparation"))
		if err != nil {
			t.Fatal(err)
		}
		binding := sqliteSnapshot(t, s).HostBindings[plan.Request.Host]
		entered, release := make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		var calls atomic.Int32
		go func() {
			_, e := s.ExecutePlannedDispatch(ctx, plan, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
				calls.Add(1)
				close(entered)
				<-release
				return DispatchReceipt{State: "unknown"}, errors.New("accepted fixture lost")
			})
			result <- e
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("claim not committed")
		}
		other, err := OpenSQLite(s.path, Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		fence, snapshot := preparationSnapshot(t, other, plan)
		if err = other.CompleteReconciliation(ctx, fence, snapshot); !errors.Is(err, ErrReconciliation) {
			t.Fatal("in-flight intent ignored")
		}
		close(release)
		if err = <-result; !errors.Is(err, ErrDispatchUnknown) {
			t.Fatal(err)
		}
		if _, err = other.ExecutePlannedDispatch(ctx, plan, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
			calls.Add(1)
			return confirmedReceipt(), nil
		}); !errors.Is(err, ErrDispatchClaimed) || calls.Load() != 1 {
			t.Fatal("in-flight proof duplicated callback")
		}
		assertPreparationCharge(t, s, false, 1)
	})
}
func TestGarmPreparationProofIsNotReusableAcrossRestartOrBackupFence(t *testing.T) {
	s, plan, fence, snapshot := preparationFixture(t)
	ctx := context.Background()
	other, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	next, err := other.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.CompleteReconciliation(ctx, fence, snapshot); !errors.Is(err, ErrFence) {
		t.Fatal("stale barrier reopened on another handle")
	}
	if err = other.CompleteReconciliation(ctx, next, snapshot); !errors.Is(err, ErrReconciliation) {
		t.Fatal("cached proof reused in new barrier")
	}
	directory := t.TempDir()
	if os.Chmod(directory, 0700) != nil {
		t.Fatal("private fixture")
	}
	backup := filepath.Join(directory, "backup.db")
	if err = s.BackupSQLite(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenSQLite(backup, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.CompleteReconciliation(ctx, next, snapshot); !errors.Is(err, ErrFence) {
		t.Fatal("source fence worked on artifact")
	}
	assertPreparationCharge(t, restored, false, 1)
	if _, err = restored.LookupExecutionPlan(ctx, plan.Request.Key); err != nil {
		t.Fatal("backup lost immutable plan")
	}
}
func TestGarmPreparationAuditFailureRollsBackWithoutRemovingCharge(t *testing.T) {
	s, _, fence, snapshot := preparationFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_preparation BEFORE INSERT ON audit WHEN NEW.kind='garm_preparation_verified' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReconciliation(context.Background(), fence, snapshot); err == nil {
		t.Fatal("proof without durable audit accepted")
	}
	assertPreparationCharge(t, s, false, 1)
}
