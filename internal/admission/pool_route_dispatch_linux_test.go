package admission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
)

func activeTestOnlyRoute(t *testing.T) (*SQLiteStore, PoolRouteRecord) {
	t.Helper()
	s, route := routeStateFixture(t)
	appendTestOnlyRouteState(t, s, route, 1, "active")
	readyTestRoute(t, s)
	return s, route
}
func readyTestRoute(t *testing.T, s *SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(sqliteSnapshot(t, s).Config, s.now())}); err != nil {
		t.Fatal(err)
	}
}
func routePlacementRequest(route PoolRouteRecord, name string, revision int64) PlacementRequest {
	image := route.Identity.Template.Execution
	image.Name = name
	return PlacementRequest{PoolRouteIdentitySequence: route.Identity.IdentitySequence, PoolRouteRevision: revision, Key: executionKey(image, route.Identity.PoolID), Pool: route.Identity.PoolID, Execution: image, Resources: route.Identity.Template.PerRunner, Hosts: append([]string(nil), route.Identity.Template.Hosts...), Strategy: route.Identity.Template.Strategy}
}
func routeSourceReceipt(t *testing.T, s *SQLiteStore, p ExecutionPlan, route PoolRouteRecord) OriginalPreparationReceipt {
	t.Helper()
	request := garmprotocol.PreparationRequest{Protocol: garmprotocol.PreparationVersion, Name: p.Execution.Name, PoolID: p.Request.Pool, InstanceID: "44444444-4444-4444-8444-444444444444", AgentID: 42, Generation: route.Identity.Generation, ProviderName: route.Identity.ProviderName, EntityType: "repository", EntityID: route.Identity.RepositoryID, ForgeEndpointName: route.Identity.Template.ForgeEndpointName, JITEnabled: *route.Identity.Template.JITEnabled}
	authority := PreparationAuthority{Controller: route.Identity.Controller, SourceScopeFingerprint: bindingHash("test route scope"), ClientFingerprint: route.Identity.Template.SourceFingerprints[0], Role: "source", PoolID: request.PoolID, ProviderName: request.ProviderName, EntityType: request.EntityType, EntityID: request.EntityID, ForgeEndpointName: request.ForgeEndpointName, Generation: request.Generation, JITEnabled: request.JITEnabled}
	result, err := s.RecordOriginalPreparation(context.Background(), request, authority)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestPoolRouteDispatchProvenanceAndClaimBeforeInactive(t *testing.T) {
	s, route := activeTestOnlyRoute(t)
	ctx := context.Background()
	p, err := s.AdmitPlacement(ctx, routePlacementRequest(route, "route-confirmed", 1))
	if err != nil {
		t.Fatal(err)
	}
	receipt := routeSourceReceipt(t, s, p, route)
	if receipt.PoolRouteIdentitySequence != p.PoolRouteIdentitySequence || receipt.PoolRouteRevision != p.PoolRouteRevision {
		t.Fatal("source receipt lost original route revision")
	}
	binding, err := s.LookupHostBinding(ctx, p.Request.Host)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		_, err := s.ExecutePlannedDispatch(ctx, p, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
			calls.Add(1)
			close(entered)
			<-release
			return DispatchReceipt{State: "confirmed", OperationID: "11111111-1111-4111-8111-111111111111", ResourceID: p.Execution.Name}, nil
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("claim did not commit")
	}
	if _, err := s.DeactivatePoolRoute(ctx, route.Identity.PoolID, route.Identity.IdentitySequence, 1, "local stop-new; not a native or job fence"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delivered callback did not finish")
	}
	record, err := s.LookupDispatchReadOnly(ctx, p.Request.Key)
	if err != nil || record.State != "confirmed" || record.PoolRouteIdentitySequence != p.PoolRouteIdentitySequence || record.PoolRouteRevision != 1 {
		t.Fatal("inactive erased original outcome/provenance", record, err)
	}
	if _, err := s.LookupExecutionPlan(ctx, p.Request.Key); err != nil {
		t.Fatal("historical plan unreadable when inactive", err)
	}
	if _, err := s.ExecutePlannedDispatch(ctx, p, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		calls.Add(1)
		return DispatchReceipt{}, nil
	}); !errors.Is(err, ErrDispatchClaimed) {
		t.Fatal("confirmed replay produced new permit", err)
	}
	if calls.Load() != 1 || sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("replayed or released delivered allocation")
	}
}
func TestPoolRouteDispatchInactiveBeforeClaimAndReactivationRetainCharge(t *testing.T) {
	s, route := activeTestOnlyRoute(t)
	ctx := context.Background()
	request := routePlacementRequest(route, "held-route", 1)
	p, err := s.AdmitPlacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	routeSourceReceipt(t, s, p, route)
	binding, err := s.LookupHostBinding(ctx, p.Request.Host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeactivatePoolRoute(ctx, route.Identity.PoolID, route.Identity.IdentitySequence, 1, "local stop-new; not a native or job fence"); err != nil {
		t.Fatal(err)
	}
	var calls int
	execute := func(context.Context, DispatchPermit) (DispatchReceipt, error) { calls++; return DispatchReceipt{}, nil }
	if _, err := s.ExecutePlannedDispatch(ctx, p, binding, execute); !errors.Is(err, ErrPoolRouteInactive) {
		t.Fatal("inactive granted new intent", err)
	}
	appendTestOnlyRouteState(t, s, route, 3, "active")
	if _, err := s.ExecutePlannedDispatch(ctx, p, binding, execute); !errors.Is(err, ErrPoolRouteInactive) {
		t.Fatal("reactivation reinterpreted old plan", err)
	}
	request.PoolRouteRevision = 3
	if _, err := s.AdmitPlacement(ctx, request); !errors.Is(err, ErrPoolRouteInactive) {
		t.Fatal("same-name replay replaced admission revision", err)
	}
	if _, err := s.LookupDispatchReadOnly(ctx, p.Request.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("refusal journaled native intent", err)
	}
	if calls != 0 || sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("inactive released or called backend")
	}
}
func TestPoolRouteAdmissionRequiresExactImmutableTemplatePolicy(t *testing.T) {
	for _, kind := range []string{"identity", "revision", "resources", "image", "hosts", "strategy"} {
		t.Run(kind, func(t *testing.T) {
			s, route := activeTestOnlyRoute(t)
			request := routePlacementRequest(route, "wrong-route-plan", 1)
			switch kind {
			case "identity":
				request.PoolRouteIdentitySequence++
			case "revision":
				request.PoolRouteRevision = 2
			case "resources":
				request.Resources.CPU++
			case "image":
				request.Execution.Fingerprint = strings.Repeat("b", 64)
			case "hosts":
				request.Hosts = []string{"a"}
			case "strategy":
				request.Strategy = "least_runners"
			}
			if _, err := s.AdmitPlacement(context.Background(), request); err == nil {
				t.Fatal("current route admitted altered immutable template")
			}
			if len(sqliteSnapshot(t, s).Reservations) != 0 {
				t.Fatal("failed route check charged")
			}
		})
	}
}
func TestPoolRouteOldOpenPreparedSQLDefaultsCannotBypassMigratedGuard(t *testing.T) {
	s, route := routeStateFixture(t)
	path := s.path
	if _, err := s.db.Exec(sqliteTestRemovePoolRouteGuardSchema); err != nil {
		t.Fatal(err)
	}
	// Reconstruct real schema9 inactive triggers, then retain prepared OLD SQL and
	// its open handle through migration. This is an old-SQL-contract test, not a
	// claim of running the old compiled binary or revoking delivered callbacks.
	ddl := sqliteInactivePoolRouteSchema
	fragment := ddl[strings.Index(ddl, "CREATE TRIGGER inactive_pool_no_new_reservation"):strings.Index(ddl, "PRAGMA user_version=9")]
	if _, err := s.db.Exec(fragment + "PRAGMA user_version=9;"); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	old.SetMaxOpenConns(1)
	reserve, err := old.Prepare("INSERT INTO reservations(request_key,pool,host,cpu,ram,runners,state,created_at) VALUES(?,?,?,?,?,1,'creating',?)")
	if err != nil {
		t.Fatal(err)
	}
	defer reserve.Close()
	dispatch, err := old.Prepare("INSERT INTO dispatches(reservation_key,permit_id,state,operation_id,resource_id,created_at,updated_at,host_binding_sequence) VALUES(?,?,'intent','','',?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	defer dispatch.Close()
	s.Close()
	current, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	appendTestOnlyRouteState(t, current, route, 1, "active")
	readyTestRoute(t, current)
	candidate := routePlacementRequest(route, "old-raw-attempt", 1)
	if _, err := reserve.Exec(candidate.Key, candidate.Pool, "a", 1000, 1024, current.now().UTC().Format(time.RFC3339Nano)); err == nil {
		t.Fatal("old prepared reservation SQL omitted provenance but succeeded")
	}
	p, err := current.AdmitPlacement(context.Background(), routePlacementRequest(route, "qualified-route", 1))
	if err != nil {
		t.Fatal(err)
	}
	at := current.now().UTC().Format(time.RFC3339Nano)
	if _, err := dispatch.Exec(p.Request.Key, "old-permit", at, at, p.HostBindingSequence); err == nil {
		t.Fatal("old prepared dispatch SQL omitted provenance but succeeded")
	}
	if _, err := current.LookupDispatchReadOnly(context.Background(), p.Request.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("old SQL minted intent", err)
	}
	if sqliteSnapshot(t, current).Allocated.Global.Runners != 1 {
		t.Fatal("old SQL added/released charge")
	}
}

func TestPoolRouteLocalInactiveCASAcrossHandlesAndAuditRollback(t *testing.T) {
	s, route := activeTestOnlyRoute(t)
	ctx := context.Background()
	other, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	results := make(chan error, 2)
	for _, store := range []*SQLiteStore{s, other} {
		go func(store *SQLiteStore) {
			_, err := store.DeactivatePoolRoute(ctx, route.Identity.PoolID, route.Identity.IdentitySequence, 1, "stop new provisioning only")
			results <- err
		}(store)
	}
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrPolicyConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("independent writers escaped revision CAS", successes, conflicts)
	}
	state, err := s.LookupPoolRouteState(ctx, route.Identity.PoolID)
	if err != nil || state.Revision != 2 || state.Mode != "inactive" || sqliteSnapshot(t, s).Ready {
		t.Fatal("local inactive not committed atomically", err)
	}
	before := sqliteSnapshot(t, s)
	replay, err := s.DeactivatePoolRoute(ctx, route.Identity.PoolID, route.Identity.IdentitySequence, 2, "already inactive no-op")
	if err != nil || !replay.Replay || replay.StateSequence != state.StateSequence || !reflect.DeepEqual(before, sqliteSnapshot(t, s)) {
		t.Fatal("inactive no-op wrote a false new event", err)
	}
}
func TestPoolRouteLocalInactiveAuditFailureAndUncertainCommitReturnNoACK(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			s, route := activeTestOnlyRoute(t)
			ctx := context.Background()
			before := sqliteSnapshot(t, s)
			statement := `CREATE TRIGGER reject_inactive BEFORE INSERT ON audit WHEN NEW.kind='pool_route_deactivated' BEGIN SELECT RAISE(ABORT,'fixture'); END;`
			if uncertain {
				statement = `CREATE TABLE state_parent(id INTEGER PRIMARY KEY);CREATE TABLE state_child(id INTEGER REFERENCES state_parent(id) DEFERRABLE INITIALLY DEFERRED);CREATE TRIGGER reject_inactive BEFORE INSERT ON audit WHEN NEW.kind='pool_route_deactivated' BEGIN INSERT INTO state_child(id) VALUES(999);END;`
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			state, err := s.DeactivatePoolRoute(ctx, route.Identity.PoolID, route.Identity.IdentitySequence, 1, "local inactive failure")
			if err == nil || !reflect.DeepEqual(state, PoolRouteState{}) {
				t.Fatal("failed transaction returned apparent inactive ACK", state, err)
			}
			if uncertain {
				if !errors.Is(err, ErrUncertain) || !s.poisoned.Load() {
					t.Fatal("uncertain writer remained usable", err)
				}
			} else {
				if !reflect.DeepEqual(before, sqliteSnapshot(t, s)) {
					t.Fatal("rolled-back inactive changed charge/readiness/history")
				}
			}
		})
	}
}
func TestPoolRouteArchivePreservesTransitionsAndOriginalProvenance(t *testing.T) {
	s, route := activeTestOnlyRoute(t)
	ctx := context.Background()
	p, err := s.AdmitPlacement(ctx, routePlacementRequest(route, "route-archive", 1))
	if err != nil {
		t.Fatal(err)
	}
	original := routeSourceReceipt(t, s, p, route)
	binding, err := s.LookupHostBinding(ctx, p.Request.Host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecutePlannedDispatch(ctx, p, binding, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		return DispatchReceipt{State: "confirmed", OperationID: "11111111-1111-4111-8111-111111111111", ResourceID: p.Execution.Name}, nil
	}); err != nil {
		t.Fatal(err)
	}
	inactive, err := s.DeactivatePoolRoute(ctx, route.Identity.PoolID, route.Identity.IdentitySequence, 1, "hold future native grants")
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(sqliteBackupDir(t), "route-state-archive.db")
	if err := s.BackupSQLite(ctx, archive); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenSQLite(archive, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	state, err := copy.LookupPoolRouteState(ctx, route.Identity.PoolID)
	if err != nil || !reflect.DeepEqual(state, inactive) {
		t.Fatal("archive lost inactive barrier history", err)
	}
	plan, err := copy.LookupExecutionPlan(ctx, p.Request.Key)
	if err != nil || !reflect.DeepEqual(plan, p) {
		t.Fatal("archive reinterpreted original plan", err)
	}
	receipt, err := copy.LookupOriginalPreparation(ctx, p.Request.Key)
	if err != nil || !reflect.DeepEqual(receipt, original) {
		t.Fatal("archive changed original source route", err)
	}
	dispatch, err := copy.LookupDispatchReadOnly(ctx, p.Request.Key)
	if err != nil || dispatch.PoolRouteIdentitySequence != p.PoolRouteIdentitySequence || dispatch.PoolRouteRevision != 1 || dispatch.State != "confirmed" {
		t.Fatal("archive changed dispatch authority history", err)
	}
	var kind string
	if copy.db.QueryRow("SELECT runtime_kind FROM ledger_lineage WHERE id=1").Scan(&kind) != nil || kind != "archive" || sqliteSnapshot(t, copy).Ready || sqliteSnapshot(t, copy).Allocated.Global.Runners != 1 {
		t.Fatal("archive activated/released allocation")
	}
	if _, err := s.db.Exec("DROP TRIGGER pool_route_states_no_delete;DELETE FROM pool_route_states WHERE revision=2"); err != nil {
		t.Fatal(err)
	}
	if err := s.BackupSQLite(ctx, filepath.Join(sqliteBackupDir(t), "missing-inactive.db")); err == nil {
		t.Fatal("archive erased newest inactive history")
	}
}

func TestPoolRouteSourceHistoryIsNotCurrentPublicationOrPollACK(t *testing.T) {
	s, route := activeTestOnlyRoute(t)
	ctx := context.Background()
	p, err := s.AdmitPlacement(ctx, routePlacementRequest(route, "held-source", 1))
	if err != nil {
		t.Fatal(err)
	}
	receipt := routeSourceReceipt(t, s, p, route)
	if _, err := s.DeactivatePoolRoute(ctx, route.Identity.PoolID, route.Identity.IdentitySequence, 1, "hold source publication and new native grants"); err != nil {
		t.Fatal(err)
	}
	before := sqliteSnapshot(t, s)
	historical, err := s.ValidateOriginalPreparation(ctx, receipt.Original, receipt.Source)
	if err != nil || !historical.Replay || historical.RecordSequence != receipt.RecordSequence {
		t.Fatal("historical metadata was erased", err)
	}
	if current, err := s.ValidateCurrentOriginalPreparation(ctx, receipt.Original, receipt.Source); !errors.Is(err, ErrPoolRouteInactive) || !reflect.DeepEqual(current, OriginalPreparationReceipt{}) {
		t.Fatal("metadata replay became current poll permission", current, err)
	}
	if current, err := s.RecordOriginalPreparation(ctx, receipt.Original, receipt.Source); !errors.Is(err, ErrPoolRouteInactive) || !reflect.DeepEqual(current, OriginalPreparationReceipt{}) {
		t.Fatal("inactive exact Record replay authorized row publication", current, err)
	}
	if !reflect.DeepEqual(before, sqliteSnapshot(t, s)) {
		t.Fatal("source hold changed charge/history")
	}
}
func TestPoolRouteStateOperatorCLIExportsExactRetainedTuple(t *testing.T) {
	s, route := activeTestOnlyRoute(t)
	ctx := context.Background()
	p, err := s.AdmitPlacement(ctx, routePlacementRequest(route, "route-cli", 1))
	if err != nil {
		t.Fatal(err)
	}
	original := routeSourceReceipt(t, s, p, route)
	inactive, err := s.DeactivatePoolRoute(ctx, route.Identity.PoolID, route.Identity.IdentitySequence, 1, "local stop-new before closed export")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "capacity-ledger")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", binary, "../../cmd/capacity-ledger")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("operator build %v %s", err, out)
	}
	archive := filepath.Join(sqliteBackupDir(t), "closed.db")
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	for _, args := range [][]string{{"backup", "--source", s.path, "--destination", archive}, {"restore", "--source", archive, "--destination", restored, "--old-writers-fenced"}} {
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
			t.Fatalf("operator export %v %s", err, out)
		}
	}
	copy, err := OpenSQLite(restored, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	state, err := copy.LookupPoolRouteState(ctx, route.Identity.PoolID)
	if err != nil || !reflect.DeepEqual(state, inactive) {
		t.Fatal("operator lost local inactive revision", err)
	}
	plan, err := copy.LookupExecutionPlan(ctx, p.Request.Key)
	if err != nil || !reflect.DeepEqual(plan, p) {
		t.Fatal("operator rewrote original admission tuple", err)
	}
	receipt, err := copy.LookupOriginalPreparation(ctx, p.Request.Key)
	if err != nil || !reflect.DeepEqual(receipt, original) {
		t.Fatal("operator lost source revision provenance", err)
	}
	var kind string
	if copy.db.QueryRow("SELECT runtime_kind FROM ledger_lineage WHERE id=1").Scan(&kind) != nil || kind != "restored" || sqliteSnapshot(t, copy).Ready || sqliteSnapshot(t, copy).Allocated.Global.Runners != 1 {
		t.Fatal("operator activated/reset retained charge")
	}
}
