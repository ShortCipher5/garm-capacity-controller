package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSQLiteBackupFilePreservesHistoricalSourceAndClearsFence(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "capacity-ledger")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", binary, "../../cmd/capacity-ledger")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s %v", output, err)
	}
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(sqliteBackupDir(t), "historical.db")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			file.Close()
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			schema := sqliteSchema
			if version == 2 {
				schema += sqliteDispatchSchema
			}
			if _, err = db.Exec(schema); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(fixture(time.Now()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("INSERT INTO metadata VALUES(1,?,0,1,'historical-fence',?)", encoded, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("INSERT INTO reservations VALUES('legacy-creating','app','a',1000,2048,1,'creating',?)", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			for _, viaCLI := range []bool{false, true} {
				t.Run(fmt.Sprintf("cli=%v", viaCLI), func(t *testing.T) {
					output := filepath.Join(sqliteBackupDir(t), "export.db")
					restoredPath := filepath.Join(sqliteBackupDir(t), "restore.db")
					if viaCLI {
						for _, args := range [][]string{{"backup", "--source", path, "--destination", output}, {"restore", "--source", output, "--destination", restoredPath, "--old-writers-fenced"}} {
							if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
								t.Fatalf("CLI historical export: %s %v", out, err)
							}
						}
					} else {
						if err := BackupSQLiteFile(context.Background(), path, output); err != nil {
							t.Fatal(err)
						}
						if err := RestoreSQLite(context.Background(), output, restoredPath); err != nil {
							t.Fatal(err)
						}
					}
					after, err := os.ReadFile(path)
					if err != nil || string(before) != string(after) {
						t.Fatal("historical source changed")
					}
					for _, artifact := range []string{output, restoredPath} {
						copy, err := OpenSQLite(artifact, Config{})
						if err != nil {
							t.Fatal(err)
						}
						defer copy.Close()
						state := sqliteSnapshot(t, copy)
						if state.Ready || state.Allocated.Global.Runners != 1 {
							t.Fatal("allocation lost or admission enabled")
						}
						var excluded, schema int
						if err := copy.db.QueryRow("SELECT count(*) FROM cancellation_history_exclusions WHERE request_key='legacy-creating'").Scan(&excluded); err != nil || excluded != 1 {
							t.Fatalf("legacy provenance exclusion lost: %d %v", excluded, err)
						}
						if err := copy.db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != sqliteCurrentVersion {
							t.Fatalf("artifact not current: %d %v", schema, err)
						}
						copiedState := sqliteSnapshot(t, copy)
						if len(copiedState.LegacyUnboundHosts) == 0 || len(copiedState.HostBindings) != 0 {
							t.Fatal("historical migration lost unqualified-host history")
						}

						if err := copy.CompleteReconciliation(context.Background(), Fence{Epoch: 1, Nonce: "historical-fence"}, InventorySnapshot{}); !errors.Is(err, ErrFence) {
							t.Fatalf("source fence survived: %v", err)
						}
						legacy := request("legacy-creating")
						if _, err := copy.CancelUndispatched(context.Background(), legacy); !errors.Is(err, ErrReconciliation) {
							t.Fatalf("blocked artifact authorized cleanup: %v", err)
						}
						reconcileSQLite(t, copy, nil)
						if _, err := copy.CancelUndispatched(context.Background(), legacy); !errors.Is(err, ErrDenied) {
							t.Fatalf("legacy exclusion lost after new baseline: %v", err)
						}
					}
				})
			}
		})
	}
}
