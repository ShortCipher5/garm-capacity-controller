//go:build linux && stock_read_pilot_harness

package admission

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPilotFixtureReadonlyCanonical(t *testing.T) {
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	path, idpath := filepath.Join(dir, "ledger.db"), filepath.Join(dir, "identity")
	cfg := fixture(time.Now())
	if e := InitializeSQLite(path, idpath, cfg); e != nil {
		t.Fatal(e)
	}
	identity, e := os.ReadFile(idpath)
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	// Pin comes from the actual unchanged initializer, never a fabricated schema.
	db, e := sql.Open("sqlite3", "file:"+path+"?mode=ro&immutable=1&_query_only=true")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	got, _, e := pilotFixtureSchema(context.Background(), tx)
	tx.Rollback()
	db.Close()
	if e != nil || got != pilotFixtureSchemaSHA256 {
		t.Fatalf("canonical schema fingerprint %s: %v", got, e)
	}
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e := VerifyPilotFixtureSQLite(context.Background(), f, identity, cfg); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("database changed")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, e := os.Lstat(path + suffix); !os.IsNotExist(e) {
			t.Fatal("sidecar created")
		}
		if e := os.WriteFile(path+suffix, []byte("present"), 0600); e != nil {
			t.Fatal(e)
		}
		if VerifyPilotFixtureSQLite(context.Background(), f, identity, cfg) == nil {
			t.Fatal("sidecar ignored")
		}
		os.Remove(path + suffix)
	}
	other := cfg
	other.Global.CPU++
	if VerifyPilotFixtureSQLite(context.Background(), f, identity, other) == nil {
		t.Fatal("policy mismatch")
	}
	wrong := bytes.Clone(identity)
	wrong[0] = 'z'
	if VerifyPilotFixtureSQLite(context.Background(), f, wrong, cfg) == nil {
		t.Fatal("identity mismatch")
	}
	writable, e := os.OpenFile(path, os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer writable.Close()
	if VerifyPilotFixtureSQLite(context.Background(), writable, identity, cfg) == nil {
		t.Fatal("writable descriptor")
	}
	for _, statement := range []string{"UPDATE metadata SET ready=1", "UPDATE metadata SET epoch=1", "INSERT INTO audit(kind,request_key,recorded_at) VALUES('activity','','2026-01-01T00:00:00Z')", "CREATE TABLE unexpected(id INTEGER)", "DROP TABLE inventory", "DROP TRIGGER ledger_lineage_no_delete", "PRAGMA user_version=27", "UPDATE cancellation_totals SET total=1", "UPDATE broker_policy SET revision=1"} {
		t.Run(statement, func(t *testing.T) {
			d := t.TempDir()
			os.Chmod(d, 0700)
			p := filepath.Join(d, "ledger.db")
			if e := os.WriteFile(p, before, 0600); e != nil {
				t.Fatal(e)
			}
			db, e := sql.Open("sqlite3", p)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = db.Exec(statement); e != nil {
				t.Fatal(e)
			}
			db.Close()
			bad, e := os.Open(p)
			if e != nil {
				t.Fatal(e)
			}
			defer bad.Close()
			if VerifyPilotFixtureSQLite(context.Background(), bad, identity, cfg) == nil {
				t.Fatal("changed state accepted")
			}
		})
	}
}

func TestPilotFixtureReadonlyDescriptorRefusals(t *testing.T) {
	cfg := fixture(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if VerifyPilotFixtureSQLite(ctx, nil, nil, cfg) == nil || VerifyPilotFixtureSQLite(nil, nil, nil, cfg) == nil {
		t.Fatal("invalid context/descriptor")
	}
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	p, id := filepath.Join(dir, "ledger.db"), filepath.Join(dir, "identity")
	if e := InitializeSQLite(p, id, cfg); e != nil {
		t.Fatal(e)
	}
	identity, e := os.ReadFile(id)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	alias := filepath.Join(dir, "alias")
	if e := os.Symlink(p, alias); e != nil {
		t.Fatal(e)
	}
	linked, e := os.Open(alias)
	if e != nil {
		t.Fatal(e)
	}
	defer linked.Close()
	if VerifyPilotFixtureSQLite(context.Background(), linked, identity, cfg) == nil {
		t.Fatal("symlink path accepted")
	}
	hard := filepath.Join(dir, "hard")
	if e := os.Link(p, hard); e != nil {
		t.Fatal(e)
	}
	if VerifyPilotFixtureSQLite(context.Background(), f, identity, cfg) == nil {
		t.Fatal("multiply linked accepted")
	}
	if e := os.Remove(hard); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(p, p+".old"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte("replacement"), 0600); e != nil {
		t.Fatal(e)
	}
	if VerifyPilotFixtureSQLite(context.Background(), f, identity, cfg) == nil {
		t.Fatal("replaced path accepted")
	}
}
