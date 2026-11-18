package admission

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRecoveryAssessmentDoesNotMutateOrAuthorize(t *testing.T) {
	ctx := context.Background()
	store := sqliteFixtureStore(t, fixture(time.Now()), false)
	if err := os.Chmod(filepath.Dir(store.path), 0700); err != nil {
		t.Fatal(err)
	}
	reconcileSQLite(t, store, nil)
	for _, key := range []string{"a", "b", "c"} {
		if _, err := store.Admit(ctx, request(key)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ClaimDispatch(ctx, request("b")); err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report, err := AssessRecoverySQLiteFile(ctx, store.path, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if report.ResumeAuthorized || len(report.Blockers) == 0 || report.NextAfter != "b" || len(report.Entries) != 2 {
		t.Fatalf("unsafe report: %+v", report)
	}
	if report.Entries[1].RequiredAction != "retain_charge_resolve_original_dispatch_without_redispatch" || !report.Entries[1].LedgerCharged {
		t.Fatalf("lost intent: %+v", report)
	}
	tail, err := AssessRecoverySQLiteFile(ctx, store.path, report.NextAfter, 2)
	if err != nil {
		t.Fatal(err)
	}
	if tail.NextAfter != "" || len(tail.Entries) != 1 || tail.Entries[0].RequestKey != "c" {
		t.Fatalf("bad tail %+v", tail)
	}
	after, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("assessment mutated ledger")
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := AssessRecoverySQLiteFile(ctx, path, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, replay) {
		t.Fatal("closed ledger changed assessment")
	}
}

func TestRecoveryAssessmentDeletingRemainsCharged(t *testing.T) {
	store, req, _ := releaseTestFixture(t)
	if err := os.Chmod(filepath.Dir(store.path), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDeleting(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	report, err := AssessRecoverySQLiteFile(context.Background(), store.path, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || !report.Entries[0].LedgerCharged || report.Entries[0].RequiredAction != "retain_charge_do_not_repeat_delete" {
		t.Fatalf("bad deleting report %+v", report)
	}
}

func TestRecoveryAssessmentRefusesMissingInvalidAndLegacy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := AssessRecoverySQLiteFile(ctx, path, "", 10); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing ledger created")
	}
	store := sqliteFixtureStore(t, fixture(time.Now()), false)
	for _, limit := range []int{0, 101, -1} {
		if _, err := AssessRecoverySQLiteFile(ctx, store.path, "", limit); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	if err := os.Chmod(filepath.Dir(store.path), 0700); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := AssessRecoverySQLiteFile(cancelled, store.path, "", 10); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	if _, err := store.db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := AssessRecoverySQLiteFile(ctx, store.path, "", 10); err == nil {
		t.Fatal("legacy ledger accepted or migrated")
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("schema changed")
	}
}

func TestRecoveryAssessmentFlagsHistoricalRelease(t *testing.T) {
	store, req, _ := releaseTestFixture(t)
	if err := os.Chmod(filepath.Dir(store.path), 0700); err != nil {
		t.Fatal(err)
	}
	// Seed historical data directly: the current public release API deliberately
	// cannot mint a tombstone from unqualified project observations.
	_, err := store.db.Exec(`INSERT INTO releases(request_key,pool,host,cpu,ram,disk,runners,absence_digest,record,released_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, req.Key, req.Pool, req.Host, req.Resources.CPU, req.Resources.RAM, req.Resources.Disk, 1, req.Key, []byte("{}"), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	report, err := AssessRecoverySQLiteFile(context.Background(), store.path, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || !report.Entries[0].HistoricalRelease || report.Entries[0].LedgerCharged || report.Entries[0].RequiredAction != "review_historical_release_against_authoritative_inventory" {
		t.Fatalf("historical release hidden: %+v", report)
	}
	if _, err := store.LookupRelease(context.Background(), req.Key); err != nil {
		t.Fatal("assessment rewrote historical release")
	}
}
