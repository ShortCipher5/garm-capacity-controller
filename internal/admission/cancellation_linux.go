package admission

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrCancelled = errors.New("request permanently cancelled before dispatch")

const sqliteCancellationSchema = `
CREATE TABLE cancellation_history_exclusions (request_key TEXT PRIMARY KEY NOT NULL) STRICT;
CREATE TRIGGER cancellation_history_exclusions_immutable_update BEFORE UPDATE ON cancellation_history_exclusions
BEGIN SELECT RAISE(ABORT,'cancellation history exclusions are immutable'); END;
CREATE TRIGGER cancellation_history_exclusions_immutable_delete BEFORE DELETE ON cancellation_history_exclusions
BEGIN SELECT RAISE(ABORT,'cancellation history exclusions are immutable'); END;
CREATE TABLE cancellations (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, request_key TEXT UNIQUE NOT NULL,
 pool TEXT NOT NULL, host TEXT NOT NULL,
 cpu INTEGER NOT NULL CHECK(cpu>0), ram INTEGER NOT NULL CHECK(ram>0),
 runners INTEGER NOT NULL CHECK(runners=1), cancelled_at TEXT NOT NULL
) STRICT;
CREATE TABLE cancellation_totals (
 id INTEGER PRIMARY KEY CHECK(id=1), total INTEGER NOT NULL CHECK(typeof(total)='integer' AND total>=0)
) STRICT;
INSERT INTO cancellation_totals(id,total) VALUES(1,0);
CREATE TRIGGER count_cancellation_insert AFTER INSERT ON cancellations
BEGIN UPDATE cancellation_totals SET total=total+1 WHERE id=1; END;
CREATE TRIGGER cancellation_requires_undispatched_reservation BEFORE INSERT ON cancellations
WHEN NOT EXISTS(SELECT 1 FROM reservations WHERE request_key=NEW.request_key
 AND pool=NEW.pool AND host=NEW.host AND cpu=NEW.cpu AND ram=NEW.ram AND runners=NEW.runners AND state='creating')
 OR EXISTS(SELECT 1 FROM cancellation_history_exclusions WHERE request_key=NEW.request_key)
 OR EXISTS(SELECT 1 FROM dispatches WHERE reservation_key=NEW.request_key)
 OR EXISTS(SELECT 1 FROM inventory WHERE reservation_key=NEW.request_key)
BEGIN SELECT RAISE(ABORT,'cancellation proof unavailable'); END;
CREATE TRIGGER cancellations_are_immutable_update BEFORE UPDATE ON cancellations
BEGIN SELECT RAISE(ABORT,'cancellations are immutable'); END;
CREATE TRIGGER cancellations_are_immutable_delete BEFORE DELETE ON cancellations
BEGIN SELECT RAISE(ABORT,'cancellations are immutable'); END;
CREATE TRIGGER cancelled_request_cannot_be_reserved BEFORE INSERT ON reservations
WHEN EXISTS(SELECT 1 FROM cancellations WHERE request_key=NEW.request_key)
BEGIN SELECT RAISE(ABORT,'cancelled request cannot be reserved'); END;
CREATE TRIGGER cancelled_request_cannot_be_rekeyed BEFORE UPDATE OF request_key ON reservations
WHEN EXISTS(SELECT 1 FROM cancellations WHERE request_key=NEW.request_key)
BEGIN SELECT RAISE(ABORT,'cancelled request cannot be reserved'); END;
PRAGMA user_version=3;
`

// Cancellation is a permanent request-identity tombstone, not physical runner
// retirement. Replay describes only an idempotent CancelUndispatched retry.
type Cancellation struct {
	Request     Request   `json:"request"`
	CancelledAt time.Time `json:"cancelled_at"`
	Replay      bool      `json:"replay"`
}

func scanCancellation(scan func(...any) error) (Cancellation, error) {
	var result Cancellation
	var at string
	err := scan(&result.Request.Key, &result.Request.Pool, &result.Request.Host, &result.Request.Resources.CPU, &result.Request.Resources.RAM, &result.Request.Resources.Runners, &at)
	if err != nil {
		return Cancellation{}, err
	}
	result.CancelledAt, err = time.Parse(time.RFC3339Nano, at)
	if err != nil || result.CancelledAt.IsZero() || !validRequest(result.Request) {
		return Cancellation{}, ErrConflict
	}
	return result, nil
}
func cancellationRecord(tx *sql.Tx, key string) (Cancellation, error) {
	return scanCancellation(tx.QueryRow("SELECT request_key,pool,host,cpu,ram,runners,cancelled_at FROM cancellations WHERE request_key=?", key).Scan)
}

// sqliteRecentCancellations is bounded administrative history ONLY, never key
// absence proof. Authorization uses complete indexed cancellationRecord lookups.
func sqliteRecentCancellations(tx *sql.Tx) (map[string]Cancellation, int64, error) {
	var total int64
	if err := tx.QueryRow("SELECT total FROM cancellation_totals WHERE id=1").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query("SELECT request_key,pool,host,cpu,ram,runners,cancelled_at FROM cancellations ORDER BY sequence DESC LIMIT 100")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := map[string]Cancellation{}
	for rows.Next() {
		record, err := scanCancellation(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		result[record.Request.Key] = record
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	// Every active allocation is checked against the full tombstone index, not
	// merely recent history; permanent history need not be scanned into memory.
	var overlaps bool
	if err := tx.QueryRow(`SELECT EXISTS(
 SELECT 1 FROM reservations r WHERE EXISTS(SELECT 1 FROM cancellations c WHERE c.request_key=r.request_key)
 UNION ALL SELECT 1 FROM dispatches d WHERE EXISTS(SELECT 1 FROM cancellations c WHERE c.request_key=d.reservation_key)
 UNION ALL SELECT 1 FROM inventory i WHERE EXISTS(SELECT 1 FROM cancellations c WHERE c.request_key=i.reservation_key)
 LIMIT 1)`).Scan(&overlaps); err != nil {
		return nil, 0, err
	}
	if overlaps {
		return nil, 0, ErrConflict
	}
	return result, total, nil
}

// CancelUndispatched is a trusted administrative ledger primitive, NOT provider
// DeleteInstance. ONE BEGIN IMMEDIATE transaction proves an exact still-creating
// reservation has no dispatch intent of any state and no linked resource, writes
// an immutable identity tombstone, removes only that charge, and audits the change.
// ClaimDispatch uses the same serialization boundary. Any existing intent, even
// if no callback ran, blocks cancellation; timeout/error/age are not proof.
// A fresh ready baseline is mandatory. Following historical restore, operators
// must also fence old writers/queued requests and recover later ledger history.
// No external operation is performed; capacity is freed only on successful commit.
func (s *SQLiteStore) CancelUndispatched(ctx context.Context, req Request) (Cancellation, error) {
	var result Cancellation
	var outcome error
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		deny := func(kind string, reason error) error { outcome = reason; return sqliteAudit(tx, kind, "", s.now()) }
		if !validRequest(req) {
			return deny("cancellation_denied_invalid_request", ErrDenied)
		}
		config, ready, epoch, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		if current, err := cancellationRecord(tx, req.Key); err == nil {
			if current.Request != req {
				return deny("cancellation_denied_identity_conflict", ErrConflict)
			}
			result = current
			result.Replay = true
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !ready || epoch == int64(1<<63-1) {
			return deny("cancellation_denied_reconciliation", ErrReconciliation)
		}
		if !sqliteObservationsFresh(config, s.now()) {
			if _, err := tx.Exec("UPDATE metadata SET ready=0,nonce='' WHERE id=1"); err != nil {
				return err
			}
			return deny("cancellation_denied_freshness", ErrReconciliation)
		}
		if host, ok := config.Hosts[req.Host]; !ok || !host.Healthy {
			return deny("cancellation_denied_host_health", ErrReconciliation)
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		reservation, exists := reservations[req.Key]
		if !exists {
			return deny("cancellation_denied_missing_reservation", ErrDenied)
		}
		if reservation.Request != req {
			return deny("cancellation_denied_identity_conflict", ErrConflict)
		}
		if reservation.State != "creating" {
			return deny("cancellation_denied_reservation_state", ErrDenied)
		}
		var excluded, dispatches, resources int
		if err := tx.QueryRow("SELECT COUNT(*) FROM cancellation_history_exclusions WHERE request_key=?", req.Key).Scan(&excluded); err != nil {
			return err
		}
		if excluded != 0 {
			return deny("cancellation_denied_unqualified_legacy_history", ErrDenied)
		}
		if err := tx.QueryRow("SELECT COUNT(*) FROM dispatches WHERE reservation_key=?", req.Key).Scan(&dispatches); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT COUNT(*) FROM inventory WHERE reservation_key=?", req.Key).Scan(&resources); err != nil {
			return err
		}
		if dispatches != 0 || resources != 0 {
			return deny("cancellation_denied_existing_dispatch_or_resource", ErrDispatchClaimed)
		}
		at := s.now().UTC()
		if at.IsZero() {
			return ErrConflict
		}
		if _, err := tx.Exec("INSERT INTO cancellations(request_key,pool,host,cpu,ram,runners,cancelled_at) VALUES(?,?,?,?,?,?,?)", req.Key, req.Pool, req.Host, req.Resources.CPU, req.Resources.RAM, req.Resources.Runners, at.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		deleted, err := tx.Exec("DELETE FROM reservations WHERE request_key=?", req.Key)
		if err != nil {
			return err
		}
		count, err := deleted.RowsAffected()
		if err != nil || count != 1 {
			return ErrConflict
		}
		if err := sqliteAudit(tx, "cancelled_undispatched", req.Key, at); err != nil {
			return err
		}
		result = Cancellation{Request: req, CancelledAt: at}
		return nil
	})
	if err != nil {
		return Cancellation{}, err
	}
	return result, outcome
}

func (s *SQLiteStore) LookupCancellation(ctx context.Context, key string) (Cancellation, error) {
	var result Cancellation
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { var err error; result, err = cancellationRecord(tx, key); return err })
	if err != nil {
		return Cancellation{}, err
	}
	return result, nil
}
