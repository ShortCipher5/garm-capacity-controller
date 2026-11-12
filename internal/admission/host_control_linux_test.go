package admission

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestHostModeChangePreservesAllChargesAndRequiresFreshEnable(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if _, err := s.Admit(ctx, request("reserved")); err != nil {
		t.Fatal(err)
	}
	reconcileSQLite(t, s, []InventoryResource{resource("orphan-idle", "", 1000, 1024)})
	before := sqliteSnapshot(t, s)
	if err := s.SetHostMode(ctx, "a", "draining"); err != nil {
		t.Fatal(err)
	}
	after := sqliteSnapshot(t, s)
	if after.Ready || after.Config.Hosts["a"].Mode != "draining" || after.Allocated.Global != before.Allocated.Global || !reflect.DeepEqual(after.Reservations, before.Reservations) || !reflect.DeepEqual(after.Inventory, before.Inventory) {
		t.Fatal("mode change released/changed resources or left readiness open")
	}
	if after.Config.Hosts["a"].Budget != before.Config.Hosts["a"].Budget || after.Config.Global != before.Config.Global || !reflect.DeepEqual(after.Config.Pools, before.Config.Pools) {
		t.Fatal("host mode changed budgets")
	}
	if _, err := s.ClaimDispatch(ctx, request("reserved")); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("reservation replay bypassed mode barrier %v", err)
	}
	reconcileSQLite(t, s, after.Inventory)
	if _, err := s.Admit(ctx, request("new-draining")); !errors.Is(err, ErrDenied) {
		t.Fatalf("draining admitted %v", err)
	}
	if _, err := s.ClaimDispatch(ctx, request("reserved")); !errors.Is(err, ErrDenied) {
		t.Fatalf("draining dispatched old receipt %v", err)
	}
	if err := s.SetHostMode(ctx, "a", "enabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDispatch(ctx, request("reserved")); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("enable bypassed fresh baseline %v", err)
	}
	reconcileSQLite(t, s, after.Inventory)
	if _, err := s.ClaimDispatch(ctx, request("reserved")); err != nil {
		t.Fatalf("fresh enabled host did not claim %v", err)
	}
	final := sqliteSnapshot(t, s)
	if final.Allocated.Global != before.Allocated.Global {
		t.Fatal("mode enable changed conservative allocation")
	}
	var events int
	for _, event := range final.Audit {
		if event.Host != "" {
			events++
			if event.Host != "a" || event.RequestKey != "" {
				t.Fatal("host audit conflated with reservation")
			}
		}
	}
	if events != 2 {
		t.Fatal("missing host administration audit")
	}
}
func TestHostModeInvalidatesActiveCollectorAndNoopPreservesIt(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostMode(ctx, "a", "enabled"); err != nil {
		t.Fatal(err)
	}
	snapshot := InventorySnapshot{Hosts: observations(fixture(time.Now()), time.Now())}
	if err := s.CompleteReconciliation(ctx, fence, snapshot); err != nil {
		t.Fatalf("no-op invalidated collector %v", err)
	}
	fence, err = s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostMode(ctx, "a", "disabled"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReconciliation(ctx, fence, snapshot); !errors.Is(err, ErrFence) {
		t.Fatalf("pre-mode-change collector reopened admission %v", err)
	}
	reconcileSQLite(t, s, nil)
	if sqliteSnapshot(t, s).Config.Hosts["a"].Mode != "disabled" {
		t.Fatal("collector overwrote disabled policy")
	}
}
func TestHostModeAndAuditRollbackTogether(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	before := sqliteSnapshot(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_host_mode BEFORE INSERT ON audit WHEN NEW.kind LIKE 'host_mode_%' BEGIN SELECT RAISE(ABORT,'test audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostMode(ctx, "a", "draining"); err == nil {
		t.Fatal("mode change ignored audit failure")
	}
	after := sqliteSnapshot(t, s)
	if !after.Ready || !reflect.DeepEqual(after.Config, before.Config) {
		t.Fatal("failed audit left a partial policy/barrier change")
	}
	for _, args := range [][2]string{{"missing", "disabled"}, {"a", "bad-mode"}, {"a\n", "disabled"}} {
		if err := s.SetHostMode(ctx, args[0], args[1]); !errors.Is(err, ErrDenied) {
			t.Fatal("invalid target/mode accepted")
		}
	}
}
func TestCommittedDispatchIsInflightNotRevokedByLaterMode(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if _, err := s.Admit(ctx, request("inflight")); err != nil {
		t.Fatal(err)
	}
	permit, err := s.ClaimDispatch(ctx, request("inflight"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostMode(ctx, "a", "draining"); err != nil {
		t.Fatal(err)
	}
	// Later external completion remains recordable. Mode changes cannot retroactively
	// revoke a committed permit or cancel a job; its charge must survive.
	if err := s.RecordDispatchOutcome(ctx, *permit, DispatchReceipt{State: "confirmed", OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: "runner-inflight"}); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimDispatch(ctx, request("inflight")); next != nil || !errors.Is(err, ErrDispatchClaimed) {
		t.Fatal("drain/replay created another permit")
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("in-flight charge disappeared")
	}
}
func TestHostModeRacesDispatchAcrossHandlesSerializeByAudit(t *testing.T) {
	ctx := context.Background()
	for attempt := 0; attempt < 20; attempt++ {
		path := filepath.Join(t.TempDir(), "ledger.db")
		first, err := OpenSQLite(path, fixture(time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		second, err := OpenSQLite(path, Config{})
		if err != nil {
			first.Close()
			t.Fatal(err)
		}
		reconcileSQLite(t, first, nil)
		if _, err := first.Admit(ctx, request("race")); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		var permit *DispatchPermit
		var claimErr, modeErr error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; permit, claimErr = first.ClaimDispatch(ctx, request("race")) }()
		go func() { defer wg.Done(); <-start; modeErr = second.SetHostMode(ctx, "a", "draining") }()
		close(start)
		wg.Wait()
		if modeErr != nil {
			t.Fatal(modeErr)
		}
		state := sqliteSnapshot(t, first)
		if state.Ready || state.Config.Hosts["a"].Mode != "draining" {
			t.Fatal("race lost mode barrier")
		}
		var claimSequence, modeSequence int64
		for _, event := range state.Audit {
			if event.Kind == "dispatch_claimed" {
				claimSequence = event.Sequence
			}
			if event.Kind == "host_mode_draining" {
				modeSequence = event.Sequence
			}
		}
		if permit != nil {
			if claimErr != nil || claimSequence == 0 || claimSequence >= modeSequence {
				t.Fatal("new permit committed after draining")
			}
		} else if !errors.Is(claimErr, ErrReconciliation) && !errors.Is(claimErr, ErrDenied) {
			t.Fatalf("unexpected race outcome %v", claimErr)
		}
		first.Close()
		second.Close()
	}
}
