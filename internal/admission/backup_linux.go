package admission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/mattn/go-sqlite3"
)

// BackupSQLite exports a consistent SQLite online-backup snapshot, including
// committed WAL contents, to a NEW private path. It does not change source
// readiness. The artifact itself is admission-blocked and has no usable fence.
// This is an offline recovery primitive, not a live gateway recovery workflow.
func (s *SQLiteStore) BackupSQLite(ctx context.Context, destination string) error {
	if s.poisoned.Load() {
		return ErrUncertain
	}
	return sqliteExport(ctx, s.db, destination, "backup_created", func() error {
		if s.poisoned.Load() {
			return ErrUncertain
		}
		return nil
	})
}

// BackupSQLiteFile exports an existing private ledger through a read-only handle.
// It never creates or migrates the source database. Like BackupSQLite, its
// standalone artifact is admission-blocked and includes committed WAL contents.
// A live source may still have cooperating writers; this is not writer fencing.
func BackupSQLiteFile(ctx context.Context, source, destination string) error {
	return sqliteExportFile(ctx, source, destination, "backup_created")
}

// RestoreSQLite exports a private owned backup to a NEW private database path.
// It never overwrites a database or returns a ready Store. Before using the
// result, callers must stop/fence old external writers, then begin a NEW
// reconciliation and collect authoritative complete inventory after its barrier.
// A historical snapshot cannot prove the absence of post-backup resources.
func RestoreSQLite(ctx context.Context, backup, destination string) error {
	return sqliteExportFile(ctx, backup, destination, "backup_restored")
}

func sqliteExportFile(ctx context.Context, input, destination, auditKind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := sqlitePrivatePath(input, true)
	if err != nil {
		return err
	}
	location := url.URL{Scheme: "file", Path: source}
	if err := sqlitePreflight(location); err != nil {
		return err
	}
	location.RawQuery = "mode=ro&_busy_timeout=100&_foreign_keys=on"
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 1 && version != 2 && version != 3 && version != 4 {
		return errors.New("export requires an owned versioned ledger")
	}
	return sqliteExport(ctx, db, destination, auditKind, func() error { return nil })
}

// The parent must be private: atomic publication and SQLite sidecar handling
// assume no untrusted directory writer. This does not defend against the owner
// independently editing files or concurrently reusing the destination path.
func sqlitePrivatePath(path string, exists bool) (string, error) {
	if path == "" {
		return "", errors.New("a local database path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := os.Lstat(filepath.Dir(absolute))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return "", errors.New("backup and restore require a private real parent directory")
	}
	info, err := os.Lstat(absolute)
	if exists {
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("backup must be a private regular file")
		}
	} else {
		if err == nil {
			return "", os.ErrExist
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(absolute + suffix); !errors.Is(err, os.ErrNotExist) {
				return "", errors.New("destination has an existing SQLite sidecar")
			}
		}
	}
	return absolute, nil
}

func sqliteExport(ctx context.Context, source *sql.DB, destination, auditKind string, sourceOK func() error) error {
	// Busy sources and continuously changing large databases must not wait forever.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := sqlitePrivatePath(destination, false)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".ledger-backup-*.db")
	if err != nil {
		return err
	}
	temporary := temp.Name()
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(temporary + suffix)
		}
	}()
	if err := temp.Close(); err != nil {
		return err
	}
	location := url.URL{Scheme: "file", Path: temporary, RawQuery: "mode=rw&_busy_timeout=100&_synchronous=FULL"}
	target, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return err
	}
	target.SetMaxOpenConns(1)
	err = sqliteOnlineCopy(ctx, source, target)
	closeErr := target.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := sourceOK(); err != nil {
		return err
	}

	// Validate ownership before any writable migration, then validate logical
	// accounting as well as physical integrity on the isolated copy.
	artifact, err := OpenSQLite(temporary, Config{})
	if err != nil {
		return err
	}
	defer artifact.Close()
	if err := sqliteCheckArtifact(ctx, artifact); err != nil {
		return err
	}
	if err := artifact.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE metadata SET ready=0,nonce='',began_at='' WHERE id=1"); err != nil {
			return err
		}
		return sqliteAudit(tx, auditKind, "", artifact.now())
	}); err != nil {
		return err
	}
	// Leave one standalone database, not a .db file that still needs WAL pages.
	var mode string
	if err := artifact.db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return errors.New("could not finalize standalone backup")
	}
	if err := artifact.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr = file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// link is an atomic no-overwrite install on this same local filesystem.
	if _, err := sqlitePrivatePath(path, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("%w: backup published but directory sync unavailable", ErrUncertain)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("%w: backup published but directory sync failed", ErrUncertain)
	}
	if err := os.Remove(temporary); err != nil {
		return fmt.Errorf("%w: backup published but temporary link remains", ErrUncertain)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("%w: backup published but cleanup sync failed", ErrUncertain)
	}
	return nil
}

func sqliteOnlineCopy(ctx context.Context, source, target *sql.DB) error {
	src, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := target.Conn(ctx)
	if err != nil {
		return err
	}
	defer dst.Close()
	return src.Raw(func(sourceDriver any) error {
		sourceConn, ok := sourceDriver.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("unsupported SQLite backup source driver")
		}
		return dst.Raw(func(targetDriver any) (result error) {
			targetConn, ok := targetDriver.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unsupported SQLite backup destination driver")
			}
			backup, err := targetConn.Backup("main", sourceConn, "main")
			if err != nil {
				return err
			}
			defer func() {
				if err := backup.Finish(); result == nil {
					result = err
				}
			}()
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				done, err := backup.Step(128)
				if err != nil {
					return err
				}
				if done {
					return nil
				}
				// Step reports BUSY/LOCKED as not-done without an error. Yield on all
				// incomplete steps, respecting cancellation and avoiding a busy spin.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
		})
	})
}

func sqliteCheckArtifact(ctx context.Context, s *SQLiteStore) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			rows.Close()
			return err
		}
		if result != "ok" {
			rows.Close()
			return errors.New("backup integrity check failed")
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("backup integrity check incomplete")
	}
	rows, err = s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	invalid := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("backup foreign key check failed")
	}
	_, err = s.Snapshot(ctx)
	return err
}
