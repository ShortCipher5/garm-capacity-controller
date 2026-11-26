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
	for _, change := range []PolicyChange{{Action: "host_mode", Target: "a", Mode: "disabled"}, {Action: "host_mode", Target: "a", Mode: "draining"}, {Action: "host_mode", Target: "a", Mode: "enabled"}, {Action: "host_budget", Target: "a", Budget: &budget}, {Action: "pool_budget", Target: "app", Budget: &budget}} {
		change.Reason = "operator maintenance"
		change.Revision = PolicyRevision(sqliteSnapshot(t, s).Config)
		if err := s.ApplyPolicyChange(ctx, change); err != nil {
			t.Fatalf("%s: %v", change.Action, err)
		}
	}
	before := sqliteSnapshot(t, s)
	bad := []PolicyChange{{Action: "host_mode", Target: "a", Mode: "retired"}, {Action: "host_mode", Target: "missing", Mode: "enabled"}, {Action: "global_budget", Budget: &Resources{CPU: -1}}, {Action: "pool_hosts", Target: "app"}, {Action: "delete", Target: "app"}, {Action: "host_budget", Target: "a", Budget: &budget, Mode: "enabled"}}
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

func TestAdminHostActivationRequiresCurrentVerifiedPlacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	config := fixture(time.Now())
	host := config.Hosts["a"]
	host.Mode = "disabled"
	config.Hosts["a"] = host
	s := sqliteFixtureStore(t, config, true)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }

	enable := PolicyChange{
		Revision: PolicyRevision(config), Action: "host_mode", Target: "a",
		Mode: "enabled", Reason: "resume server placement",
	}
	if err := s.ApplyAdminPolicyChange(ctx, enable, true); !errors.Is(err, ErrHostActivation) {
		t.Fatalf("unbound host passed activation: %v", err)
	}
	fence, bindings := bindingFixture(t, s)
	if err := s.BindHostIdentities(ctx, fence, bindings); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAdminPolicyChange(ctx, enable, true); !errors.Is(err, ErrHostActivation) {
		t.Fatalf("partially reconciled host passed activation: %v", err)
	}
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(config, now)}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAdminPolicyChange(ctx, enable, false); !errors.Is(err, ErrHostActivation) {
		t.Fatalf("activation worked with provisioning off: %v", err)
	}
	now = now.Add(config.MaxObservationAge + time.Second)
	if err := s.ApplyAdminPolicyChange(ctx, enable, true); !errors.Is(err, ErrHostActivation) {
		t.Fatalf("expired health passed activation: %v", err)
	}
	reconcileSQLite(t, s, nil)
	if err := s.ApplyAdminPolicyChange(ctx, enable, true); err != nil {
		t.Fatal(err)
	}
	after := sqliteSnapshot(t, s)
	if after.Ready || after.Config.Hosts["a"].Mode != "enabled" {
		t.Fatal("activation failed to close readiness pending new inventory")
	}
	for _, change := range []string{"draining", "disabled"} {
		stop := PolicyChange{
			Revision: PolicyRevision(sqliteSnapshot(t, s).Config), Action: "host_mode", Target: "a",
			Mode: change, Reason: "stop server placement",
		}
		if err := s.ApplyAdminPolicyChange(ctx, stop, false); err != nil {
			t.Fatalf("could not apply %s while unavailable: %v", change, err)
		}
	}
}

func TestDiscoveryCannotEstablishPlacementHealth(t *testing.T) {
	for _, tc := range []string{"stale", "unhealthy", "legacy persisted health"} {
		t.Run(tc, func(t *testing.T) {
			ctx := t.Context()
			now := time.Now().UTC()
			config := fixture(now)
			host := config.Hosts["a"]
			host.Mode = "disabled"
			host.Healthy = tc == "stale"
			config.Hosts["a"] = host
			s := sqliteFixtureStore(t, config, false)
			s.now = func() time.Time { return now }
			fence, bindings := bindingFixture(t, s)
			if err := s.BindHostIdentities(ctx, fence, bindings); err != nil {
				t.Fatal(err)
			}
			if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(config, now)}); err != nil {
				t.Fatal(err)
			}
			if tc == "stale" {
				now = now.Add(config.MaxObservationAge + time.Second)
			}
			if tc == "legacy persisted health" {
				// Reproduce the pre-fix transaction and audit event. The old
				// write reports healthy even though reconciliation said otherwise.
				_, err := s.db.Exec(`UPDATE metadata SET config=CAST(json_set(config, '$.hosts.a.healthy', json('true'), '$.hosts.a.observed_at', ?) AS BLOB) WHERE id=1;
					INSERT INTO audit(kind,request_key,recorded_at) VALUES ('host_detected','a',?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
				if err != nil {
					t.Fatal(err)
				}
				path := s.path
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = OpenSQLite(path, Config{})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				s.now = func() time.Time { return now }
				persisted := sqliteSnapshot(t, s)
				if !persisted.Ready || !persisted.Config.Hosts["a"].Healthy || !persisted.Config.Hosts["a"].ObservedAt.Equal(now) {
					t.Fatal("legacy discovery did not persist apparently healthy, ready state")
				}
			}
			detected := Resources{CPU: 8000, RAM: 8192, Disk: 32768}
			if err := s.SetHostDetected(ctx, "a", &detected, now); err != nil {
				t.Fatal(err)
			}
			enable := PolicyChange{Revision: PolicyRevision(config), Action: "host_mode", Target: "a", Mode: "enabled", Reason: "resume server placement"}
			if err := s.ApplyAdminPolicyChange(ctx, enable, true); !errors.Is(err, ErrHostActivation) {
				t.Fatalf("accepted discovery as placement health: %v", err)
			}
			fence, err := s.BeginReconciliation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			host.Healthy = true
			config.Hosts["a"] = host
			if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(config, now)}); err != nil {
				t.Fatal(err)
			}
			if err := s.SetHostDetected(ctx, "a", nil, now); err != nil {
				t.Fatal(err)
			}
			if err := s.ApplyAdminPolicyChange(ctx, enable, true); err != nil {
				t.Fatal("fresh reconciled health should permit activation", err)
			}
		})
	}
}

func TestRemoveUnusedDiscoveryEnrollmentPreservesHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	budget := Resources{CPU: 1000, RAM: 1024, Disk: 4096, Runners: 1}
	review, err := s.RecordDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", "discovery", now, []byte(`{}`), &budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollHostFromReview(ctx, "new-server", budget, "disabled", review); err != nil {
		t.Fatal(err)
	}
	before := sqliteSnapshot(t, s)
	remove := PolicyChange{
		Revision: PolicyRevision(before.Config), Action: "host_draft_remove", Target: "new-server",
		Reason: "discard staged server",
	}
	if err := s.ApplyPolicyChange(ctx, remove); !errors.Is(err, ErrHostDraftRemoval) {
		t.Fatalf("generic policy interface removed a managed draft: %v", err)
	}
	if err := s.ApplyAdminPolicyChange(ctx, remove, true); !errors.Is(err, ErrHostDraftRemoval) {
		t.Fatalf("removed draft with provisioning on: %v", err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER reject_removal BEFORE INSERT ON audit WHEN NEW.kind='admin_policy_change' BEGIN SELECT RAISE(ABORT,'fault'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAdminPolicyChange(ctx, remove, false); err == nil {
		t.Fatal("removed without successful audit")
	}
	if after := sqliteSnapshot(t, s); !reflect.DeepEqual(after.Config, before.Config) || len(after.Audit) != len(before.Audit) {
		t.Fatal("failed audit persisted the withdrawal")
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_removal"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAdminPolicyChange(ctx, remove, false); err != nil {
		t.Fatal(err)
	}
	after := sqliteSnapshot(t, s)
	if _, ok := after.Config.Hosts["new-server"]; ok || after.Ready ||
		after.Allocated.Global != before.Allocated.Global || auditKinds(after)["host_draft_removed"] != 1 {
		t.Fatal("draft removal changed charges or lost its retirement barrier")
	}
	for _, event := range after.Audit {
		if event.Kind == "host_draft_removed" && (event.Host != "new-server" || event.RequestKey != "") {
			t.Fatal("draft removal audit lost its host target")
		}
	}
	other, err := OpenSQLite(s.path, after.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.EnrollHost(ctx, "new-server", budget, "disabled"); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdrawn alias could be reused: %v", err)
	}
	if err := other.EnrollHostFromReview(ctx, "different-alias", budget, "disabled", review); !errors.Is(err, ErrConflict) {
		t.Fatalf("consumed review could be reused: %v", err)
	}
}

func TestDraftRemovalRefusesConfiguredOrBoundServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	review, err := s.RecordDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", "discovery", now, []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollHostFromReview(ctx, "new-server", Resources{}, "disabled", review); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostMode(ctx, "a", "disabled"); err != nil {
		t.Fatal(err)
	}
	fence, bindings := bindingFixture(t, s)
	if err := s.BindHostIdentities(ctx, fence, bindings); err != nil {
		t.Fatal(err)
	}
	before := sqliteSnapshot(t, s)
	for _, target := range []string{"a", "new-server"} {
		change := PolicyChange{
			Revision: PolicyRevision(before.Config), Action: "host_draft_remove", Target: target,
			Reason: "try to discard a known host",
		}
		if err := s.ApplyAdminPolicyChange(ctx, change, false); !errors.Is(err, ErrHostDraftRemoval) {
			t.Fatalf("removed host %q: %v", target, err)
		}
	}
	after := sqliteSnapshot(t, s)
	if !reflect.DeepEqual(after.Config, before.Config) || !reflect.DeepEqual(after.HostBindings, before.HostBindings) || len(after.Audit) != len(before.Audit) {
		t.Fatal("failed removal touched the configuration, bindings or audit")
	}
}

func TestDiscoveryTotalsAuditPreventsDraftRemoval(t *testing.T) {
	ctx := t.Context()
	store := sqliteFixtureStore(t, fixture(time.Now()), false)
	review, err := store.RecordDiscoveryReview(ctx, "11111111-1111-4111-8111-111111111111", "discovery", time.Now().UTC(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollHostFromReview(ctx, "draft", Resources{}, "disabled", review); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHostDetected(ctx, "draft", nil, store.now()); err != nil {
		t.Fatal(err)
	}
	snap := sqliteSnapshot(t, store)
	remove := PolicyChange{Revision: PolicyRevision(snap.Config), Action: "host_draft_remove", Target: "draft", Reason: "discard draft"}
	if err := store.ApplyAdminPolicyChange(ctx, remove, false); !errors.Is(err, ErrHostDraftRemoval) {
		t.Fatalf("lost retained detection history: %v", err)
	}
}
