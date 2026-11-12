package admission

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

// readTransaction opens a separate PHYSICALLY read-only SQLite connection.
// Request cancellation can roll back its view, never poison the shared writer.
// No core ambiguous-write-commit classification is relaxed by this method.
func (s *SQLiteStore) readTransaction(ctx context.Context, read func(*sql.Tx) error) error {
	if s.closed.Load() {
		return sql.ErrConnDone
	}
	if s.poisoned.Load() {
		return ErrUncertain
	}
	if s.path == "" {
		return ErrDenied
	}
	info, err := os.Lstat(s.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return ErrDenied
	}
	location := url.URL{Scheme: "file", Path: s.path}
	query := url.Values{"mode": {"ro"}, "_busy_timeout": {"2000"}, "_foreign_keys": {"on"}, "_query_only": {"true"}, "_txlock": {"deferred"}}
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var queryOnly, application, version int
	if err = tx.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil {
		return err
	}
	if err = tx.QueryRow("PRAGMA application_id").Scan(&application); err != nil {
		return err
	}
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if queryOnly != 1 || application != 0x47434331 || version != 4 {
		return errors.New("read view requires an owned current ledger")
	}
	if err = read(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	} // A read-only rollback cannot commit allocation changes.
	if s.closed.Load() {
		return sql.ErrConnDone
	}
	if s.poisoned.Load() {
		return ErrUncertain
	}
	return nil
}

// ReadSnapshot is a consistent, bounded administrative view. Unlike Snapshot it
// never changes readiness/audit and does not own a writer transaction. Expired
// readiness is reported false in the view; admission/dispatch and authoritative
// reconciliation still enforce/persist their own readiness checks independently.
func (s *SQLiteStore) ReadSnapshot(ctx context.Context) (SQLiteSnapshot, error) {
	var result SQLiteSnapshot
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		result.Config, result.Ready, result.Epoch, _, err = sqliteMetadata(tx)
		if err != nil {
			return err
		}
		result.Reservations, err = sqliteReservations(tx)
		if err != nil {
			return err
		}
		result.RecentCancellations, result.CancellationCount, err = sqliteRecentCancellations(tx)
		if err != nil {
			return err
		}
		result.Inventory, err = sqliteInventory(tx)
		if err != nil {
			return err
		}
		result.LegacyUnboundHosts, err = sqliteLegacyUnboundHosts(tx, result.Config)
		if err != nil {
			return err
		}
		result.HostBindings, err = sqliteHostBindings(tx, result.Config)
		if err != nil {
			return err
		}
		result.Allocated, err = sqliteAllocations(result.Config, result.Reservations, result.Inventory)
		if err != nil {
			return err
		}
		rows, err := tx.Query("SELECT sequence,kind,request_key,recorded_at FROM audit ORDER BY sequence DESC LIMIT 100")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event AuditEvent
			var recorded string
			if err := rows.Scan(&event.Sequence, &event.Kind, &event.RequestKey, &recorded); err != nil {
				return err
			}
			event.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
			if err != nil {
				return err
			}
			if strings.HasPrefix(event.Kind, "host_mode_") {
				event.Host = event.RequestKey
				event.RequestKey = ""
			}
			result.Audit = append(result.Audit, event)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if result.Ready && !sqliteObservationsFresh(result.Config, s.now()) {
			result.Ready = false
		}
		return nil
	})
	return result, err
}
