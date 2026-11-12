package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteBackupFilePreservesHistoricalSourceAndClearsFence(t *testing.T) {
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
			output := filepath.Join(sqliteBackupDir(t), "export.db")
			if err = BackupSQLiteFile(context.Background(), path, output); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("historical source changed")
			}
			copy, err := OpenSQLite(output, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer copy.Close()
			state := sqliteSnapshot(t, copy)
			if state.Ready || state.Allocated.Global.Runners != 1 {
				t.Fatal("allocation lost or admission enabled")
			}
			if err = copy.CompleteReconciliation(context.Background(), Fence{Epoch: 1, Nonce: "historical-fence"}, InventorySnapshot{}); err != ErrFence {
				t.Fatalf("source fence survived: %v", err)
			}
		})
	}
}
