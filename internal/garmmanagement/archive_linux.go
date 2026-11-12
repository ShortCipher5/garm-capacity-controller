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
	"time"

	"github.com/mattn/go-sqlite3"
)

var ErrArchive = errors.New("Garm journal archive unavailable")
var ErrArchiveUncertain = errors.New("Garm journal archive publication uncertain; preserve source and inspect destination")

// Distinct from the active journal application ID. OpenJournal refuses every
// archive/inspection restore BEFORE enabling WAL or returning a usable manager.
// This is a safety marker, not protection against an owner manually editing SQL.
const archiveApplicationID = 0x47434d42
const archiveFormat = "garm-management-archive-v1"
const archiveProvenanceSchema = `
CREATE TABLE archive_provenance (
 sequence INTEGER PRIMARY KEY CHECK(sequence>=1),
 archive_id TEXT UNIQUE NOT NULL CHECK(length(archive_id)=64),
 parent_id TEXT NOT NULL CHECK(length(parent_id) IN (0,64)),
 operation TEXT NOT NULL CHECK(operation IN ('backup','restore_inspection')),
 created_at TEXT NOT NULL,
 format_version INTEGER NOT NULL CHECK(format_version=1)
) STRICT;`

// ArchiveInfo is bounded metadata, not a completion fence or recovery permit.
// A historical journal can omit later remote commits even when Pending is zero.
type ArchiveInfo struct {
	JournalLineage     string    `json:"journal_lineage"`
	Format             string    `json:"format"`
	ArchiveID          string    `json:"archive_id"`
	ParentArchiveID    string    `json:"parent_archive_id,omitempty"`
	Operation          string    `json:"operation"`
	CreatedAt          time.Time `json:"created_at"`
	ManagementIdentity string    `json:"management_identity,omitempty"`
	Intents            uint64    `json:"intents"`
	AuditEvents        uint64    `json:"audit_events"`
	Pending            uint64    `json:"pending"`
	WritesPermitted    bool      `json:"writes_permitted"`
}

func archivePrivatePath(path string, exists bool) (string, error) {
	if path == "" {
		return "", ErrArchive
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", ErrArchive
	}
	parent, err := os.Lstat(filepath.Dir(absolute))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return "", ErrArchive
	}
	if !archiveOwned(parent) {
		return "", ErrArchive
	}
	info, err := os.Lstat(absolute)
	if exists {
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !archiveOwned(info) {
			return "", ErrArchive
		}
	} else {
		if err == nil {
			return "", os.ErrExist
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", ErrArchive
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(absolute + suffix); !errors.Is(err, os.ErrNotExist) {
				return "", ErrArchive
			}
		}
	}
	return absolute, nil
}
func archiveOpenReadOnly(path string) (*sql.DB, error) {
	location := url.URL{Scheme: "file", Path: path}
	location.RawQuery = "mode=ro&_busy_timeout=100&_foreign_keys=on&_query_only=on"
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, ErrArchive
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
func archiveHeader(ctx context.Context, db *sql.DB, expected int) error {
	var app, version int
	if db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app) != nil || db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || app != expected || version != 2 {
		return ErrArchive
	}
	return nil
}

// Backup creates a NEW standalone, private, non-activatable SQLite snapshot,
// including committed WAL contents. It never changes source intent/audit state.
func (j *Journal) Backup(ctx context.Context, destination string) (ArchiveInfo, error) {
	return archiveExport(ctx, j.db, destination, "backup", j.Lineage())
}

// BackupFile uses a read-only existing-file handle. It never creates/migrates or
// opens an absent source as a fresh journal. Cooperating source writers may run;
// the SQLite backup API supplies snapshot consistency, not remote writer fencing.
func BackupFile(ctx context.Context, source, destination, expectedLineage string) (ArchiveInfo, error) {
	path, err := archivePrivatePath(source, true)
	if err != nil {
		return ArchiveInfo{}, err
	}
	db, err := archiveOpenReadOnly(path)
	if err != nil {
		return ArchiveInfo{}, err
	}
	defer db.Close()
	return archiveExport(ctx, db, destination, "backup", expectedLineage)
}

// RestoreInspection creates a NEW quarantined copy of a validated archive. It
// deliberately cannot be supplied to OpenJournal or resume mutations. There is
// no live/overwrite/force/activate option: historical write history is not proof
// that a later accepted remote operation did not occur.
func RestoreInspection(ctx context.Context, source, destination, expectedLineage string) (ArchiveInfo, error) {
	path, err := archivePrivatePath(source, true)
	if err != nil {
		return ArchiveInfo{}, err
	}
	if err = archiveStandalone(path); err != nil {
		return ArchiveInfo{}, err
	}
	db, err := archiveOpenReadOnly(path)
	if err != nil {
		return ArchiveInfo{}, err
	}
	defer db.Close()
	return archiveExport(ctx, db, destination, "restore_inspection", expectedLineage)
}
func archiveNoSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			return ErrArchive
		}
	}
	return nil
}

// InspectArchive is physically read-only and never returns raw settings, reasons,
// endpoint addresses, credentials or job data. It performs no remote operation.
func InspectArchive(ctx context.Context, path string) (ArchiveInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	source, err := archivePrivatePath(path, true)
	if err != nil {
		return ArchiveInfo{}, err
	}
	if archiveStandalone(source) != nil {
		return ArchiveInfo{}, ErrArchive
	}
	db, err := archiveOpenReadOnly(source)
	if err != nil {
		return ArchiveInfo{}, err
	}
	defer db.Close()
	return validateArchive(ctx, db, true)
}

func archiveExport(ctx context.Context, source *sql.DB, destination, operation, expectedLineage string) (ArchiveInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ArchiveInfo{}, err
	}
	if !validJournalLineage(expectedLineage) {
		return ArchiveInfo{}, ErrArchive
	}
	expected := applicationID
	if operation == "restore_inspection" {
		expected = archiveApplicationID
	} else if operation != "backup" {
		return ArchiveInfo{}, ErrArchive
	}
	if archiveHeader(ctx, source, expected) != nil {
		return ArchiveInfo{}, ErrArchive
	}
	var sourceLineage string
	if source.QueryRowContext(ctx, "SELECT lineage FROM journal_identity WHERE id=1").Scan(&sourceLineage) != nil || sourceLineage != expectedLineage {
		return ArchiveInfo{}, ErrArchive
	}
	if operation == "restore_inspection" {
		if _, err := validateArchive(ctx, source, true); err != nil {
			return ArchiveInfo{}, err
		}
	}
	path, err := archivePrivatePath(destination, false)
	if err != nil {
		return ArchiveInfo{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".garm-journal-archive-*.db")
	if err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	temp := temporary.Name()
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(temp + suffix)
		}
	}()
	if temporary.Close() != nil {
		return ArchiveInfo{}, ErrArchive
	}
	location := url.URL{Scheme: "file", Path: temp, RawQuery: "mode=rw&_busy_timeout=100&_synchronous=FULL&_foreign_keys=on"}
	target, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	target.SetMaxOpenConns(1)
	defer target.Close()
	if err = archiveOnlineCopy(ctx, source, target); err != nil {
		return ArchiveInfo{}, err
	}
	if archiveHeader(ctx, target, expected) != nil {
		return ArchiveInfo{}, ErrArchive
	}
	verified, err := validateArchive(ctx, target, operation == "restore_inspection")
	if err != nil {
		return ArchiveInfo{}, err
	}
	if verified.JournalLineage != expectedLineage {
		return ArchiveInfo{}, ErrArchive
	}
	parent := ""
	sequence := uint64(1)
	if operation == "restore_inspection" {
		var previous ArchiveInfo
		previous, err = archiveProvenance(ctx, target)
		if err != nil {
			return ArchiveInfo{}, err
		}
		parent = previous.ArchiveID
		if target.QueryRowContext(ctx, "SELECT count(*) FROM archive_provenance").Scan(&sequence) != nil || sequence >= 1024 {
			return ArchiveInfo{}, ErrArchive
		}
		sequence++
	}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	if operation == "backup" {
		if _, err = tx.ExecContext(ctx, archiveProvenanceSchema); err != nil {
			_ = tx.Rollback()
			return ArchiveInfo{}, ErrArchive
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO archive_provenance(sequence,archive_id,parent_id,operation,created_at,format_version) VALUES(?,?,?,?,?,1)", sequence, hex.EncodeToString(entropy[:]), parent, operation, timestamp())
	if err == nil {
		_, err = tx.ExecContext(ctx, "PRAGMA application_id=1195593026")
	}
	if err != nil {
		_ = tx.Rollback()
		return ArchiveInfo{}, ErrArchive
	}
	if tx.Commit() != nil {
		return ArchiveInfo{}, ErrArchive
	}
	var journalMode string
	if target.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&journalMode) != nil || journalMode != "delete" {
		return ArchiveInfo{}, ErrArchive
	}
	info, err := validateArchive(ctx, target, true)
	if err != nil {
		return ArchiveInfo{}, err
	}
	if target.Close() != nil {
		return ArchiveInfo{}, ErrArchive
	}
	if err = ctx.Err(); err != nil {
		return ArchiveInfo{}, err
	}
	file, err := os.OpenFile(temp, os.O_RDWR, 0)
	if err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return ArchiveInfo{}, ErrArchive
	}
	if archiveNoSidecars(temp) != nil {
		return ArchiveInfo{}, ErrArchive
	}
	if err = ctx.Err(); err != nil {
		return ArchiveInfo{}, err
	}
	if err = archivePublish(temp, path, archiveSyncDirectory); err != nil {
		return ArchiveInfo{}, err
	}
	return info, nil
}

func archivePublish(temp, path string, syncDirectory func(string) error) error {
	if _, err := archivePrivatePath(path, false); err != nil {
		return err
	}
	if err := os.Link(temp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return ErrArchive
	}
	if syncDirectory(filepath.Dir(path)) != nil {
		return ErrArchiveUncertain
	}
	if os.Remove(temp) != nil {
		return ErrArchiveUncertain
	}
	if syncDirectory(filepath.Dir(path)) != nil {
		return ErrArchiveUncertain
	}
	return nil
}
func archiveSyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func archiveOnlineCopy(ctx context.Context, source, target *sql.DB) error {
	src, err := source.Conn(ctx)
	if err != nil {
		return ErrArchive
	}
	defer src.Close()
	dst, err := target.Conn(ctx)
	if err != nil {
		return ErrArchive
	}
	defer dst.Close()
	return src.Raw(func(sourceDriver any) error {
		sourceConnection, ok := sourceDriver.(*sqlite3.SQLiteConn)
		if !ok {
			return ErrArchive
		}
		return dst.Raw(func(targetDriver any) (result error) {
			targetConnection, ok := targetDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return ErrArchive
			}
			backup, err := targetConnection.Backup("main", sourceConnection, "main")
			if err != nil {
				return ErrArchive
			}
			defer func() {
				if err := backup.Finish(); result == nil && err != nil {
					result = ErrArchive
				}
			}()
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				done, err := backup.Step(128)
				if err != nil {
					return ErrArchive
				}
				if done {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
		})
	})
}

func validArchiveHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	data, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(data) == value
}
func archiveProvenance(ctx context.Context, db *sql.DB) (ArchiveInfo, error) {
	rows, err := db.QueryContext(ctx, "SELECT sequence,archive_id,parent_id,operation,created_at,format_version FROM archive_provenance ORDER BY sequence LIMIT 1025")
	if err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	defer rows.Close()
	result := ArchiveInfo{Format: archiveFormat}
	var count uint64
	for rows.Next() {
		var sequence uint64
		var id, parent, operation, at string
		var version int
		if rows.Scan(&sequence, &id, &parent, &operation, &at, &version) != nil {
			return ArchiveInfo{}, ErrArchive
		}
		count++
		created, err := time.Parse(time.RFC3339Nano, at)
		if count > 1024 || sequence != count || !validArchiveHash(id) || version != 1 || err != nil || created.IsZero() || (count == 1 && (parent != "" || operation != "backup")) || (count > 1 && (parent != result.ArchiveID || operation != "restore_inspection")) {
			return ArchiveInfo{}, ErrArchive
		}
		result.ArchiveID = id
		result.ParentArchiveID = parent
		result.Operation = operation
		result.CreatedAt = created
	}
	if rows.Err() != nil || count == 0 {
		return ArchiveInfo{}, ErrArchive
	}
	return result, nil
}
func validateArchive(ctx context.Context, db *sql.DB, quarantined bool) (ArchiveInfo, error) {
	expected := applicationID
	if quarantined {
		expected = archiveApplicationID
	}
	if archiveHeader(ctx, db, expected) != nil {
		return ArchiveInfo{}, ErrArchive
	}
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	count := 0
	for rows.Next() {
		var status string
		if rows.Scan(&status) != nil || status != "ok" {
			rows.Close()
			return ArchiveInfo{}, ErrArchive
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || count != 1 {
		return ArchiveInfo{}, ErrArchive
	}
	rows, err = db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	invalid := rows.Next()
	err = rows.Err()
	rows.Close()
	if invalid || err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	result := ArchiveInfo{Format: archiveFormat}
	if quarantined {
		result, err = archiveProvenance(ctx, db)
		if err != nil {
			return ArchiveInfo{}, err
		}
	} else {
		var exists int
		if db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='archive_provenance'").Scan(&exists) != nil || exists != 0 {
			return ArchiveInfo{}, ErrArchive
		}
	}
	var identities int
	if db.QueryRowContext(ctx, "SELECT count(*) FROM journal_identity").Scan(&identities) != nil || identities != 1 || db.QueryRowContext(ctx, "SELECT lineage FROM journal_identity WHERE id=1").Scan(&result.JournalLineage) != nil || !validJournalLineage(result.JournalLineage) {
		return ArchiveInfo{}, ErrArchive
	}
	if db.QueryRowContext(ctx, "SELECT count(*) FROM intents").Scan(&result.Intents) != nil || db.QueryRowContext(ctx, "SELECT count(*) FROM audit").Scan(&result.AuditEvents) != nil || db.QueryRowContext(ctx, "SELECT count(*) FROM intents WHERE state IN ('pending','unknown')").Scan(&result.Pending) != nil || result.Pending > 1 {
		return ArchiveInfo{}, ErrArchive
	}
	var bindings int
	if db.QueryRowContext(ctx, "SELECT count(*) FROM binding").Scan(&bindings) != nil || bindings > 1 || (result.Intents > 0 && bindings != 1) {
		return ArchiveInfo{}, ErrArchive
	}
	if bindings == 1 {
		if db.QueryRowContext(ctx, "SELECT identity FROM binding WHERE id=1").Scan(&result.ManagementIdentity) != nil || !validArchiveHash(result.ManagementIdentity) {
			return ArchiveInfo{}, ErrArchive
		}
	}
	rows, err = db.QueryContext(ctx, "SELECT kind,before_json,desired_json,result_json,state FROM intents")
	if err != nil {
		return ArchiveInfo{}, ErrArchive
	}
	defer rows.Close()
	for rows.Next() {
		var kind, state string
		var before, desired, recorded []byte
		if rows.Scan(&kind, &before, &desired, &recorded, &state) != nil || !json.Valid(before) || !json.Valid(desired) || (state == "confirmed" && !json.Valid(recorded)) || (kind != "pool" && kind != "repository") {
			return ArchiveInfo{}, ErrArchive
		}
	}
	if rows.Err() != nil {
		return ArchiveInfo{}, ErrArchive
	}
	return result, nil
}

func archiveStandalone(path string) error {
	if archiveNoSidecars(path) != nil {
		return ErrArchive
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrArchive
	}
	defer file.Close()
	var header [24]byte
	if _, err = file.ReadAt(header[:], 0); err != nil || string(header[:16]) != "SQLite format 3\x00" || header[18] != 1 || header[19] != 1 {
		return ErrArchive
	}
	return nil
}
