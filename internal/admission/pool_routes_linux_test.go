package admission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Explicit trusted-source fixture, NOT a production proof adapter. Actual
// confirmed-journal lookup and authenticated readback integration are separate.
type inactiveRouteSource struct {
	facts DisabledPoolFacts
	err   error
	calls atomic.Int32
}

func (s *inactiveRouteSource) ConfirmedDisabledPool(context.Context, string) (DisabledPoolFacts, error) {
	s.calls.Add(1)
	return s.facts, s.err
}
func inactiveRouteFixture(t *testing.T) (*SQLiteStore, *PoolRegistrar, *inactiveRouteSource) {
	t.Helper()
	s := placementFixture(t)
	source := &inactiveRouteSource{facts: DisabledPoolFacts{Protocol: "native-disabled-repository-pool-v1", ManagementIdentity: bindingHash("management"), JournalLineage: bindingHash("journal"), RequestID: "11111111-1111-4111-8111-111111111111", PoolID: "22222222-2222-4222-8222-222222222222", RepositoryID: "33333333-3333-4333-8333-333333333333", ProviderName: "capacity", ForgeEndpointName: "github", SpecFingerprint: bindingHash("created-spec"), PoolFingerprint: bindingHash("readback"), Image: "prebuilt", Flavor: "small", OSType: "linux", OSArch: "amd64", ExtraSpecsEmpty: true, MaxRunners: 1, ObservedAt: s.now()}}
	jit := true
	template := PoolRouteTemplate{JITEnabled: &jit, Reference: "small-linux", Revision: bindingHash("template revision"), ProviderName: "capacity", ForgeEndpointName: "github", RepositoryIDs: []string{source.facts.RepositoryID}, HostGroup: "existing-hosts", Hosts: []string{"b", "a"}, SourceActorGroup: "existing-source", SourceFingerprints: []string{bindingHash("source leaf")}, Execution: placementRequest("placeholder").Execution, PerRunner: Resources{CPU: 1000, RAM: 1024, Runners: 1}, MaxRunners: 2, Strategy: "first_fit"}
	registrar, err := NewPoolRegistrar(s, source, "controller", source.facts.ManagementIdentity, source.facts.JournalLineage, []PoolRouteTemplate{template})
	if err != nil {
		t.Fatal(err)
	}
	return s, registrar, source
}
func TestInactivePoolRouteRegistrationAtomicallyAddsNoChargeAndClosesFence(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	old, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := sqliteSnapshot(t, s)
	record, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(before.Config))
	if err != nil || record.Mode != "inactive" || record.Identity.IdentitySequence <= 0 || record.Replay {
		t.Fatalf("registration %+v %v", record, err)
	}
	after := sqliteSnapshot(t, s)
	if after.Ready || !reflect.DeepEqual(after.Allocated, before.Allocated) || len(after.Reservations) != 0 || after.Config.Pools[source.facts.PoolID].Budget != (Resources{CPU: 1000, RAM: 1024, Runners: 1}) {
		t.Fatal("registration admitted/released or lost bounded policy")
	}
	if err := s.CompleteReconciliation(ctx, old, InventorySnapshot{Hosts: observations(after.Config, s.now())}); !errors.Is(err, ErrFence) {
		t.Fatal("registration reused old collection", err)
	}
	loaded, err := s.LookupPoolRoute(ctx, source.facts.PoolID)
	if err != nil || !record.Equal(loaded) {
		t.Fatal("identity lost", err)
	}
	replay, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(before.Config))
	if err != nil || !replay.Replay || !replay.Equal(record) {
		t.Fatal("lost ACK retry changed identity", err)
	}
	for _, statement := range []string{"UPDATE pool_routes SET mode='active'", "UPDATE pool_routes SET payload='{}'", "DELETE FROM pool_routes"} {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatal("foundation exposed activation/reset")
		}
	}
}
func TestInactivePoolRouteDoesNotBypassDeclaredCapacityGuards(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	before := sqliteSnapshot(t, s)
	if _, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(before.Config)); err != nil {
		t.Fatal(err)
	}
	// Even after a complete new inventory, no active route/creation capability is
	// installed. SQL also protects an older writer unaware of catalog APIs.
	state := sqliteSnapshot(t, s)
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(state.Config, s.now())}); err != nil {
		t.Fatal(err)
	}
	request := placementRequest("must-not-create")
	request.Pool = source.facts.PoolID
	request.Key = executionKey(request.Execution, request.Pool)
	if _, err := s.AdmitPlacement(ctx, request); err == nil {
		t.Fatal("inactive route admitted")
	}
	raw := Request{Key: request.Key, Pool: request.Pool, Host: "a", Resources: request.Resources}
	if _, err := s.Admit(ctx, raw); err == nil {
		t.Fatal("older raw API bypassed inactive route")
	}
	if len(sqliteSnapshot(t, s).Reservations) != 0 {
		t.Fatal("inactive denial left charge")
	}
}
func TestInactivePoolRouteRequiresBoundConfirmedFreshExactTemplate(t *testing.T) {
	for _, kind := range []string{"unknown-outcome", "management", "lineage", "request", "pool-id", "repository", "provider", "forge", "enabled", "min-idle", "zero-max", "over-max", "template", "shell", "extras", "os", "arch", "image", "flavor", "future", "stale", "template-ref", "policy-cas", "host-binding"} {
		t.Run(kind, func(t *testing.T) {
			s, r, source := inactiveRouteFixture(t)
			before := sqliteSnapshot(t, s)
			template := "small-linux"
			revision := PolicyRevision(before.Config)
			switch kind {
			case "unknown-outcome":
				source.err = errors.New("private upstream response must not escape")
			case "management":
				source.facts.ManagementIdentity = bindingHash("other")
			case "lineage":
				source.facts.JournalLineage = bindingHash("other")
			case "request":
				source.facts.RequestID = "44444444-4444-4444-8444-444444444444"
			case "pool-id":
				source.facts.PoolID = "guessed-alias"
			case "repository":
				source.facts.RepositoryID = "44444444-4444-4444-8444-444444444444"
			case "provider":
				source.facts.ProviderName = "other"
			case "forge":
				source.facts.ForgeEndpointName = "other"
			case "enabled":
				source.facts.Enabled = true
			case "min-idle":
				source.facts.MinIdleRunners = 1
			case "zero-max":
				source.facts.MaxRunners = 0
			case "over-max":
				source.facts.MaxRunners = 3
			case "template":
				source.facts.TemplateID = 1
			case "shell":
				source.facts.ShellEnabled = true
			case "extras":
				source.facts.ExtraSpecsEmpty = false
			case "os":
				source.facts.OSType = "windows"
			case "arch":
				source.facts.OSArch = "arm64"
			case "image":
				source.facts.Image = "other"
			case "flavor":
				source.facts.Flavor = "other"
			case "future":
				source.facts.ObservedAt = s.now().Add(time.Hour)
			case "stale":
				source.facts.ObservedAt = s.now().Add(-time.Hour)
			case "template-ref":
				template = "caller-supplied-arbitrary-template"
			case "policy-cas":
				revision = bindingHash("stale-policy")
			case "host-binding":
				if _, err := s.db.Exec("INSERT INTO host_binding_history_exclusions VALUES('a')"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.RegisterInactive(context.Background(), "11111111-1111-4111-8111-111111111111", template, revision); err == nil {
				t.Fatal("unqualified registration accepted")
			}
			after := sqliteSnapshot(t, s)
			if PolicyRevision(after.Config) != PolicyRevision(before.Config) || !reflect.DeepEqual(after.Allocated, before.Allocated) {
				t.Fatal("failed validation changed policy/charge")
			}
			var count int
			if s.db.QueryRow("SELECT count(*) FROM pool_routes").Scan(&count) != nil || count != 0 {
				t.Fatal("failed registration persisted")
			}
		})
	}
}
func TestInactivePoolRouteAuditRollbackAndSeparateHandleCAS(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		s, r, source := inactiveRouteFixture(t)
		before := sqliteSnapshot(t, s)
		if _, err := s.db.Exec(`CREATE TRIGGER route_audit_fail BEFORE INSERT ON audit WHEN NEW.kind='pool_route_registered_inactive' BEGIN SELECT RAISE(ABORT,'fixture'); END;`); err != nil {
			t.Fatal(err)
		}
		if _, err := r.RegisterInactive(context.Background(), source.facts.RequestID, "small-linux", PolicyRevision(before.Config)); err == nil {
			t.Fatal("audit failure committed")
		}
		after := sqliteSnapshot(t, s)
		if PolicyRevision(before.Config) != PolicyRevision(after.Config) || before.Ready != after.Ready {
			t.Fatal("partial metadata escaped audit rollback")
		}
	})
	t.Run("handles", func(t *testing.T) {
		s, r, source := inactiveRouteFixture(t)
		other, err := OpenSQLite(s.path, Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		second, err := NewPoolRegistrar(other, source, r.controller, r.managementIdentity, r.journalLineage, []PoolRouteTemplate{r.templates["small-linux"]})
		if err != nil {
			t.Fatal(err)
		}
		revision := PolicyRevision(sqliteSnapshot(t, s).Config)
		var fresh atomic.Int32
		var wg sync.WaitGroup
		gate := make(chan struct{})
		var sequence atomic.Int64
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-gate
				reg := r
				if i%2 == 1 {
					reg = second
				}
				record, err := reg.RegisterInactive(context.Background(), source.facts.RequestID, "small-linux", revision)
				if err != nil {
					t.Error(err)
					return
				}
				if !record.Replay {
					fresh.Add(1)
				}
				sequence.CompareAndSwap(0, record.Identity.IdentitySequence)
				if sequence.Load() != record.Identity.IdentitySequence {
					t.Error("duplicate route identities")
				}
			}(i)
		}
		close(gate)
		wg.Wait()
		if fresh.Load() != 1 {
			t.Fatalf("fresh identities%d", fresh.Load())
		}
	})
}

func TestInactivePoolRouteReturnedRecordCannotMutateTrustedTemplate(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	revision := PolicyRevision(sqliteSnapshot(t, s).Config)
	original, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", revision)
	if err != nil {
		t.Fatal(err)
	}
	original.Identity.Template.Hosts[0] = "caller-new-host"
	original.Identity.Template.RepositoryIDs[0] = "44444444-4444-4444-8444-444444444444"
	original.Identity.Template.SourceFingerprints[0] = bindingHash("caller-new-issuer")
	original.Identity.Template.Execution.Profiles[0] = "arbitrary-profile"
	replay, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", revision)
	if err != nil || !replay.Replay || replay.Identity.Template.Hosts[0] != "a" || replay.Identity.Template.Execution.Profiles[0] != "runner" {
		t.Fatal("returned value mutated trusted catalog", err)
	}
}

func TestInactivePoolRouteHistoricalReplayDoesNotBecomeFreshSourceAuthority(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	revision := PolicyRevision(sqliteSnapshot(t, s).Config)
	first, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", revision)
	if err != nil {
		t.Fatal(err)
	}
	calls := source.calls.Load()
	source.err = errors.New("unavailable or unrelated remote drift")
	replay, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", revision)
	if err != nil || !replay.Replay || !replay.Equal(first) || source.calls.Load() != calls || replay.Mode != "inactive" {
		t.Fatal("historical return reread/reinterpreted or activated", err)
	}
	if sqliteSnapshot(t, s).Ready {
		t.Fatal("historical replay opened readiness")
	}
}
func TestInactivePoolRouteCommitUncertaintyNeverReturnsAnApparentlyUsableRecord(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	revision := PolicyRevision(sqliteSnapshot(t, s).Config)
	if _, err := s.db.Exec(`CREATE TABLE route_commit_parent(id INTEGER PRIMARY KEY);CREATE TABLE route_commit_child(id INTEGER REFERENCES route_commit_parent(id) DEFERRABLE INITIALLY DEFERRED);CREATE TRIGGER route_commit_fail AFTER INSERT ON pool_routes BEGIN INSERT INTO route_commit_child VALUES(9);END;`); err != nil {
		t.Fatal(err)
	}
	record, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", revision)
	if !errors.Is(err, ErrUncertain) || !reflect.DeepEqual(record, PoolRouteRecord{}) {
		t.Fatalf("ambiguous record escaped %+v %v", record, err)
	}
	if _, err := s.Snapshot(ctx); !errors.Is(err, ErrUncertain) {
		t.Fatal("writer not held closed", err)
	}
}

func TestInactivePoolRouteCannotBlessPermanentHistoryAfterPolicyRemoval(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	before := sqliteSnapshot(t, s)
	// Construct prior real plan/cancellation history for the SAME pool UUID,
	// then simulate a trusted offline policy removal. No public delete path exists.
	config := before.Config
	config.Pools[source.facts.PoolID] = config.Pools["app"]
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE metadata SET config=?", encoded); err != nil {
		t.Fatal(err)
	}
	request := placementRequest("historical-pool")
	request.Pool = source.facts.PoolID
	request.Key = executionKey(request.Execution, request.Pool)
	plan, err := s.AdmitPlacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelUndispatched(ctx, plan.Request); err != nil {
		t.Fatal(err)
	}
	delete(config.Pools, source.facts.PoolID)
	encoded, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE metadata SET config=?", encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(config)); !errors.Is(err, ErrConflict) {
		t.Fatal("removed policy laundered permanent plan history", err)
	}
}

type inactiveRouteSources map[string]DisabledPoolFacts

func (s inactiveRouteSources) ConfirmedDisabledPool(_ context.Context, id string) (DisabledPoolFacts, error) {
	facts, ok := s[id]
	if !ok {
		return DisabledPoolFacts{}, ErrPoolRoute
	}
	return facts, nil
}
func TestInactivePoolRouteDifferentIdentitiesSerializePolicyCASWithoutPartialInsertion(t *testing.T) {
	s, original, source := inactiveRouteFixture(t)
	ctx := context.Background()
	secondFacts := source.facts
	secondFacts.RequestID = "44444444-4444-4444-8444-444444444444"
	secondFacts.PoolID = "55555555-5555-4555-8555-555555555555"
	both := inactiveRouteSources{source.facts.RequestID: source.facts, secondFacts.RequestID: secondFacts}
	first, err := NewPoolRegistrar(s, both, original.controller, original.managementIdentity, original.journalLineage, []PoolRouteTemplate{original.templates["small-linux"]})
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	second, err := NewPoolRegistrar(other, both, original.controller, original.managementIdentity, original.journalLineage, []PoolRouteTemplate{original.templates["small-linux"]})
	if err != nil {
		t.Fatal(err)
	}
	revision := PolicyRevision(sqliteSnapshot(t, s).Config)
	gate := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-gate
		_, err := first.RegisterInactive(ctx, source.facts.RequestID, "small-linux", revision)
		results <- err
	}()
	go func() {
		<-gate
		_, err := second.RegisterInactive(ctx, secondFacts.RequestID, "small-linux", revision)
		results <- err
	}()
	close(gate)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrPolicyConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("CAS did not serialize", success, conflict)
	}
	var rows int
	if s.db.QueryRow("SELECT count(*) FROM pool_routes").Scan(&rows) != nil || rows != 1 {
		t.Fatal("partial registration")
	}
	state := sqliteSnapshot(t, s)
	if state.Ready || state.Allocated.Global.Runners != 0 {
		t.Fatal("registration race admitted/released")
	}
}

func TestInactivePoolRouteSchema9MigrationClosesOldFenceWithoutImportingExistingPools(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	request := placementRequest("prior-eight")
	prior, err := s.AdmitPlacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE pool_routes;DROP INDEX pool_route_execution_history;DROP TRIGGER inactive_pool_no_new_reservation;DROP TRIGGER inactive_pool_no_new_dispatch;PRAGMA user_version=8"); err != nil {
		t.Fatal(err)
	}
	path := s.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	snapshot := sqliteSnapshot(t, migrated)
	if snapshot.Ready || snapshot.Allocated.Global.CPU != 1000 {
		t.Fatal("upgrade admitted/released")
	}
	if err := migrated.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(snapshot.Config, migrated.now())}); !errors.Is(err, ErrFence) {
		t.Fatal("upgrade reused prior fence", err)
	}
	loaded, err := migrated.LookupExecutionPlan(ctx, prior.Request.Key)
	if err != nil || !reflect.DeepEqual(prior, loaded) {
		t.Fatal("upgrade reinterpreted existing plan", err)
	}
	var count int
	if migrated.db.QueryRow("SELECT count(*) FROM pool_routes").Scan(&count) != nil || count != 0 {
		t.Fatal("upgrade invented dynamic authority")
	}
}
func TestInactivePoolRouteArchiveRestoreKeepsIdentityAndCannotActivate(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	record, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(sqliteBackupDir(t), "archive.db")
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	if err := s.BackupSQLite(ctx, archive); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(ctx, archive, restored); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{archive, restored} {
		copy, err := OpenSQLite(path, Config{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := copy.LookupPoolRoute(ctx, source.facts.PoolID)
		if err != nil || !got.Equal(record) || sqliteSnapshot(t, copy).Ready {
			t.Fatal("closed artifact lost/activated catalog", err)
		}
		if _, err := copy.db.Exec("UPDATE pool_routes SET mode='active'"); err == nil {
			t.Fatal("archive activation invented")
		}
		copy.Close()
	}
}

func TestInactivePoolRouteOperatorCLIExportsFullClosedCatalog(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	// File-oriented export requires an explicitly private source directory.
	if err := os.Chmod(filepath.Dir(s.path), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	record, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "capacity-ledger")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", binary, "../../cmd/capacity-ledger")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("operator build %v %s", err, out)
	}
	archive := filepath.Join(sqliteBackupDir(t), "cli-archive.db")
	restored := filepath.Join(sqliteBackupDir(t), "cli-restored.db")
	for _, args := range [][]string{{"backup", "--source", s.path, "--destination", archive}, {"restore", "--source", archive, "--destination", restored, "--old-writers-fenced"}} {
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
			t.Fatalf("operator closed export %v %s", err, out)
		}
	}
	copy, err := OpenSQLite(restored, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	got, err := copy.LookupPoolRoute(ctx, source.facts.PoolID)
	if err != nil || !got.Equal(record) || sqliteSnapshot(t, copy).Ready {
		t.Fatal("operator archive lost or activated catalog", err)
	}
}
func TestInactivePoolRouteCorruptCanonicalHistoryCannotBeExported(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	if _, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TRIGGER pool_routes_no_update"); err != nil {
		t.Fatal(err)
	}
	var raw string
	if s.db.QueryRow("SELECT payload FROM pool_routes").Scan(&raw) != nil {
		t.Fatal("missing catalog")
	}
	raw = `{"unknown":1,` + raw[1:]
	sum := sha256.Sum256([]byte(raw))
	if _, err := s.db.Exec("UPDATE pool_routes SET payload=?,payload_hash=?", raw, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupPoolRoute(ctx, source.facts.PoolID); err == nil {
		t.Fatal("unknown canonical fields accepted")
	}
	if err := s.BackupSQLite(ctx, filepath.Join(sqliteBackupDir(t), "bad.db")); err == nil {
		t.Fatal("corrupt catalog exported")
	}
}

func TestInactivePoolRouteMissingPermanentHistoryCannotBeExported(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	if _, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config)); err != nil {
		t.Fatal(err)
	}
	// Owner-level corruption is not an isolation claim, but must not be blessed
	// into a validated archive merely because all remaining route rows look valid.
	if _, err := s.db.Exec("DROP TRIGGER pool_routes_no_delete; DELETE FROM pool_routes"); err != nil {
		t.Fatal(err)
	}
	var retained int
	if s.db.QueryRow("SELECT count(*) FROM audit WHERE kind='pool_route_registered_inactive'").Scan(&retained) != nil || retained != 1 {
		t.Fatal("missing permanent registration anchor")
	}
	if err := s.BackupSQLite(ctx, filepath.Join(sqliteBackupDir(t), "lost-history.db")); err == nil {
		t.Fatal("missing catalog history exported")
	}
}
func TestInactivePoolRouteCurrentBudgetDoesNotRewriteIdentity(t *testing.T) {
	s, r, source := inactiveRouteFixture(t)
	ctx := context.Background()
	original, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config))
	if err != nil {
		t.Fatal(err)
	}
	// Existing policy edits may narrow the current budget while the registration
	// identity retains the original declared plan/budget. Neither grants activation.
	budget := Resources{CPU: 500, RAM: 512, Runners: 1}
	if err := s.ApplyPolicyChange(ctx, PolicyChange{Revision: PolicyRevision(sqliteSnapshot(t, s).Config), Action: "pool_budget", Target: source.facts.PoolID, Budget: &budget, Reason: "narrow inactive route allowance"}); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(sqliteBackupDir(t), "narrowed.db")
	if err := s.BackupSQLite(ctx, archive); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenSQLite(archive, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	got, err := copy.LookupPoolRoute(ctx, source.facts.PoolID)
	if err != nil || !got.Equal(original) || sqliteSnapshot(t, copy).Config.Pools[source.facts.PoolID].Budget.CPU != 500 {
		t.Fatal("policy edit reinterpreted original inactive identity", err)
	}
}
