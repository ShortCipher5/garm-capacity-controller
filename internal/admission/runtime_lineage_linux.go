package admission

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// ErrRuntimeLineage never invites adoption or regeneration of missing state.
var ErrRuntimeLineage = errors.New("existing initialized runtime ledger and independent matching identity required")

const sqliteRuntimeSchema = `
CREATE TABLE ledger_lineage (
 id INTEGER PRIMARY KEY CHECK(id=1),
 lineage_id TEXT NOT NULL CHECK(length(lineage_id)=64),
 runtime_kind TEXT NOT NULL CHECK(runtime_kind IN ('active','offline','archive','restored'))
) STRICT;
CREATE TRIGGER ledger_lineage_identity_immutable BEFORE UPDATE OF id,lineage_id ON ledger_lineage
BEGIN SELECT RAISE(ABORT,'ledger lineage is immutable'); END;
CREATE TRIGGER ledger_lineage_no_delete BEFORE DELETE ON ledger_lineage
BEGIN SELECT RAISE(ABORT,'ledger lineage is immutable'); END;
CREATE TRIGGER ledger_lineage_no_activation BEFORE UPDATE OF runtime_kind ON ledger_lineage
WHEN NEW.runtime_kind IN ('active','offline') AND NEW.runtime_kind IS NOT OLD.runtime_kind
BEGIN SELECT RAISE(ABORT,'runtime activation is unavailable'); END;
PRAGMA user_version=6;
`

func sqliteNewLineage() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// InitializeSQLite is NEW-only and offline. It refuses any existing destination,
// identity file, or SQLite sidecar. The independent owner-only identity must be
// protected separately from the database volume. No legacy adoption is provided.
// On failure preserve partial outputs and inspect offline; never retry over them.
func InitializeSQLite(path, identityPath string, initial Config) error {
	if err := validateConfig(initial); err != nil {
		return err
	}
	absolute, err := sqliteRuntimePrivatePath(path, false)
	if err != nil {
		return err
	}
	identity, err := sqliteRuntimePrivatePath(identityPath, false)
	if err != nil {
		return err
	}
	if absolute == identity || strings.HasPrefix(identity, absolute+"-") || strings.HasPrefix(absolute, identity+"-") {
		return ErrRuntimeLineage
	}
	// Reserve both names without overwriting. A partial failure stays fail-closed.
	anchor, err := os.OpenFile(identity, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer anchor.Close()
	file, err := os.OpenFile(absolute, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	id, err := sqliteNewLineage()
	if err != nil {
		return err
	}
	store, err := openSQLite(absolute, initial, id, "")
	if err != nil {
		return err
	}
	defer store.Close()
	// Persist immutable identity in the main database, enabling no-side-effect
	// rejection preflight without replaying or creating WAL/SHM sidecars.
	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return ErrRuntimeLineage
	}
	if err := store.Close(); err != nil {
		return err
	}
	if _, err := anchor.WriteString(id + "\n"); err != nil {
		return err
	}
	if err := anchor.Sync(); err != nil {
		return err
	}
	for _, parent := range []string{filepath.Dir(absolute), filepath.Dir(identity)} {
		directory, err := os.Open(parent)
		if err != nil {
			return err
		}
		err = directory.Sync()
		closeErr := directory.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// OpenRuntimeSQLite never creates, migrates legacy state, adopts, or rebinds.
// Its identity is read from a private file OUTSIDE the database, not discovered
// from whatever database happens to be mounted. Both read-only service mode and
// provisioning must use this opener. UUID lineage does not detect rollback of a
// raw historical active copy with the same ID; external fencing is still needed.
func OpenRuntimeSQLite(path, identityPath string) (*SQLiteStore, error) {
	if _, err := sqliteRuntimePrivatePath(identityPath, true); err != nil {
		return nil, ErrRuntimeLineage
	}
	identity, err := securefiles.Read(identityPath, 65)
	if err != nil {
		return nil, ErrRuntimeLineage
	}
	id := strings.TrimSuffix(string(identity), "\n")
	if !bindingFingerprint(id) {
		return nil, ErrRuntimeLineage
	}
	absolute, err := sqliteRuntimePrivatePath(path, true)
	if err != nil {
		return nil, ErrRuntimeLineage
	}
	if err := sqliteRuntimePreflight(absolute, id); err != nil {
		return nil, err
	}
	return openSQLite(absolute, Config{}, "", id)
}

func sqliteRuntimePreflight(path, expected string) error {
	// Immutable mode is ONLY used to check checkpointed immutable lineage and
	// ownership, never allocation/readiness/inventory. Unlike ordinary mode=ro it
	// cannot create WAL/SHM while rejecting a wrong mount. The normal connection
	// subsequently checks current (including WAL) lineage in its transaction.
	location := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1&_query_only=true"}
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return ErrRuntimeLineage
	}
	defer db.Close()
	var version, application int
	if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || db.QueryRow("PRAGMA application_id").Scan(&application) != nil || version != sqliteCurrentVersion || application != 0x47434331 {
		return ErrRuntimeLineage
	}
	return sqliteRuntimeIdentity(db.QueryRow, expected)
}

func sqliteRuntimeIdentity(query func(string, ...any) *sql.Row, expected string) error {
	var id, kind string
	if query("SELECT lineage_id,runtime_kind FROM ledger_lineage WHERE id=1").Scan(&id, &kind) != nil || id != expected || kind != "active" {
		return ErrRuntimeLineage
	}
	return nil
}

func (s *SQLiteStore) runtimePathCheck() error {
	if s.runtimeID == "" {
		return nil
	}
	current, err := os.Lstat(s.path)
	if err != nil || !current.Mode().IsRegular() || current.Mode().Perm()&0077 != 0 || !os.SameFile(current, s.runtimeFile) {
		return ErrRuntimeLineage
	}
	return nil
}

// Ancestors remain trusted, as for gateway credential loading. Direct parent and
// files must be owned by the effective user, not merely chmod 0600 by another uid.
func sqliteRuntimePrivatePath(path string, exists bool) (string, error) {
	absolute, err := sqlitePrivatePath(path, exists)
	if err != nil {
		return "", err
	}
	paths := []string{filepath.Dir(absolute)}
	if exists {
		paths = append(paths, absolute)
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return "", ErrRuntimeLineage
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return "", ErrRuntimeLineage
		}
	}
	return absolute, nil
}
