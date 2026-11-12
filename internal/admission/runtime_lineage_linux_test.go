package admission

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func runtimeFixture(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(sqliteBackupDir(t), "ledger.db")
	identity := filepath.Join(sqliteBackupDir(t), "identity")
	if err := InitializeSQLite(path, identity, fixture(time.Now())); err != nil {
		t.Fatal(err)
	}
	return path, identity
}

func ledgerFiles(t *testing.T, path string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		raw, err := os.ReadFile(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[suffix] = string(raw)
	}
	return result
}

func TestRuntimeRejectsBeforeChangingDatabaseOrSidecars(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "foreign", "wrong-id", "offline", "legacy", "archive", "restored", "missing-identity", "invalid-identity", "symlink", "public-parent"} {
		t.Run(mode, func(t *testing.T) {
			path, identity := runtimeFixture(t)
			switch mode {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				db, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec("PRAGMA application_id=123"); err != nil {
					t.Fatal(err)
				}
				db.Close()
			case "wrong-id":
				if err := os.WriteFile(identity, []byte(strings.Repeat("f", 64)+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "offline":
				path = filepath.Join(sqliteBackupDir(t), "offline.db")
				s, err := OpenSQLite(path, fixture(time.Now()))
				if err != nil {
					t.Fatal(err)
				}
				s.Close()
			case "legacy":
				db, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(sqliteTestRemovePoolRouteGuardSchema + "DROP TABLE pool_routes; DROP INDEX pool_route_execution_history; DROP TRIGGER IF EXISTS inactive_pool_no_new_reservation; DROP TRIGGER IF EXISTS inactive_pool_no_new_dispatch; DROP TRIGGER active_dispatch_requires_preparation; DROP TRIGGER dispatch_preparation_sequence_immutable; DROP TABLE original_preparations; DROP TABLE original_preparation_history_exclusions; ALTER TABLE dispatches DROP COLUMN original_preparation_sequence; DROP TABLE ledger_lineage; DROP TABLE execution_plans; PRAGMA user_version=5"); err != nil {
					t.Fatal(err)
				}
				db.Close()
			case "archive", "restored":
				target := filepath.Join(sqliteBackupDir(t), "artifact.db")
				var err error
				if mode == "archive" {
					err = BackupSQLiteFile(context.Background(), path, target)
				} else {
					err = RestoreSQLite(context.Background(), path, target)
				}
				if err != nil {
					t.Fatal(err)
				}
				path = target
			case "missing-identity":
				if err := os.Remove(identity); err != nil {
					t.Fatal(err)
				}
			case "invalid-identity":
				if err := os.WriteFile(identity, []byte("untrusted\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				alias := filepath.Join(sqliteBackupDir(t), "link.db")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				path = alias
			case "public-parent":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			}
			before := ledgerFiles(t, path)
			s, err := OpenRuntimeSQLite(path, identity)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrRuntimeLineage) {
				t.Fatalf("accepted %s: %v", mode, err)
			}
			if after := ledgerFiles(t, path); !reflect.DeepEqual(before, after) {
				t.Fatal("rejection changed database/sidecars")
			}
		})
	}
}

func TestRuntimeWALReopenAndPathReplacementFailClosed(t *testing.T) {
	ctx := context.Background()
	path, identity := runtimeFixture(t)
	s, err := OpenRuntimeSQLite(path, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	reconcileSQLite(t, s, nil)
	if _, err := s.Admit(ctx, request("in-wal")); err != nil {
		t.Fatal(err)
	}
	other, err := OpenRuntimeSQLite(path, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	state := sqliteSnapshot(t, other)
	if _, ok := state.Reservations["in-wal"]; !ok {
		t.Fatal("WAL reservation missing")
	}
	if err := os.Rename(path, path+".removed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(ctx); !errors.Is(err, ErrRuntimeLineage) {
		t.Fatalf("deleted path accepted: %v", err)
	}
	if _, err := s.ReadSnapshot(ctx); !errors.Is(err, ErrRuntimeLineage) {
		t.Fatalf("read used replaced path: %v", err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(ctx, request("after-replace")); !errors.Is(err, ErrRuntimeLineage) {
		t.Fatalf("replacement accepted: %v", err)
	}
}

func TestRuntimeArtifactsCannotActivateEvenAfterOfflineReconciliation(t *testing.T) {
	ctx := context.Background()
	path, identity := runtimeFixture(t)
	s, err := OpenRuntimeSQLite(path, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reconcileSQLite(t, s, nil)
	if _, err := s.Admit(ctx, request("charged")); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(sqliteBackupDir(t), "backup.db")
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	if err := s.BackupSQLite(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSQLite(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{backup, restored} {
		offline, err := OpenSQLite(artifact, Config{})
		if err != nil {
			t.Fatal(err)
		}
		reconcileSQLite(t, offline, nil)
		if _, err := offline.db.Exec("UPDATE ledger_lineage SET runtime_kind='active'"); err == nil {
			t.Fatal("artifact activated")
		}
		if _, err := offline.db.Exec("UPDATE ledger_lineage SET lineage_id=?", strings.Repeat("a", 64)); err == nil {
			t.Fatal("identity changed")
		}
		if _, err := offline.db.Exec("DELETE FROM ledger_lineage"); err == nil {
			t.Fatal("identity removed")
		}
		state := sqliteSnapshot(t, offline)
		if _, ok := state.Reservations["charged"]; !ok {
			t.Fatal("charge lost")
		}
		offline.Close()
		before := ledgerFiles(t, artifact)
		runtime, err := OpenRuntimeSQLite(artifact, identity)
		if runtime != nil {
			runtime.Close()
		}
		if !errors.Is(err, ErrRuntimeLineage) {
			t.Fatalf("artifact activated: %v", err)
		}
		if !reflect.DeepEqual(before, ledgerFiles(t, artifact)) {
			t.Fatal("artifact rejection mutated state")
		}
	}
}

func TestRuntimeInitializeRefusesExistingOutputsAndSidecars(t *testing.T) {
	for _, mode := range []string{"database", "empty", "identity", "symlink", "wal", "shm", "journal", "invalid-policy"} {
		t.Run(mode, func(t *testing.T) {
			dir := sqliteBackupDir(t)
			path := filepath.Join(dir, "ledger.db")
			identity := filepath.Join(dir, "identity")
			config := fixture(time.Now())
			existing := ""
			switch mode {
			case "database", "empty":
				existing = path
			case "identity":
				existing = identity
			case "symlink":
				if err := os.Symlink("missing", path); err != nil {
					t.Fatal(err)
				}
			case "wal", "shm", "journal":
				existing = path + "-" + mode
			case "invalid-policy":
				config = Config{}
			}
			if existing != "" {
				raw := []byte("preserve")
				if mode == "empty" {
					raw = nil
				}
				if err := os.WriteFile(existing, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := ledgerFiles(t, path)
			if err := InitializeSQLite(path, identity, config); err == nil {
				t.Fatal("unsafe initialization accepted")
			}
			if mode != "symlink" && !reflect.DeepEqual(before, ledgerFiles(t, path)) {
				t.Fatal("initialization changed existing files")
			}
			if mode == "invalid-policy" {
				if _, err := os.Stat(identity); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("invalid config created identity")
				}
			}
		})
	}
}
