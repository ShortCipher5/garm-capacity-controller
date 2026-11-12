package admission

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func sqliteBackupDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSQLiteBackupWALPreservesHistoryAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	for _, key := range []string{"intent", "waiting"} {
		if _, err := s.Admit(ctx, request(key)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ClaimDispatch(ctx, request("intent")); err != nil {
		t.Fatal(err)
	}
	reconcileSQLite(t, s, []InventoryResource{resource("unknown-orphan", "", 500, 512)})
	before := sqliteSnapshot(t, s)
	// Disable auto-checkpoint so committed reservation/audit pages remain in WAL.
	if _, err := s.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordState(ctx, "intent", "unknown"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sqliteBackupDir(t), "backup.db")
	if err := s.BackupSQLite(ctx, path); err != nil {
		t.Fatal(err)
	}
	if !sqliteSnapshot(t, s).Ready {
		t.Fatal("backup changed source readiness")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("backup requires sidecar %s: %v", suffix, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("artifact is not private: %v", err)
	}
	restoredPath := filepath.Join(sqliteBackupDir(t), "restored.db")
	if err := RestoreSQLite(ctx, path, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenSQLite(restoredPath, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	snapshot := sqliteSnapshot(t, restored)
	if snapshot.Ready || len(snapshot.Reservations) != 2 || len(snapshot.Inventory) != 1 || snapshot.Allocated.Global != before.Allocated.Global {
		t.Fatalf("lost accounting or ready restored snapshot: %+v", snapshot)
	}
	if snapshot.Reservations["intent"].State != "unknown" {
		t.Fatal("committed WAL state omitted")
	}
	record, err := restored.LookupDispatch(ctx, "intent")
	if err != nil || record.State != "intent" {
		t.Fatalf("dispatch intent lost: %+v %v", record, err)
	}
	if permit, err := restored.ClaimDispatch(ctx, request("waiting")); !errors.Is(err, ErrReconciliation) || permit != nil {
		t.Fatalf("dispatch before reconcile: %+v %v", permit, err)
	}
	if _, err := restored.Admit(ctx, request("new")); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("admission before reconcile: %v", err)
	}
	var count int
	if err := restored.db.QueryRow("SELECT count(*) FROM audit WHERE kind IN ('backup_created','backup_restored')").Scan(&count); err != nil || count != 2 {
		t.Fatalf("backup audit lost: %d %v", count, err)
	}
	// A resource and creating operation appeared after the historical backup.
	// They have no historical reservation; authoritative reconciliation imports
	// and charges them as orphans rather than inferring they do not exist.
	fence, err := restored.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap := InventorySnapshot{Hosts: observations(snapshot.Config, time.Now()), Resources: []InventoryResource{resource("post-backup-creating", "", 1000, 2048)}}
	if err := restored.CompleteReconciliation(ctx, fence, InventorySnapshot{}); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("incomplete recovery reopened: %v", err)
	}
	if err := restored.CompleteReconciliation(ctx, fence, snap); err != nil {
		t.Fatal(err)
	}
	snapshot = sqliteSnapshot(t, restored)
	if !snapshot.Ready || len(snapshot.Inventory) != 2 || snapshot.Allocated.Global.Runners != 4 {
		t.Fatalf("post-backup or missing resource not charged: %+v", snapshot)
	}
	if _, err := restored.ClaimDispatch(ctx, request("intent")); !errors.Is(err, ErrDispatchClaimed) {
		t.Fatalf("old intent repeated: %v", err)
	}
}

func TestSQLiteBackupInvalidatesActiveCollectorAndRestorationFence(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(sqliteBackupDir(t), "backup.db")
	if err := s.BackupSQLite(ctx, backup); err != nil {
		t.Fatal(err)
	}
	// The source's active collector still works; only the copy was invalidated.
	snapshot := InventorySnapshot{Hosts: observations(fixture(time.Now()), time.Now())}
	if err := s.CompleteReconciliation(ctx, fence, snapshot); err != nil {
		t.Fatalf("source fence changed: %v", err)
	}
	copied, err := OpenSQLite(backup, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := copied.CompleteReconciliation(ctx, fence, snapshot); !errors.Is(err, ErrFence) {
		t.Fatalf("old source fence reused: %v", err)
	}
	artifactFence, err := copied.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := copied.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sqliteBackupDir(t), "restored.db")
	if err := RestoreSQLite(ctx, backup, path); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.CompleteReconciliation(ctx, artifactFence, snapshot); !errors.Is(err, ErrFence) {
		t.Fatalf("backup's active collector reused after restore: %v", err)
	}
	reconcileSQLite(t, restored, nil)
	if !sqliteSnapshot(t, restored).Ready {
		t.Fatal("new reconciliation did not open restored copy")
	}
}

func TestSQLiteBackupConcurrentWriterConsistency(t *testing.T) {
	ctx := context.Background()
	config := fixture(time.Now())
	config.Global = Resources{CPU: 200000, RAM: 500000, Runners: 200}
	for name, host := range config.Hosts {
		host.Budget = config.Global
		config.Hosts[name] = host
	}
	for name, pool := range config.Pools {
		pool.Budget = config.Global
		config.Pools[name] = pool
	}
	sourcePath := filepath.Join(sqliteBackupDir(t), "source.db")
	source, err := OpenSQLite(sourcePath, config)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reconcileSQLite(t, source, nil)
	writer, err := OpenSQLite(sourcePath, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Admit(ctx, request("before-backup")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	writerErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if _, err := writer.Admit(ctx, request(fmt.Sprintf("concurrent-%d", i))); err != nil {
				writerErr <- err
				return
			}
		}
		writerErr <- nil
	}()
	path := filepath.Join(sqliteBackupDir(t), "backup.db")
	backupErr := source.BackupSQLite(ctx, path)
	wg.Wait()
	if err := <-writerErr; err != nil {
		t.Fatal(err)
	}
	if backupErr != nil {
		t.Fatal(backupErr)
	}
	copy, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	snapshot := sqliteSnapshot(t, copy)
	if _, ok := snapshot.Reservations["before-backup"]; !ok {
		t.Fatal("committed WAL reservation omitted")
	}
	var reserved int
	if err := copy.db.QueryRow("SELECT count(*) FROM audit WHERE kind='reserved'").Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != len(snapshot.Reservations) {
		t.Fatalf("torn snapshot: reserved audit=%d reservations=%d", reserved, len(snapshot.Reservations))
	}
}

func TestSQLiteBackupRestoreRefusesUnsafeDestinationsAndPartialInputs(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	dir := sqliteBackupDir(t)
	backup := filepath.Join(dir, "backup.db")
	if err := s.BackupSQLite(ctx, backup); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []func() error{func() error { return s.BackupSQLite(ctx, backup) }, func() error { return RestoreSQLite(ctx, backup, backup) }} {
		if err := action(); err == nil {
			t.Fatal("existing destination overwritten")
		}
		after, err := os.ReadFile(backup)
		if err != nil || string(after) != string(original) {
			t.Fatal("existing destination changed")
		}
	}
	symlink := filepath.Join(dir, "link.db")
	if err := os.Symlink(backup, symlink); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(ctx, symlink, filepath.Join(dir, "via-link.db")); err == nil {
		t.Fatal("accepted symlink source")
	}
	if err := s.BackupSQLite(ctx, symlink); err == nil {
		t.Fatal("overwrote symlink destination")
	}
	for _, contents := range [][]byte{[]byte("partial SQLite"), original[:len(original)/2], {}} {
		partial := filepath.Join(sqliteBackupDir(t), "partial.db")
		if err := os.WriteFile(partial, contents, 0600); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(sqliteBackupDir(t), "output.db")
		if err := RestoreSQLite(ctx, partial, output); err == nil {
			t.Fatal("accepted incomplete backup")
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("published partial restore: %v", err)
		}
	}
	sidecarDestination := filepath.Join(dir, "sidecar.db")
	if err := os.WriteFile(sidecarDestination+"-wal", []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(ctx, backup, sidecarDestination); err == nil {
		t.Fatal("accepted existing destination WAL")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	output := filepath.Join(dir, "canceled.db")
	if err := RestoreSQLite(canceled, backup, output); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published canceled restore: %v", err)
	}
	public := filepath.Join(dir, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.BackupSQLite(ctx, filepath.Join(public, "out.db")); err == nil {
		t.Fatal("accepted nonprivate parent")
	}
	if err := os.Chmod(backup, 0644); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(ctx, backup, filepath.Join(dir, "public-source.db")); err == nil {
		t.Fatal("accepted public source")
	}
	s.poisoned.Store(true)
	if err := s.BackupSQLite(ctx, filepath.Join(dir, "poisoned.db")); !errors.Is(err, ErrUncertain) {
		t.Fatalf("backed up poisoned handle: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".ledger-backup-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary artifacts leaked: %v %v", leftovers, err)
	}
}

func TestSQLiteBackupCancellationCleansPartialArtifact(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	// Occupy the source connection so export has created its private staging
	// file, but cannot acquire a snapshot. Cancellation must not publish it.
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	dir := sqliteBackupDir(t)
	output := filepath.Join(dir, "interrupted.db")
	if err := s.BackupSQLite(ctx, output); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ignored interruption: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial artifact survived canceled export: %v %v", entries, err)
	}
}

func TestSQLiteBackupConcurrentPublicationNeverOverwrites(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	dir := sqliteBackupDir(t)
	output := filepath.Join(dir, "new.db")
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- s.BackupSQLite(context.Background(), output) }()
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("no-overwrite publication winners: %d", successes)
	}
	copy, err := OpenSQLite(output, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if sqliteSnapshot(t, copy).Ready {
		t.Fatal("publication became ready")
	}
}
