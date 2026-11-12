package admission

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var ErrDispatchClaimed = errors.New("dispatch intent already exists; no new permit")
var ErrDispatchUnknown = errors.New("dispatch outcome unknown; allocation remains charged")

const sqliteDispatchSchema = `
CREATE TABLE dispatches (
 reservation_key TEXT PRIMARY KEY NOT NULL REFERENCES reservations(request_key),
 permit_id TEXT UNIQUE NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('intent','submitted','confirmed','unknown')),
 operation_id TEXT NOT NULL, resource_id TEXT NOT NULL,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;
PRAGMA user_version=2;
`

// DispatchPermit can only be constructed by a successful new ClaimDispatch.
// Copying a permit is not permission to invoke an external operation again.
// A gateway should use ExecuteDispatch, which owns the one-shot callback.
type DispatchPermit struct {
	key string
	id  string
}

func (p DispatchPermit) ReservationKey() string { return p.key }
func (p DispatchPermit) ID() string             { return p.id }

type DispatchRecord struct {
	ReservationKey string
	State          string
	OperationID    string
	ResourceID     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DispatchReceipt contains bounded identifiers only, never bootstrap payloads,
// credentials, full response bodies, URL query strings or provider errors.
type DispatchReceipt struct {
	State       string
	OperationID string
	ResourceID  string
}

func dispatchRecord(tx *sql.Tx, key string) (DispatchRecord, string, error) {
	var record DispatchRecord
	var permit, created, updated string
	err := tx.QueryRow("SELECT reservation_key,permit_id,state,operation_id,resource_id,created_at,updated_at FROM dispatches WHERE reservation_key=?", key).Scan(&record.ReservationKey, &permit, &record.State, &record.OperationID, &record.ResourceID, &created, &updated)
	if err != nil {
		return DispatchRecord{}, "", err
	}
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return DispatchRecord{}, "", err
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return record, permit, err
}

// ClaimDispatch serializes admission state and dispatch intent. It rechecks
// current readiness/health/budgets, not merely the old reservation receipt.
// Existing intent never produces a second permit, including after restart.
func (s *SQLiteStore) ClaimDispatch(ctx context.Context, req Request) (*DispatchPermit, error) {
	var permit *DispatchPermit
	var outcome error
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		deny := func(kind string, reason error) error {
			outcome = reason
			return sqliteAudit(tx, kind, "", s.now())
		}
		config, ready, epoch, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		reservation, exists := reservations[req.Key]
		if !validRequest(req) || !exists {
			return deny("dispatch_denied_missing_reservation", ErrDenied)
		}
		if reservation.Request != req {
			return deny("dispatch_denied_identity_conflict", ErrConflict)
		}
		if _, _, err := dispatchRecord(tx, req.Key); err == nil {
			return deny("dispatch_replay_blocked", ErrDispatchClaimed)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !ready || epoch == int64(1<<63-1) {
			return deny("dispatch_denied_reconciliation", ErrReconciliation)
		}
		if !sqliteObservationsFresh(config, s.now()) {
			if _, err := tx.Exec("UPDATE metadata SET ready=0,nonce='' WHERE id=1"); err != nil {
				return err
			}
			return deny("dispatch_denied_freshness", ErrReconciliation)
		}
		host, hostOK := config.Hosts[req.Host]
		pool, poolOK := config.Pools[req.Pool]
		if !hostOK || !poolOK || !contains(pool.Hosts, req.Host) || host.Mode != "enabled" || !host.Healthy || reservation.State != "creating" {
			return deny("dispatch_denied_host_or_state", ErrDenied)
		}
		inventory, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		for _, resource := range inventory {
			if resource.ReservationKey == req.Key {
				return deny("dispatch_denied_existing_resource", ErrDispatchClaimed)
			}
		}
		allocated, err := sqliteAllocations(config, reservations, inventory)
		if err != nil {
			return err
		}
		if !fits(allocated.Global, Resources{}, config.Global) || !fits(allocated.Hosts[req.Host], Resources{}, host.Budget) || !fits(allocated.Pools[req.Pool], Resources{}, pool.Budget) {
			return deny("dispatch_denied_overcommit", ErrDenied)
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		id := hex.EncodeToString(nonce[:])
		at := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec("INSERT INTO dispatches(reservation_key,permit_id,state,operation_id,resource_id,created_at,updated_at) VALUES(?,?,'intent','','',?,?)", req.Key, id, at, at); err != nil {
			return err
		}
		if err := sqliteAudit(tx, "dispatch_claimed", req.Key, s.now()); err != nil {
			return err
		}
		permit = &DispatchPermit{key: req.Key, id: id}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return permit, outcome
}

func validDispatchIdentity(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

func validOperationID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// RecordDispatchOutcome cannot authorize another external attempt or release
// capacity. Confirmed results cannot be downgraded, and identities cannot change.
func (s *SQLiteStore) RecordDispatchOutcome(ctx context.Context, permit DispatchPermit, receipt DispatchReceipt) error {
	if permit.key == "" || permit.id == "" || (receipt.State != "submitted" && receipt.State != "confirmed" && receipt.State != "unknown") {
		return ErrConflict
	}
	if receipt.State != "unknown" && (!validOperationID(receipt.OperationID) || !validDispatchIdentity(receipt.ResourceID)) {
		return ErrConflict
	}
	if receipt.OperationID != "" && !validOperationID(receipt.OperationID) {
		return ErrConflict
	}
	if receipt.ResourceID != "" && !validDispatchIdentity(receipt.ResourceID) {
		return ErrConflict
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		current, id, err := dispatchRecord(tx, permit.key)
		if err != nil {
			return err
		}
		if id != permit.id || (current.State == "confirmed" && receipt.State != "confirmed") {
			return ErrConflict
		}
		if current.OperationID != "" && receipt.OperationID != "" && current.OperationID != receipt.OperationID {
			return ErrConflict
		}
		if current.ResourceID != "" && receipt.ResourceID != "" && current.ResourceID != receipt.ResourceID {
			return ErrConflict
		}
		if receipt.OperationID == "" {
			receipt.OperationID = current.OperationID
		}
		if receipt.ResourceID == "" {
			receipt.ResourceID = current.ResourceID
		}
		if _, err := tx.Exec("UPDATE dispatches SET state=?,operation_id=?,resource_id=?,updated_at=? WHERE reservation_key=?", receipt.State, receipt.OperationID, receipt.ResourceID, s.now().UTC().Format(time.RFC3339Nano), permit.key); err != nil {
			return err
		}
		return sqliteAudit(tx, "dispatch_"+receipt.State, permit.key, s.now())
	})
}

func (s *SQLiteStore) LookupDispatch(ctx context.Context, key string) (DispatchRecord, error) {
	var result DispatchRecord
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		result, _, err = dispatchRecord(tx, key)
		return err
	})
	return result, err
}

// ExecuteDispatch commits intent before calling the trusted backend once,
// outside the SQLite transaction. Any error, panic, cancellation or lost result
// remains charged and cannot be retried by replaying this method. The callback
// must enforce immutable host/resource placement and use verified credentials.
func (s *SQLiteStore) ExecuteDispatch(ctx context.Context, req Request, execute func(context.Context, DispatchPermit) (DispatchReceipt, error)) (DispatchRecord, error) {
	if execute == nil {
		return DispatchRecord{}, ErrDenied
	}
	permit, err := s.ClaimDispatch(ctx, req)
	if err != nil {
		return DispatchRecord{}, err
	}
	receipt, executionErr := func() (receipt DispatchReceipt, err error) {
		defer func() {
			if recover() != nil {
				err = ErrDispatchUnknown
			}
		}()
		if ctx.Err() != nil {
			return receipt, ctx.Err()
		}
		return execute(ctx, *permit)
	}()
	if executionErr != nil {
		// A lost follow-up response must not erase a known create operation.
		// Keep only validated correlation fields; never preserve provider errors.
		receipt.State = "unknown"
		if !validOperationID(receipt.OperationID) {
			receipt.OperationID = ""
		}
		if !validDispatchIdentity(receipt.ResourceID) {
			receipt.ResourceID = ""
		}
	}
	persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.RecordDispatchOutcome(persist, *permit, receipt); err != nil {
		// The original intent remains durable/charged, even if saving outcome fails.
		return DispatchRecord{}, ErrDispatchUnknown
	}
	if executionErr != nil || receipt.State == "unknown" {
		return DispatchRecord{}, ErrDispatchUnknown
	}
	record, err := s.LookupDispatch(persist, req.Key)
	if err != nil {
		return DispatchRecord{}, ErrDispatchUnknown
	}
	return record, nil
}
