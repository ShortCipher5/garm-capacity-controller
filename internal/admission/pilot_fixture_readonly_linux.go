//go:build linux && stock_read_pilot_harness

package admission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Exact schema produced by this revision's InitializeSQLite, independent of
// random lineage, timestamps and policy. A generator regression below protects
// this pin. This is fixture compatibility, not runtime or process authority.
const pilotFixtureSchemaSHA256 = "4d9a315718fedbba747231a2602060804eaafac2814a4161da3234ac47ffba09"

// VerifyPilotFixtureSQLite reads a stopped, freshly initialized disposable DB.
// It never opens a Store, migrates, writes, grants, or returns an authority type.
// The caller owns protected ancestor custody and stopping the initializer. The
// sampled file brackets below do not prove exclusive custody or process exit.
func VerifyPilotFixtureSQLite(ctx context.Context, file *os.File, identity []byte, expected Config) error {
	if ctx == nil || ctx.Err() != nil || file == nil || validateConfig(expected) != nil || len(identity) != 65 || identity[64] != '\n' || !bindingFingerprint(string(identity[:64])) {
		return ErrRuntimeLineage
	}
	path := file.Name()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrRuntimeLineage
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_ACCMODE != syscall.O_RDONLY {
		return ErrRuntimeLineage
	}
	before, e := file.Stat()
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 4096 || before.Size() > 128<<20 {
		return ErrRuntimeLineage
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return ErrRuntimeLineage
	}
	bracket := func() error {
		current, e := os.Lstat(path)
		if e != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) || current.Mode() != before.Mode() || current.Size() != before.Size() {
			return ErrRuntimeLineage
		}
		last, ok := current.Sys().(*syscall.Stat_t)
		if !ok || last.Uid != st.Uid || last.Gid != st.Gid || last.Nlink != 1 || last.Ctim != st.Ctim || last.Mtim != st.Mtim {
			return ErrRuntimeLineage
		}
		parent, e := os.Lstat(filepath.Dir(path))
		if e != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 {
			return ErrRuntimeLineage
		}
		ps, ok := parent.Sys().(*syscall.Stat_t)
		if !ok || ps.Uid != st.Uid {
			return ErrRuntimeLineage
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, e := os.Lstat(path + suffix); !errors.Is(e, os.ErrNotExist) {
				return ErrRuntimeLineage
			}
		}
		return ctx.Err()
	}
	if bracket() != nil {
		return ErrRuntimeLineage
	}
	hash := func() ([]byte, error) {
		h := sha256.New()
		n, e := io.Copy(h, io.NewSectionReader(file, 0, before.Size()+1))
		if e != nil || n != before.Size() {
			return nil, ErrRuntimeLineage
		}
		return h.Sum(nil), nil
	}
	original, e := hash()
	if e != nil {
		return ErrRuntimeLineage
	}
	db, e := sql.Open("sqlite3", fmt.Sprintf("file:/proc/self/fd/%d?mode=ro&immutable=1&_query_only=true&_busy_timeout=100", file.Fd()))
	if e != nil {
		return ErrRuntimeLineage
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return ErrRuntimeLineage
	}
	defer tx.Rollback()
	var version, application int
	var journal, integrity string
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || version != sqliteCurrentVersion || tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&application) != nil || application != 0x47434331 || tx.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal) != nil || journal != "delete" || tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
		return ErrRuntimeLineage
	}
	schema, tables, e := pilotFixtureSchema(ctx, tx)
	if e != nil || schema != pilotFixtureSchemaSHA256 {
		return ErrRuntimeLineage
	}
	var config []byte
	var ready, epoch int64
	var nonce, began string
	if tx.QueryRowContext(ctx, "SELECT config,ready,epoch,nonce,began_at FROM metadata WHERE id=1").Scan(&config, &ready, &epoch, &nonce, &began) != nil || ready != 0 || epoch != 0 || nonce != "" || began != "" {
		return ErrRuntimeLineage
	}
	encoded, e := json.Marshal(expected)
	if e != nil || !bytes.Equal(config, encoded) || sqliteRuntimeIdentity(tx.QueryRow, string(identity[:64])) != nil {
		return ErrRuntimeLineage
	}
	for _, name := range tables {
		if name == "audit" || name == "sqlite_sequence" {
			continue
		}
		want := 0
		if name == "metadata" || name == "ledger_lineage" || name == "cancellation_totals" || name == "broker_policy" {
			want = 1
		}
		var count int
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM "`+name+`"`).Scan(&count) != nil || count != want {
			return ErrRuntimeLineage
		}
	}
	var total, revision int
	var policy []byte
	if tx.QueryRowContext(ctx, "SELECT total FROM cancellation_totals WHERE id=1").Scan(&total) != nil || total != 0 || tx.QueryRowContext(ctx, "SELECT revision,document FROM broker_policy WHERE id=1").Scan(&revision, &policy) != nil || revision != 0 || string(policy) != "[]" {
		return ErrRuntimeLineage
	}
	rows, e := tx.QueryContext(ctx, "SELECT sequence,kind,request_key,recorded_at FROM audit ORDER BY sequence")
	if e != nil {
		return ErrRuntimeLineage
	}
	n := 0
	for rows.Next() {
		var seq int
		var kind, key, at string
		if rows.Scan(&seq, &kind, &key, &at) != nil {
			rows.Close()
			return ErrRuntimeLineage
		}
		n++
		if n > sqliteCurrentVersion-1 || seq != n || kind != fmt.Sprintf("schema_migrated_v%d", n+1) || key != "" {
			rows.Close()
			return ErrRuntimeLineage
		}
		if _, e := time.Parse(time.RFC3339Nano, at); e != nil {
			rows.Close()
			return ErrRuntimeLineage
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil || n != sqliteCurrentVersion-1 {
		return ErrRuntimeLineage
	}
	var sequence, count int
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_sequence").Scan(&count) != nil || count != 1 || tx.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name='audit'").Scan(&sequence) != nil || sequence != n {
		return ErrRuntimeLineage
	}
	rows, e = tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if e != nil {
		return ErrRuntimeLineage
	}
	invalid := rows.Next()
	e = rows.Err()
	rows.Close()
	if invalid || e != nil {
		return ErrRuntimeLineage
	}
	if tx.Commit() != nil || bracket() != nil {
		return ErrRuntimeLineage
	}
	final, e := hash()
	if e != nil || !bytes.Equal(original, final) || bracket() != nil {
		return ErrRuntimeLineage
	}
	return nil
}

func pilotFixtureSchema(ctx context.Context, tx *sql.Tx) (string, []string, error) {
	rows, e := tx.QueryContext(ctx, "SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name")
	if e != nil {
		return "", nil, e
	}
	defer rows.Close()
	h := sha256.New()
	var tables []string
	n := 0
	for rows.Next() {
		var typ, name, table string
		var definition sql.NullString
		if rows.Scan(&typ, &name, &table, &definition) != nil {
			return "", nil, ErrRuntimeLineage
		}
		n++
		if n > 1024 || len(definition.String) > 64<<10 || len(name) > 256 || len(table) > 256 {
			return "", nil, ErrRuntimeLineage
		}
		raw, e := json.Marshal([]any{typ, name, table, definition.Valid, definition.String})
		if e != nil {
			return "", nil, e
		}
		h.Write(raw)
		h.Write([]byte{'\n'})
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	if rows.Err() != nil || n == 0 {
		return "", nil, ErrRuntimeLineage
	}
	return hex.EncodeToString(h.Sum(nil)), tables, nil
}
