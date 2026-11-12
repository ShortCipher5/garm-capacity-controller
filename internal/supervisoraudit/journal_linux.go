//go:build linux && cgo

// Package supervisoraudit records caller-claimed local history only. It cannot
// authorize activation, enrollment, isolation, drain, deletion or release.
package supervisoraudit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
)

var (
	ErrHistory   = errors.New("supervisor audit: invalid local history")
	ErrConflict  = errors.New("supervisor audit: historical claim conflict")
	ErrUncertain = errors.New("supervisor audit: commit outcome unknown; reopen history")
)

// Boundary is a permanent disclaimer, not a configurable qualification status.
const Boundary = "caller-claimed local history only; no independent rollback anchor, identity, isolation, service or action authority"

// MaxFacts is a deliberate limit for full integrity scanning on every transaction.
// Exhaustion requires an external design decision, never resetting this history.
const MaxFacts = 4096
const applicationID = 0x4753414a

const schemaSQL = ` CREATE TABLE history (id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=64));
 CREATE TABLE facts (sequence INTEGER PRIMARY KEY, scope_digest TEXT NOT NULL UNIQUE CHECK(length(scope_digest)=64), attempt_digest TEXT NOT NULL UNIQUE CHECK(length(attempt_digest)=64));
 CREATE TABLE audit (sequence INTEGER PRIMARY KEY REFERENCES facts(sequence), scope_digest TEXT NOT NULL, attempt_digest TEXT NOT NULL);
 CREATE TRIGGER facts_no_update BEFORE UPDATE ON facts BEGIN SELECT RAISE(ABORT,'immutable'); END;
 CREATE TRIGGER facts_no_delete BEFORE DELETE ON facts BEGIN SELECT RAISE(ABORT,'immutable'); END;
 CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT,'immutable'); END;
 CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT,'immutable'); END;
 CREATE TRIGGER history_no_update BEFORE UPDATE ON history BEGIN SELECT RAISE(ABORT,'immutable'); END;
 CREATE TRIGGER history_no_delete BEFORE DELETE ON history BEGIN SELECT RAISE(ABORT,'immutable'); END;`

// Fact is immutable historical data, NOT a consumable or replayable launch token.
type Fact struct {
	Sequence      int64
	ScopeDigest   string
	AttemptDigest string
}
type Observation struct {
	Fact     Fact
	Replay   bool
	Boundary string
}

// Snapshot.Recent is incomplete and must never be used to decide absence.
type Snapshot struct {
	HistoryID string
	Total     int64
	Recent    []Fact
	Boundary  string
}

type Journal struct {
	mu       sync.Mutex
	db       *sql.DB
	path     string
	file     os.FileInfo
	parent   os.FileInfo
	history  string
	poisoned bool
	closed   bool
	commit   func(*sql.Conn) error // tests only; never exported or supplied by callers
}

func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validPrivateInfo(i os.FileInfo, dir bool) bool {
	if i == nil || i.Mode()&os.ModeSymlink != 0 || i.IsDir() != dir || i.Mode().Perm()&0077 != 0 || (!dir && (!i.Mode().IsRegular() || i.Mode().Perm() != 0600)) {
		return false
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && (dir || st.Nlink == 1)
}
func private(path string, dir bool) (os.FileInfo, error) {
	i, e := os.Lstat(path)
	if e != nil || !validPrivateInfo(i, dir) {
		return nil, ErrHistory
	}
	return i, nil
}

func parentPath(path string) bool {
	// Refuse symlink components, including ancestors, not merely the final name.
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if p == filepath.Dir(p) {
			return true
		}
	}
}

// newSidecarsAbsent never adopts orphan sidecars, even private regular files.
func newSidecarsAbsent(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, e := os.Lstat(path + suffix); !os.IsNotExist(e) {
			return ErrHistory
		}
	}
	return nil
}
func sidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_, e := os.Lstat(path + suffix)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return ErrHistory
		}
		if _, e = private(path+suffix, false); e != nil {
			return ErrHistory
		}
	}
	return nil
}
func connect(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rw")
	q.Set("_busy_timeout", "5000")
	q.Set("_synchronous", "FULL")
	q.Set("_foreign_keys", "on")
	u.RawQuery = q.Encode()
	d, e := sql.Open("sqlite3", u.String())
	if e != nil {
		return nil, ErrHistory
	}
	d.SetMaxOpenConns(1)
	return d, nil
}

type reader interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}
type connectionReader struct{ c *sql.Conn }

func (r connectionReader) Query(q string, a ...any) (*sql.Rows, error) {
	return r.c.QueryContext(context.Background(), q, a...)
}
func (r connectionReader) QueryRow(q string, a ...any) *sql.Row {
	return r.c.QueryRowContext(context.Background(), q, a...)
}
func scalar(d reader, q string) (int, error) { var v int; e := d.QueryRow(q).Scan(&v); return v, e }

// InitializeNew requires an existing private parent. Failed initialization is
// left in place for investigation; it is never silently removed or reset.
func InitializeNew(path string) (*Journal, error) {
	path, e := filepath.Abs(path)
	if e != nil || !parentPath(path) {
		return nil, ErrHistory
	}
	if _, e = private(filepath.Dir(path), true); e != nil {
		return nil, e
	}
	if e = newSidecarsAbsent(path); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return nil, ErrHistory
	}
	if e = f.Close(); e != nil {
		return nil, ErrHistory
	}
	if e = newSidecarsAbsent(path); e != nil {
		return nil, e
	}
	d, e := connect(path)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	var b [32]byte
	if _, e = rand.Read(b[:]); e != nil {
		return nil, ErrHistory
	}
	id := hex.EncodeToString(b[:])
	_, e = d.Exec(`PRAGMA synchronous=FULL; PRAGMA journal_mode=WAL; BEGIN IMMEDIATE;
 PRAGMA application_id=1196638538; PRAGMA user_version=1;
` + schemaSQL)
	if e != nil {
		return nil, ErrHistory
	}
	if _, e = d.Exec("INSERT INTO history VALUES (?)", id); e != nil {
		return nil, ErrHistory
	}
	if _, e = d.Exec("COMMIT"); e != nil {
		return nil, ErrUncertain
	}
	if _, e = d.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
		return nil, ErrUncertain
	}
	if e = d.Close(); e != nil {
		return nil, ErrUncertain
	}
	// Directory fsync records creation, not an external rollback fence.
	p, e := os.Open(filepath.Dir(path))
	if e != nil {
		return nil, ErrUncertain
	}
	e = p.Sync()
	p.Close()
	if e != nil {
		return nil, ErrUncertain
	}
	return OpenExisting(path, id)
}

// OpenExisting never initializes missing/empty/wrong databases. expectedHistoryID
// is caller memory for accidental wrong-file detection, NOT a rollback anchor.
func OpenExisting(path, expectedHistoryID string) (*Journal, error) {
	if !digest(expectedHistoryID) {
		return nil, ErrHistory
	}
	path, e := filepath.Abs(path)
	if e != nil || !parentPath(path) {
		return nil, ErrHistory
	}
	p, e := private(filepath.Dir(path), true)
	if e != nil {
		return nil, e
	}
	f, e := private(path, false)
	if e != nil {
		return nil, e
	}
	// Raw header rejects unrelated files without letting SQLite create sidecars.
	raw, e := openPinnedSource(path, f)
	if e != nil {
		return nil, ErrHistory
	}
	var h [100]byte
	_, e = io.ReadFull(raw, h[:])
	raw.Close()
	if e != nil || string(h[:16]) != "SQLite format 3\x00" || binary.BigEndian.Uint32(h[68:72]) != applicationID || binary.BigEndian.Uint32(h[60:64]) != 1 {
		return nil, ErrHistory
	}
	// Never declare the changing original immutable. A closed bounded private
	// copy is only a static-metadata accident guard, NOT a backup or fact view.
	probe := &Journal{path: path, file: f, parent: p, history: expectedHistoryID}
	if e = probe.preflightMetadata(); e != nil {
		return nil, ErrHistory
	}
	d, e := connect(path)
	if e != nil {
		return nil, e
	}
	j := &Journal{db: d, path: path, file: f, parent: p, history: expectedHistoryID}
	// Validate read-only BEFORE any mode-setting pragma can mutate another DB.
	if e = j.readValidate(); e != nil {
		d.Close()
		return nil, ErrHistory
	}
	if _, e = d.Exec("PRAGMA synchronous=FULL; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON"); e != nil {
		d.Close()
		return nil, ErrHistory
	}
	if e = j.guard(); e != nil {
		d.Close()
		return nil, e
	}
	return j, nil
}
func (j *Journal) guard() error {
	if j.closed || j.poisoned || !parentPath(j.path) {
		return ErrHistory
	}
	f, e := private(j.path, false)
	if e != nil || !os.SameFile(f, j.file) {
		return ErrHistory
	}
	p, e := private(filepath.Dir(j.path), true)
	if e != nil || !os.SameFile(p, j.parent) {
		return ErrHistory
	}
	return sidecars(j.path)
}
func (j *Journal) readValidate() error {
	c, e := j.db.Conn(context.Background())
	if e != nil {
		return ErrHistory
	}
	defer c.Close()
	if _, e = c.ExecContext(context.Background(), "BEGIN"); e != nil {
		return ErrHistory
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	return j.validate(connectionReader{c})
}

// validateMetadata keeps all externally returned failures at ErrHistory.
func (j *Journal) validateMetadata(d reader) error {
	if e := j.inspectMetadata(d); e != nil {
		return ErrHistory
	}
	return nil
}

// inspectMetadata preserves private SQL-read classification ONLY for disposable
// copy parsing. A semantic mismatch is never classified as transient.
func (j *Journal) inspectMetadata(d reader) error {
	a, e := scalar(d, "PRAGMA application_id")
	if e != nil {
		return &metadataReadFailure{e}
	}
	if a != applicationID {
		return ErrHistory
	}
	v, e := scalar(d, "PRAGMA user_version")
	if e != nil {
		return &metadataReadFailure{e}
	}
	if v != 1 {
		return ErrHistory
	}
	var id string
	var n int
	if e = d.QueryRow("SELECT count(*),min(id) FROM history").Scan(&n, &id); e != nil {
		return &metadataReadFailure{e}
	}
	if n != 1 || id != j.history {
		return ErrHistory
	}
	expected := map[string]bool{}
	for _, statement := range strings.Split(schemaSQL, ";\n") {
		statement = strings.TrimSpace(statement)
		statement = strings.TrimSuffix(statement, ";")
		if statement != "" {
			expected[statement] = true
		}
	}
	sr, e := d.Query("SELECT sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'")
	if e != nil {
		return &metadataReadFailure{e}
	}
	for sr.Next() {
		var statement string
		if e = sr.Scan(&statement); e != nil {
			sr.Close()
			return &metadataReadFailure{e}
		}
		if !expected[statement] {
			sr.Close()
			return ErrHistory
		}
		delete(expected, statement)
	}
	if e = sr.Err(); e != nil {
		sr.Close()
		return &metadataReadFailure{e}
	}
	if e = sr.Close(); e != nil {
		return &metadataReadFailure{e}
	}
	if len(expected) != 0 {
		return ErrHistory
	}
	return nil
}

func (j *Journal) validate(d reader) error {
	if e := j.validateMetadata(d); e != nil {
		return e
	}
	var e error
	var check string
	if e = d.QueryRow("PRAGMA quick_check").Scan(&check); e != nil || check != "ok" {
		return ErrHistory
	}

	rows, e := d.Query("SELECT f.sequence,f.scope_digest,f.attempt_digest,a.scope_digest,a.attempt_digest FROM facts f LEFT JOIN audit a ON a.sequence=f.sequence ORDER BY f.sequence")
	if e != nil {
		return ErrHistory
	}
	defer rows.Close()
	var seq int64
	for rows.Next() {
		var s int64
		var scope, attempt, as, aa string
		if e = rows.Scan(&s, &scope, &attempt, &as, &aa); e != nil || s != seq+1 || s > MaxFacts || !digest(scope) || !digest(attempt) || scope != as || attempt != aa {
			return ErrHistory
		}
		seq = s
	}
	if rows.Err() != nil {
		return ErrHistory
	}
	rows.Close()
	var count int64
	if e = d.QueryRow("SELECT count(*) FROM audit").Scan(&count); e != nil || count != seq {
		return ErrHistory
	}
	return nil
}
func (j *Journal) HistoryID() string { return j.history }
func (j *Journal) Close() error {
	if j == nil {
		return ErrHistory
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
	if j.db == nil {
		return ErrHistory
	}
	if e := j.db.Close(); e != nil {
		return ErrHistory
	}
	return nil
}
func (j *Journal) transaction(fn func(*sql.Conn) error) error {
	if e := j.guard(); e != nil {
		return e
	}
	c, e := j.db.Conn(context.Background())
	if e != nil {
		return ErrHistory
	}
	defer c.Close()
	if _, e = c.ExecContext(context.Background(), "BEGIN IMMEDIATE"); e != nil {
		return ErrHistory
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	if e = j.validate(connectionReader{c}); e != nil {
		return e
	}
	if e = fn(c); e != nil {
		return e
	}
	if e = j.guard(); e != nil {
		return e
	}
	if j.commit != nil {
		e = j.commit(c)
	} else {
		_, e = c.ExecContext(context.Background(), "COMMIT")
	}
	if e != nil {
		j.poisoned = true
		return ErrUncertain
	}
	if e = j.guard(); e != nil {
		j.poisoned = true
		return ErrUncertain
	}
	return nil
}

// RecordClaim appends a caller claim or returns its original history. Neither
// result permits anything. In particular Replay=false is NOT permission to launch.
func (j *Journal) RecordClaim(scope, attempt string) (Observation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	o := Observation{Boundary: Boundary}
	if !digest(scope) || !digest(attempt) {
		return o, ErrHistory
	}
	e := j.transaction(func(c *sql.Conn) error {
		var f Fact
		e := c.QueryRowContext(context.Background(), "SELECT sequence,scope_digest,attempt_digest FROM facts WHERE scope_digest=? OR attempt_digest=?", scope, attempt).Scan(&f.Sequence, &f.ScopeDigest, &f.AttemptDigest)
		if e == nil {
			if f.ScopeDigest != scope || f.AttemptDigest != attempt {
				return ErrConflict
			}
			o.Fact = f
			o.Replay = true
			return nil
		}
		if e != sql.ErrNoRows {
			return ErrHistory
		}
		var count int64
		if e := c.QueryRowContext(context.Background(), "SELECT count(*) FROM facts").Scan(&count); e != nil || count >= MaxFacts {
			return ErrHistory
		}
		r, e := c.ExecContext(context.Background(), "INSERT INTO facts(scope_digest,attempt_digest) VALUES (?,?)", scope, attempt)
		if e != nil {
			return ErrHistory
		}
		s, e := r.LastInsertId()
		if e != nil {
			return ErrHistory
		}
		if _, e = c.ExecContext(context.Background(), "INSERT INTO audit VALUES (?,?,?)", s, scope, attempt); e != nil {
			return ErrHistory
		}
		o.Fact = Fact{s, scope, attempt}
		return nil
	})
	if e != nil {
		return Observation{Boundary: Boundary}, e
	}
	return o, nil
}
func (j *Journal) Snapshot() (Snapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{HistoryID: j.history, Boundary: Boundary}
	e := j.transaction(func(c *sql.Conn) error {
		if e := c.QueryRowContext(context.Background(), "SELECT count(*) FROM facts").Scan(&s.Total); e != nil {
			return ErrHistory
		}
		r, e := c.QueryContext(context.Background(), "SELECT sequence,scope_digest,attempt_digest FROM facts ORDER BY sequence DESC LIMIT 100")
		if e != nil {
			return ErrHistory
		}
		defer r.Close()
		for r.Next() {
			var f Fact
			if r.Scan(&f.Sequence, &f.ScopeDigest, &f.AttemptDigest) != nil {
				return ErrHistory
			}
			s.Recent = append(s.Recent, f)
		}
		if r.Err() != nil {
			return ErrHistory
		}
		return nil
	})
	if e != nil {
		return Snapshot{HistoryID: j.history, Boundary: Boundary}, e
	}
	return s, nil
}
