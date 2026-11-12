// Package garmmanagement journals experimental Garm settings writes. A pending
// intent is a durable stop sign, never permission to replay a remote write.
package garmmanagement

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var (
	ErrJournal         = errors.New("Garm management journal unavailable")
	ErrBlocked         = errors.New("unresolved Garm write blocks further mutations")
	ErrRequestConflict = errors.New("Garm management request ID already used for different input")
	ErrOutcomeUnknown  = errors.New("Garm write outcome uncertain; durable reconciliation required")
)

const applicationID = 0x47434d31
const schema = `
CREATE TABLE journal_identity (
 id INTEGER PRIMARY KEY CHECK(id=1), lineage TEXT NOT NULL CHECK(length(lineage)=64)
) STRICT;
CREATE TRIGGER journal_identity_immutable_update BEFORE UPDATE ON journal_identity
BEGIN SELECT RAISE(ABORT,'journal lineage is immutable'); END;
CREATE TRIGGER journal_identity_immutable_delete BEFORE DELETE ON journal_identity
BEGIN SELECT RAISE(ABORT,'journal lineage is immutable'); END;
CREATE TABLE binding (
 id INTEGER PRIMARY KEY CHECK(id=1), identity TEXT NOT NULL
) STRICT;
CREATE TABLE intents (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 request_id TEXT UNIQUE NOT NULL,
 request_hash TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('pool','repository')),
 target TEXT NOT NULL,
 expected TEXT NOT NULL,
 reason TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role='management'),
 before_json BLOB NOT NULL,
 desired_json BLOB NOT NULL,
 result_json BLOB,
 state TEXT NOT NULL CHECK(state IN ('pending','unknown','confirmed','rejected')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX one_unresolved_write ON intents((1)) WHERE state IN ('pending','unknown');
CREATE TABLE audit (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 request_id TEXT NOT NULL REFERENCES intents(request_id),
 kind TEXT NOT NULL,
 recorded_at TEXT NOT NULL,
 observation_json BLOB
) STRICT;
PRAGMA application_id=1195593009;
PRAGMA user_version=2;
`

type Journal struct {
	db      *sql.DB
	lineage string
}

// InitializeJournal creates a NEW owner-private journal exactly once. Never use
// initialization to fix a missing/wrong mount or escape an unresolved operation.
// Save Lineage() in independent trusted service configuration before first use.
// Existing paths, including empty files and legacy journals, are never adopted.
func InitializeJournal(path string) (*Journal, error) {
	absolute, err := journalPath(path)
	if err != nil {
		return nil, err
	}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return nil, ErrJournal
	}
	lineage := hex.EncodeToString(entropy[:])
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(absolute + suffix); !errors.Is(err, os.ErrNotExist) {
			return nil, ErrJournal
		}
	}
	fd, err := syscall.Open(absolute, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrJournal
	}
	if syscall.Close(fd) != nil {
		return nil, ErrJournal
	}
	journal, err := openJournalFile(absolute, lineage, true)
	if err != nil {
		return nil, err
	}
	directory, err := os.Open(filepath.Dir(absolute))
	if err != nil {
		journal.Close()
		return nil, ErrJournal
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		journal.Close()
		return nil, ErrJournal
	}
	return journal, nil
}

// OpenJournal opens ONLY an existing owner-private schema2 journal matching the
// independently configured lineage. Missing/empty/replaced/schema1/archive files
// fail before changing WAL mode. This is not an anti-rollback proof for an owner
// manually substituting a historical raw copy with the same lineage.
func OpenJournal(path, expectedLineage string) (*Journal, error) {
	if !validJournalLineage(expectedLineage) {
		return nil, ErrJournal
	}
	absolute, err := journalPath(path)
	if err != nil {
		return nil, err
	}
	return openJournalFile(absolute, expectedLineage, false)
}
func (j *Journal) Lineage() string { return j.lineage }
func validJournalLineage(value string) bool {
	if len(value) != 64 {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(raw) == value
}
func journalPath(path string) (string, error) {
	if path == "" {
		return "", ErrJournal
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", ErrJournal
	}
	parent, err := os.Lstat(filepath.Dir(absolute))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return "", ErrJournal
	}
	stat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return "", ErrJournal
	}
	return absolute, nil
}
func openJournalFile(absolute, expected string, initialize bool) (*Journal, error) {
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrJournal
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrJournal
	}
	location := url.URL{Scheme: "file", Path: absolute}
	location.RawQuery = "mode=ro"
	check, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, ErrJournal
	}
	var app, version, tables int
	err = check.QueryRow("PRAGMA application_id").Scan(&app)
	if err == nil {
		err = check.QueryRow("PRAGMA user_version").Scan(&version)
	}
	if err == nil && initialize {
		err = check.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&tables)
	}
	var observed string
	if err == nil && !initialize {
		err = check.QueryRow("SELECT lineage FROM journal_identity WHERE id=1").Scan(&observed)
	}
	closeErr := check.Close()
	if err != nil || closeErr != nil {
		return nil, ErrJournal
	}
	if initialize {
		if app != 0 || version != 0 || tables != 0 {
			return nil, ErrJournal
		}
	} else if app != applicationID || version != 2 || observed != expected {
		return nil, ErrJournal
	}
	query := url.Values{"mode": {"rw"}, "_busy_timeout": {"5000"}, "_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_foreign_keys": {"on"}, "_txlock": {"immediate"}}
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, ErrJournal
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	j := &Journal{db: db, lineage: expected}
	err = j.transaction(context.Background(), func(tx *sql.Tx) error {
		var mode string
		var synchronous, foreignKeys int
		for query, target := range map[string]any{"PRAGMA application_id": &app, "PRAGMA user_version": &version, "PRAGMA journal_mode": &mode, "PRAGMA synchronous": &synchronous, "PRAGMA foreign_keys": &foreignKeys} {
			if tx.QueryRow(query).Scan(target) != nil {
				return ErrJournal
			}
		}
		if mode != "wal" || synchronous != 2 || foreignKeys != 1 {
			return ErrJournal
		}
		if initialize {
			if app != 0 || version != 0 {
				return ErrJournal
			}
			if tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&tables) != nil || tables != 0 {
				return ErrJournal
			}
			if _, err := tx.Exec(schema); err != nil {
				return ErrJournal
			}
			_, err := tx.Exec("INSERT INTO journal_identity(id,lineage) VALUES(1,?)", expected)
			return err
		}
		if app != applicationID || version != 2 || tx.QueryRow("SELECT lineage FROM journal_identity WHERE id=1").Scan(&observed) != nil || observed != expected {
			return ErrJournal
		}
		_, err := tx.Exec("SELECT request_id,request_hash,kind,target,expected,reason,role,before_json,desired_json,result_json,state,created_at,updated_at FROM intents LIMIT 0")
		return err
	})
	if err != nil {
		db.Close()
		return nil, ErrJournal
	}
	return j, nil
}
func (j *Journal) Close() error { return j.db.Close() }
func (j *Journal) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrJournal
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return ErrJournal
	}
	return nil
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func audit(tx *sql.Tx, id, kind string, observation []byte) error {
	_, err := tx.Exec("INSERT INTO audit(request_id,kind,recorded_at,observation_json) VALUES(?,?,?,?)", id, kind, timestamp(), observation)
	return err
}

// Intent contains only sanitized settings and operator metadata, never tokens,
// raw upstream payloads, provider specs, credentials or instance/job data.
type Intent struct {
	Sequence  uint64          `json:"sequence"`
	RequestID string          `json:"request_id"`
	Kind      string          `json:"kind"`
	Target    string          `json:"target"`
	State     string          `json:"state"`
	Reason    string          `json:"reason"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	Before    json.RawMessage `json:"before"`
	Desired   json.RawMessage `json:"desired"`
	Result    json.RawMessage `json:"result,omitempty"`
}

type AuditEvent struct {
	Sequence    uint64          `json:"sequence"`
	RequestID   string          `json:"request_id"`
	Kind        string          `json:"kind"`
	RecordedAt  string          `json:"recorded_at"`
	Observation json.RawMessage `json:"observation,omitempty"`
}

func (j *Journal) begin(ctx context.Context, intent Intent, hash, expected string) (json.RawMessage, error) {
	var existing json.RawMessage
	err := j.transaction(ctx, func(tx *sql.Tx) error {
		var oldHash, state string
		var result []byte
		err := tx.QueryRow("SELECT request_hash,state,result_json FROM intents WHERE request_id=?", intent.RequestID).Scan(&oldHash, &state, &result)
		if err == nil {
			if oldHash != hash {
				return ErrRequestConflict
			}
			if state == "confirmed" {
				if !json.Valid(result) {
					return ErrJournal
				}
				existing = result
				return nil
			}
			if state == "rejected" {
				return ErrRequestConflict
			}
			return ErrBlocked
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ErrJournal
		}
		var pending int
		if tx.QueryRow("SELECT count(*) FROM intents WHERE state IN ('pending','unknown')").Scan(&pending) != nil {
			return ErrJournal
		}
		if pending != 0 {
			return ErrBlocked
		}
		now := timestamp()
		_, err = tx.Exec("INSERT INTO intents(request_id,request_hash,kind,target,expected,reason,role,before_json,desired_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'management',?,?,'pending',?,?)", intent.RequestID, hash, intent.Kind, intent.Target, expected, intent.Reason, []byte(intent.Before), []byte(intent.Desired), now, now)
		if err != nil {
			return ErrJournal
		}
		if audit(tx, intent.RequestID, "intent_committed", nil) != nil {
			return ErrJournal
		}
		return nil
	})
	return existing, err
}
func (j *Journal) finish(ctx context.Context, id, state string, result []byte) error {
	if state != "confirmed" && state != "rejected" && state != "unknown" {
		return ErrJournal
	}
	return j.transaction(ctx, func(tx *sql.Tx) error {
		update, err := tx.Exec("UPDATE intents SET state=?,result_json=?,updated_at=? WHERE request_id=? AND state='pending'", state, result, timestamp(), id)
		if err != nil {
			return ErrJournal
		}
		n, err := update.RowsAffected()
		if err != nil || n != 1 {
			return ErrJournal
		}
		if audit(tx, id, state, result) != nil {
			return ErrJournal
		}
		return nil
	})
}
func scanIntent(row interface{ Scan(...any) error }) (Intent, error) {
	var intent Intent
	var before, desired, result []byte
	err := row.Scan(&intent.Sequence, &intent.RequestID, &intent.Kind, &intent.Target, &intent.State, &intent.Reason, &intent.CreatedAt, &intent.UpdatedAt, &before, &desired, &result)
	intent.Before = before
	intent.Desired = desired
	intent.Result = result
	return intent, err
}

const intentColumns = "sequence,request_id,kind,target,state,reason,created_at,updated_at,before_json,desired_json,result_json"

func (j *Journal) Pending(ctx context.Context) ([]Intent, error) {
	rows, err := j.db.QueryContext(ctx, "SELECT "+intentColumns+" FROM intents WHERE state IN ('pending','unknown') ORDER BY sequence")
	if err != nil {
		return nil, ErrJournal
	}
	defer rows.Close()
	intents := []Intent{}
	for rows.Next() {
		intent, e := scanIntent(rows)
		if e != nil {
			return nil, ErrJournal
		}
		intents = append(intents, intent)
	}
	if rows.Err() != nil || len(intents) > 1 {
		return nil, ErrJournal
	}
	return intents, nil
}
func (j *Journal) History(ctx context.Context, after uint64, limit int) ([]Intent, error) {
	if limit < 1 || limit > 1000 || after > 1<<63-1 {
		return nil, ErrJournal
	}
	rows, err := j.db.QueryContext(ctx, "SELECT "+intentColumns+" FROM intents WHERE sequence>? ORDER BY sequence LIMIT ?", after, limit)
	if err != nil {
		return nil, ErrJournal
	}
	defer rows.Close()
	result := []Intent{}
	for rows.Next() {
		intent, e := scanIntent(rows)
		if e != nil {
			return nil, ErrJournal
		}
		result = append(result, intent)
	}
	if rows.Err() != nil {
		return nil, ErrJournal
	}
	return result, nil
}
func (j *Journal) Audit(ctx context.Context, after uint64, limit int) ([]AuditEvent, error) {
	if limit < 1 || limit > 1000 || after > 1<<63-1 {
		return nil, ErrJournal
	}
	rows, err := j.db.QueryContext(ctx, "SELECT sequence,request_id,kind,recorded_at,observation_json FROM audit WHERE sequence>? ORDER BY sequence LIMIT ?", after, limit)
	if err != nil {
		return nil, ErrJournal
	}
	defer rows.Close()
	result := []AuditEvent{}
	for rows.Next() {
		event := AuditEvent{}
		var observation []byte
		if rows.Scan(&event.Sequence, &event.RequestID, &event.Kind, &event.RecordedAt, &observation) != nil {
			return nil, ErrJournal
		}
		event.Observation = observation
		result = append(result, event)
	}
	if rows.Err() != nil {
		return nil, ErrJournal
	}
	return result, nil
}
func (j *Journal) observe(ctx context.Context, id, kind string, observation []byte) error {
	if kind != "observed_desired" && kind != "observed_other" {
		return ErrJournal
	}
	return j.transaction(ctx, func(tx *sql.Tx) error {
		var state string
		if tx.QueryRow("SELECT state FROM intents WHERE request_id=?", id).Scan(&state) != nil || (state != "pending" && state != "unknown") {
			return ErrJournal
		}
		if audit(tx, id, kind, observation) != nil {
			return ErrJournal
		}
		return nil
	})
}

func (j *Journal) replay(ctx context.Context, id, hash string) (json.RawMessage, bool, error) {
	var oldHash, state string
	var result []byte
	err := j.db.QueryRowContext(ctx, "SELECT request_hash,state,result_json FROM intents WHERE request_id=?", id).Scan(&oldHash, &state, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, ErrJournal
	}
	if oldHash != hash {
		return nil, true, ErrRequestConflict
	}
	if state == "confirmed" {
		if !json.Valid(result) {
			return nil, true, ErrJournal
		}
		return result, true, nil
	}
	if state == "rejected" {
		return nil, true, ErrRequestConflict
	}
	return nil, true, ErrBlocked
}

func (j *Journal) bind(ctx context.Context, identity string) error {
	if len(identity) != 64 {
		return ErrJournal
	}
	return j.transaction(ctx, func(tx *sql.Tx) error {
		var old string
		err := tx.QueryRow("SELECT identity FROM binding WHERE id=1").Scan(&old)
		if err == nil {
			if old != identity {
				return ErrJournal
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ErrJournal
		}
		var count int
		if tx.QueryRow("SELECT count(*) FROM intents").Scan(&count) != nil || count != 0 {
			return ErrJournal
		}
		_, err = tx.Exec("INSERT INTO binding(id,identity) VALUES(1,?)", identity)
		if err != nil {
			return ErrJournal
		}
		return nil
	})
}
