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

func TestPolicyChangeAtomicCASAuditAndReadiness(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if _, err := s.Admit(ctx, request("reserved")); err != nil {
		t.Fatal(err)
	}
	before := sqliteSnapshot(t, s)
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	low := Resources{CPU: 1, RAM: 1, Runners: 0}
	change := PolicyChange{Revision: PolicyRevision(before.Config), Action: "global_budget", Budget: &low, Reason: "lower declared ceiling"}
	if err := s.ApplyPolicyChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	after := sqliteSnapshot(t, s)
	if after.Ready || after.Config.Global != low || after.Allocated.Global != before.Allocated.Global || !reflect.DeepEqual(after.Reservations, before.Reservations) {
		t.Fatal("policy update lost charge or readiness barrier")
	}
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(before.Config, time.Now())}); !errors.Is(err, ErrFence) {
		t.Fatalf("stale collector reopened: %v", err)
	}
	if err := s.ApplyPolicyChange(ctx, change); !errors.Is(err, ErrPolicyConflict) {
		t.Fatalf("stale policy accepted: %v", err)
	}
	var found bool
	for _, e := range after.Audit {
		if e.Kind == "admin_policy_change" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing durable audit")
	}
}

func TestPolicyChangeKindsAndValidation(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	budget := Resources{CPU: 2000, RAM: 4096, Runners: 2}
	for _, change := range []PolicyChange{{Action: "host_mode", Target: "a", Mode: "disabled"}, {Action: "host_mode", Target: "a", Mode: "enabled"}, {Action: "host_budget", Target: "a", Budget: &budget}, {Action: "pool_budget", Target: "app", Budget: &budget}} {
		change.Reason = "operator maintenance"
		change.Revision = PolicyRevision(sqliteSnapshot(t, s).Config)
		if err := s.ApplyPolicyChange(ctx, change); err != nil {
			t.Fatalf("%s: %v", change.Action, err)
		}
	}
	before := sqliteSnapshot(t, s)
	bad := []PolicyChange{{Action: "host_mode", Target: "a", Mode: "draining"}, {Action: "host_mode", Target: "missing", Mode: "enabled"}, {Action: "global_budget", Budget: &Resources{CPU: -1}}, {Action: "pool_hosts", Target: "app"}, {Action: "delete", Target: "app"}, {Action: "host_budget", Target: "a", Budget: &budget, Mode: "enabled"}}
	for _, change := range bad {
		change.Revision = PolicyRevision(before.Config)
		change.Reason = "test"
		if err := s.ApplyPolicyChange(ctx, change); !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid change accepted: %+v %v", change, err)
		}
	}
	after := sqliteSnapshot(t, s)
	if !reflect.DeepEqual(before.Config, after.Config) || len(before.Audit) != len(after.Audit) {
		t.Fatal("invalid edits changed policy/audit")
	}
}

func TestPolicyChangeAuditFailureRollsBackAndConcurrentWritersConflict(t *testing.T) {
	ctx := context.Background()
	config := fixture(time.Now())
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before := sqliteSnapshot(t, s)
	change := PolicyChange{Revision: PolicyRevision(before.Config), Action: "host_mode", Target: "a", Mode: "disabled", Reason: "test"}
	if _, err := s.db.Exec("CREATE TRIGGER reject_policy BEFORE INSERT ON audit WHEN NEW.kind='admin_policy_change' BEGIN SELECT RAISE(ABORT,'denied'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPolicyChange(ctx, change); err == nil {
		t.Fatal("failed audit committed policy")
	}
	if !reflect.DeepEqual(sqliteSnapshot(t, s).Config, before.Config) {
		t.Fatal("policy not rolled back")
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_policy"); err != nil {
		t.Fatal(err)
	}
	other, err := OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	changes := []PolicyChange{change, {Revision: change.Revision, Action: "host_mode", Target: "b", Mode: "disabled", Reason: "test"}}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*SQLiteStore{s, other} {
		wg.Add(1)
		go func(i int, store *SQLiteStore) { defer wg.Done(); results <- store.ApplyPolicyChange(ctx, changes[i]) }(i, store)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrPolicyConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("lost update: %d success %d conflict", success, conflict)
	}
}

func TestPolicyRevisionIgnoresHealthButNoopPreservesCollector(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	before := sqliteSnapshot(t, s)
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	change := PolicyChange{Revision: PolicyRevision(before.Config), Action: "host_mode", Target: "a", Mode: "enabled", Reason: "noop"}
	if err := s.ApplyPolicyChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(before.Config, time.Now())}); err != nil {
		t.Fatal(err)
	}
	if PolicyRevision(sqliteSnapshot(t, s).Config) != change.Revision {
		t.Fatal("health collection invalidated policy revision")
	}
}
