package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func bindingHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
func bindingFixture(t *testing.T, s *SQLiteStore) (Fence, []HostBinding) {
	t.Helper()
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, s)
	values := []HostBinding{}
	for host := range state.Config.Hosts {
		values = append(values, HostBinding{Host: host, ServerKeyFingerprint: bindingHash("key-" + host), OriginFingerprint: bindingHash("origin-" + host), ScopeFingerprint: bindingHash("scope-" + host), ServerName: "server-" + host, ServerVersion: "6.0.4", ObservedAt: s.now()})
	}
	return fence, values
}
func TestHostBindingPersistsUniqueCompleteSetWithoutOpeningAdmission(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	fence, values := bindingFixture(t, s)
	if err := s.BindHostIdentities(context.Background(), fence, values); err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, s)
	if state.Ready || len(state.HostBindings) != len(values) {
		t.Fatal("binding opened admission or lost scope")
	}
	for _, value := range values {
		got, err := s.LookupHostBinding(context.Background(), value.Host)
		if err != nil || !got.Matches(value) || got.Sequence <= 0 {
			t.Fatalf("lookup %+v %v", got, err)
		}
	}
	other, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, value := range values {
		got, err := other.LookupHostBinding(context.Background(), value.Host)
		if err != nil || !got.Matches(value) || got.Sequence <= 0 {
			t.Fatal("reopen lost binding")
		}
	}
	next, reobserved := bindingFixture(t, s)
	if err := other.BindHostIdentities(context.Background(), next, reobserved); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range sqliteSnapshot(t, s).Audit {
		if event.Kind == "host_identity_bound" {
			count++
		}
	}
	if count != len(values) {
		t.Fatal("binding retry duplicated audit or identity")
	}
	for _, statement := range []string{"DELETE FROM host_bindings", "UPDATE host_bindings SET server_name='changed'"} {
		if _, err := s.db.Exec(statement); err == nil {
			t.Fatal("binding mutable")
		}
	}
}
func TestHostBindingRejectsAliasesAndInvalidObservationAtomically(t *testing.T) {
	for _, mode := range []string{"key", "origin", "scope", "name", "alias", "missing", "unknown", "future", "prebarrier", "stale-fence"} {
		t.Run(mode, func(t *testing.T) {
			s := sqliteFixtureStore(t, fixture(time.Now()), false)
			fence, values := bindingFixture(t, s)
			switch mode {
			case "key":
				values[1].ServerKeyFingerprint = values[0].ServerKeyFingerprint
			case "origin":
				values[1].OriginFingerprint = values[0].OriginFingerprint
			case "scope":
				values[1].ScopeFingerprint = values[0].ScopeFingerprint
			case "name":
				values[1].ServerName = values[0].ServerName
			case "alias":
				values[1].Host = values[0].Host
			case "missing":
				values = values[:1]
			case "unknown":
				values[0].Host = "unknown-host"
			case "future":
				values[0].ObservedAt = s.now().Add(time.Second)
			case "prebarrier":
				values[0].ObservedAt = s.now().Add(-time.Second)
			case "stale-fence":
				if _, err := s.BeginReconciliation(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.BindHostIdentities(context.Background(), fence, values); err == nil {
				t.Fatal("invalid scope accepted")
			}
			state := sqliteSnapshot(t, s)
			if state.Ready || len(state.HostBindings) != 0 {
				t.Fatal("partial binding or ready escaped failed batch")
			}
		})
	}
}
func TestHostBindingRejectsDriftAndRacingInconsistentProcesses(t *testing.T) {
	for _, mode := range []string{"key", "origin", "scope", "name", "version"} {
		t.Run(mode, func(t *testing.T) {
			s := sqliteFixtureStore(t, fixture(time.Now()), false)
			fence, values := bindingFixture(t, s)
			if err := s.BindHostIdentities(context.Background(), fence, values); err != nil {
				t.Fatal(err)
			}
			fence, values = bindingFixture(t, s)
			switch mode {
			case "key":
				values[0].ServerKeyFingerprint = bindingHash("newkey")
			case "origin":
				values[0].OriginFingerprint = bindingHash("neworigin")
			case "scope":
				values[0].ScopeFingerprint = bindingHash("another-project")
			case "name":
				values[0].ServerName = "renamed-server"
			case "version":
				values[0].ServerVersion = "6.0.5"
			}
			if err := s.BindHostIdentities(context.Background(), fence, values); !errors.Is(err, ErrHostBinding) {
				t.Fatalf("drift accepted %v", err)
			}
		})
	}
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	fence, first := bindingFixture(t, s)
	second := append([]HostBinding(nil), first...)
	for i := range second {
		second[i].ServerKeyFingerprint = bindingHash("other-" + second[i].Host)
	}
	other, err := OpenSQLite(s.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i, store := range []*SQLiteStore{s, other} {
		wg.Add(1)
		go func(i int, store *SQLiteStore) {
			defer wg.Done()
			values := first
			if i == 1 {
				values = second
			}
			err := store.BindHostIdentities(context.Background(), fence, values)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrHostBinding) {
				t.Errorf("unexpected race failure %v", err)
			}
		}(i, store)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("inconsistent simultaneous scopes both accepted")
	}
	state := sqliteSnapshot(t, s)
	matchesFirst, matchesSecond := true, true
	for i := range first {
		matchesFirst = matchesFirst && state.HostBindings[first[i].Host].Matches(first[i])
		matchesSecond = matchesSecond && state.HostBindings[second[i].Host].Matches(second[i])
	}
	if matchesFirst == matchesSecond {
		t.Fatal("binding batch mixed identities")
	}
}
func TestHostBindingAuditFailureRollsBackWholeBatch(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	fence, values := bindingFixture(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_binding_audit BEFORE INSERT ON audit WHEN NEW.kind='host_identity_bound' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.BindHostIdentities(context.Background(), fence, values); err == nil {
		t.Fatal("binding committed without audit")
	}
	if len(sqliteSnapshot(t, s).HostBindings) != 0 {
		t.Fatal("audit failure left partial identity")
	}
}
func TestHostBindingMigrationPreservesChargesAndLegacyExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-v3.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{sqliteSchema, sqliteDispatchSchema, sqliteCancellationSchema} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	config := fixture(time.Now())
	raw, _ := json.Marshal(config)
	if _, err := db.Exec("INSERT INTO metadata VALUES(1,?,1,8,'old-nonce','')", raw); err != nil {
		t.Fatal(err)
	}
	req := request("legacy-binding")
	if _, err := db.Exec("INSERT INTO reservations VALUES(?,?,?,?,?,?,'creating',?)", req.Key, req.Pool, req.Host, req.Resources.CPU, req.Resources.RAM, req.Resources.Runners, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state := sqliteSnapshot(t, s)
	if state.Ready || state.Allocated.Global.Runners != 1 {
		t.Fatal("upgrade preserved ready or reset charge")
	}
	var nonce string
	s.db.QueryRow("SELECT nonce FROM metadata").Scan(&nonce)
	if nonce != "" {
		t.Fatal("upgrade retained old fence")
	}
	fence, values := bindingFixture(t, s)
	if err := s.BindHostIdentities(context.Background(), fence, values); !errors.Is(err, ErrLegacyHostBinding) {
		t.Fatalf("legacy host auto-blessed %v", err)
	}
	reconcileSQLite(t, s, nil)
	if _, err := s.CancelUndispatched(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	fence, values = bindingFixture(t, s)
	if err := s.BindHostIdentities(context.Background(), fence, values); !errors.Is(err, ErrLegacyHostBinding) {
		t.Fatal("later cancellation erased missing binding provenance")
	}
	if _, err := s.db.Exec("DELETE FROM host_binding_history_exclusions"); err == nil {
		t.Fatal("legacy binding exclusions mutable")
	}
}
func TestBoundDispatchRequiresSamePersistedIdentity(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	fence, values := bindingFixture(t, s)
	if err := s.BindHostIdentities(ctx, fence, values); err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, s)
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(state.Config, s.now())}); err != nil {
		t.Fatal(err)
	}
	req := reserveDispatch(t, s, "bound")
	binding := sqliteSnapshot(t, s).HostBindings[req.Host]
	wrong := binding
	wrong.ServerKeyFingerprint = bindingHash("wrong")
	calls := 0
	execute := func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		calls++
		return confirmedReceipt(), nil
	}
	if _, err := s.ExecuteDispatchBound(ctx, req, wrong, execute); !errors.Is(err, ErrHostBinding) || calls != 0 {
		t.Fatal("mismatched physical backend dispatched")
	}
	if _, err := s.ExecuteDispatchBound(ctx, req, binding, execute); err != nil || calls != 1 {
		t.Fatal(err)
	}
	record, err := s.LookupDispatch(ctx, req.Key)
	if err != nil || record.HostBindingSequence != binding.Sequence {
		t.Fatal("dispatch lost immutable binding provenance")
	}
	if _, err := s.db.Exec("UPDATE dispatches SET host_binding_sequence=NULL WHERE reservation_key=?", req.Key); err == nil {
		t.Fatal("dispatch binding provenance mutable")
	}

	if _, err := s.ExecuteDispatchBound(ctx, req, binding, execute); !errors.Is(err, ErrDispatchClaimed) || calls != 1 {
		t.Fatal("bound replay dispatched")
	}
}
func TestHostBindingBackupRestorePreservesIdentityAndBlock(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	fence, values := bindingFixture(t, s)
	if err := s.BindHostIdentities(ctx, fence, values); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	backup := filepath.Join(directory, "backup.db")
	restored := filepath.Join(directory, "restored.db")
	if err := s.BackupSQLite(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenSQLite(restored, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	state := sqliteSnapshot(t, opened)
	if state.Ready || len(state.HostBindings) != len(values) {
		t.Fatal("restore lost binding or became ready")
	}
	for _, value := range values {
		got, err := opened.LookupHostBinding(ctx, value.Host)
		if err != nil || !got.Matches(value) || got.Sequence <= 0 {
			t.Fatal("restored identity mismatch")
		}
	}
	if err := opened.BindHostIdentities(ctx, fence, values); !errors.Is(err, ErrFence) {
		t.Fatal("old collector fence reused after restore")
	}
}

func TestHostBindingProcessWorker(t *testing.T) {
	path := os.Getenv("CAPACITY_HOST_BINDING_TEST_PATH")
	if path == "" {
		return
	}
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	epoch, err := strconv.ParseInt(os.Getenv("CAPACITY_HOST_BINDING_TEST_EPOCH"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	fence := Fence{Epoch: epoch, Nonce: os.Getenv("CAPACITY_HOST_BINDING_TEST_NONCE")}
	awaitProcessStart(t)
	state := sqliteSnapshot(t, s)
	values := []HostBinding{}
	for host := range state.Config.Hosts {
		values = append(values, HostBinding{Host: host, ServerKeyFingerprint: bindingHash(os.Getenv("CAPACITY_HOST_BINDING_TEST_VARIANT") + host), OriginFingerprint: bindingHash("origin-" + host), ScopeFingerprint: bindingHash("scope-" + host), ServerName: "server-" + host, ServerVersion: "6.0.4", ObservedAt: s.now()})
	}
	if err := s.BindHostIdentities(context.Background(), fence, values); err != nil && !errors.Is(err, ErrHostBinding) {
		t.Fatal(err)
	}
}
func TestHostBindingRacingProcessesCannotMixBackendSets(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runProcessContenders(t, 12, "^TestHostBindingProcessWorker$", func(i int) []string {
		variant := "one-"
		if i%2 == 0 {
			variant = "two-"
		}
		return []string{"CAPACITY_HOST_BINDING_TEST_PATH=" + s.path, "CAPACITY_HOST_BINDING_TEST_EPOCH=" + strconv.FormatInt(fence.Epoch, 10), "CAPACITY_HOST_BINDING_TEST_NONCE=" + fence.Nonce, "CAPACITY_HOST_BINDING_TEST_VARIANT=" + variant}
	})
	state := sqliteSnapshot(t, s)
	one, two := true, true
	for host := range state.Config.Hosts {
		value, ok := state.HostBindings[host]
		if !ok {
			t.Fatal("partial host binding set")
		}
		one = one && value.ServerKeyFingerprint == bindingHash("one-"+host)
		two = two && value.ServerKeyFingerprint == bindingHash("two-"+host)
	}
	if one == two {
		t.Fatal("separate processes mixed incompatible physical identities")
	}
}
