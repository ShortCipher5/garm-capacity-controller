package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// ErrReleaseEvidence identifies absence evidence that is missing, unverified,
// chronologically impossible or unbound from the retiring request. Every
// mismatch fails closed: the reservation stays 'deleting' and fully charged.
var ErrReleaseEvidence = errors.New("confirmed-absence release evidence unavailable or conflicting")

// Schema 11 adds the immutable retirement tombstone. A released request
// identity can never be reserved again, mirroring the cancellation tombstone:
// the retired incarnation must not be silently re-bound to a new runner. The
// statements are idempotent: qualified downgrade tests roll user_version back
// without dropping newer tables, and reopening must re-apply cleanly.
const sqliteReleaseSchema = `
CREATE TABLE IF NOT EXISTS releases (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, request_key TEXT UNIQUE NOT NULL,
 pool TEXT NOT NULL, host TEXT NOT NULL,
 cpu INTEGER NOT NULL CHECK(cpu>0), ram INTEGER NOT NULL CHECK(ram>0),
 disk INTEGER NOT NULL CHECK(disk>=0),
 runners INTEGER NOT NULL CHECK(runners=1),
 absence_digest TEXT NOT NULL CHECK(length(absence_digest)=64),
 record BLOB NOT NULL CHECK(length(record)>0 AND length(record)<=16384),
 released_at TEXT NOT NULL
) STRICT;
CREATE TRIGGER IF NOT EXISTS releases_are_immutable_update BEFORE UPDATE ON releases
BEGIN SELECT RAISE(ABORT,'releases are immutable'); END;
CREATE TRIGGER IF NOT EXISTS releases_are_immutable_delete BEFORE DELETE ON releases
BEGIN SELECT RAISE(ABORT,'releases are immutable'); END;
CREATE TRIGGER IF NOT EXISTS released_request_cannot_be_reserved BEFORE INSERT ON reservations
WHEN EXISTS(SELECT 1 FROM releases WHERE request_key=NEW.request_key)
BEGIN SELECT RAISE(ABORT,'released request cannot be reserved'); END;
CREATE TRIGGER IF NOT EXISTS released_request_cannot_be_rekeyed BEFORE UPDATE OF request_key ON reservations
WHEN EXISTS(SELECT 1 FROM releases WHERE request_key=NEW.request_key)
BEGIN SELECT RAISE(ABORT,'released request cannot be reserved'); END;
PRAGMA user_version=11;
`

// Release is a permanent confirmed-absence retirement tombstone, not a lease
// or a reusable credit. Replay is a lookup of this exact identity only.
type Release struct {
	Request       Request   `json:"request"`
	AbsenceDigest string    `json:"absence_digest"`
	ReleasedAt    time.Time `json:"released_at"`
}

// ReleaseEvidence bundles the authenticated confirmed-absence observation that
// authorizes exactly one capacity release, together with the durable 'deleting'
// mark recorded before the destructive call. The observation can only be minted
// by credential-verified transport reads; operator JSON or zero values never
// validate, and the two matching project-scoped passes never become an atomic
// physical-absence interval, a writer fence or job-safety proof.
type ReleaseEvidence struct {
	Absence incusapi.ProjectAbsenceObservation `json:"absence"`
}

func scanRelease(scan func(...any) error) (Release, error) {
	var result Release
	var at string
	err := scan(&result.Request.Key, &result.Request.Pool, &result.Request.Host, &result.Request.Resources.CPU, &result.Request.Resources.RAM, &result.Request.Resources.Disk, &result.Request.Resources.Runners, &result.AbsenceDigest, &at)
	if err != nil {
		return Release{}, err
	}
	result.ReleasedAt, err = time.Parse(time.RFC3339Nano, at)
	if err != nil || result.ReleasedAt.IsZero() || !validRequest(result.Request) || !bindingFingerprint(result.AbsenceDigest) {
		return Release{}, ErrConflict
	}
	return result, nil
}

func releaseRecord(tx *sql.Tx, key string) (Release, error) {
	return scanRelease(tx.QueryRow("SELECT request_key,pool,host,cpu,ram,disk,runners,absence_digest,released_at FROM releases WHERE request_key=?", key).Scan)
}

func sqliteReleasedKeys(tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.Query("SELECT request_key FROM releases")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		result[key] = true
	}
	return result, rows.Err()
}

func validReleaseEvidence(e ReleaseEvidence, req Request) bool {
	if !e.Absence.Valid() {
		return false
	}
	record := e.Absence.Record()
	// Two matching project-scoped passes must actually span distinct times;
	// a single instant never confirms absence.
	if record.StartedAt.IsZero() || !record.CompletedAt.After(record.StartedAt) {
		return false
	}
	// The incarnation must have been observed before the absence window began.
	if record.Identity.ObservedAt.IsZero() || record.Identity.ObservedAt.After(record.StartedAt) {
		return false
	}
	if !bindingFingerprint(record.InstanceInventoryFingerprint) || !bindingFingerprint(record.OperationInventoryFingerprint) {
		return false
	}
	subject := record.Identity.Subject
	if !bindingFingerprint(subject.BackendKeyFingerprint) || !bindingFingerprint(subject.OriginFingerprint) || !bindingFingerprint(subject.ScopeFingerprint) || !bindingName(subject.ServerName) || subject.ServerVersion != "6.0.4" || !evidenceName(subject.Project) || !evidenceName(subject.Name) || !validOperationID(subject.UUID) || !validOperationID(subject.Generation) || subject.CreatedAt.IsZero() || !bindingName(subject.ControllerID) || subject.PoolID != req.Pool || subject.ReservationKey != req.Key || subject.CPUMillis != req.Resources.CPU || subject.RAMMiB != req.Resources.RAM {
		return false
	}
	if strings.ContainsRune(subject.ControllerID, 0) || strings.ContainsRune(subject.PoolID, 0) || strings.ContainsRune(subject.Name, 0) || evidenceDigest([]byte(subject.ControllerID+"\x00"+subject.PoolID+"\x00"+subject.Name)) != req.Key {
		return false
	}
	encoded, err := json.Marshal(subject)
	return err == nil && evidenceDigest(encoded) == record.Identity.SubjectFingerprint
}

// MarkDeleting durably moves an exact, physically linked reservation to
// 'deleting' BEFORE any destructive backend call. It changes visibility only:
// every state remains fully charged. An exact replay on an already-'deleting'
// reservation returns it unchanged, so a re-dispatch after wait/absence failure
// or restart can complete without a second mark. An already-released identity
// is refused: the tombstone, not the state column, is authoritative.
func (s *SQLiteStore) MarkDeleting(ctx context.Context, req Request) (Reservation, error) {
	var result Reservation
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if !validRequest(req) {
			return ErrDenied
		}
		if _, err := releaseRecord(tx, req.Key); err == nil {
			return ErrDenied
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		reservation, exists := reservations[req.Key]
		if !exists {
			return ErrDenied
		}
		if reservation.Request != req {
			return ErrConflict
		}
		var linked int
		if err := tx.QueryRow("SELECT COUNT(*) FROM inventory WHERE reservation_key=?", req.Key).Scan(&linked); err != nil {
			return err
		}
		if linked != 1 {
			return ErrDenied
		}
		if reservation.State == "deleting" {
			result = reservation
			return nil
		}
		if _, err := tx.Exec("UPDATE reservations SET state='deleting' WHERE request_key=?", req.Key); err != nil {
			return err
		}
		if err := sqliteAudit(tx, "deleting_marked", req.Key, s.now()); err != nil {
			return err
		}
		reservation.State = "deleting"
		result = reservation
		return nil
	})
	if err != nil {
		return Reservation{}, err
	}
	return result, nil
}

// ReleaseConfirmed frees exactly one charge after confirmed absence. ONE BEGIN
// IMMEDIATE transaction verifies the reservation still exists with the exact
// identity in state 'deleting' (the mark recorded before the destructive
// call), verifies the authenticated two-pass absence evidence and its
// incarnation binding to this request, removes the linked inventory row, and
// writes the immutable release tombstone plus audit. The reservation row
// itself stays: dispatch and create-evidence history reference it by foreign
// key and their immutable journal is never rewritten. The releases tombstone
// removes the charge from all budget accounting. Any mismatch fails closed and
// the full charge stays.
func (s *SQLiteStore) ReleaseConfirmed(ctx context.Context, req Request, evidence ReleaseEvidence) (Release, error) {
	if !validRequest(req) || !validReleaseEvidence(evidence, req) {
		return Release{}, ErrReleaseEvidence
	}
	record := evidence.Absence.Record()
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) == 0 || len(encoded) > 16384 {
		return Release{}, ErrReleaseEvidence
	}
	digest := evidenceDigest(encoded)
	var result Release
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		config, ready, epoch, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		if !ready || epoch == int64(1<<63-1) {
			return ErrReconciliation
		}
		if !sqliteObservationsFresh(config, s.now()) {
			if _, err := tx.Exec("UPDATE metadata SET ready=0,nonce='' WHERE id=1"); err != nil {
				return err
			}
			return ErrReconciliation
		}
		if _, err := releaseRecord(tx, req.Key); err == nil {
			return ErrDenied
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		reservation, exists := reservations[req.Key]
		if !exists {
			return ErrDenied
		}
		if reservation.Request != req {
			return ErrConflict
		}
		if reservation.State != "deleting" {
			return ErrDenied
		}
		removed, err := tx.Exec("DELETE FROM inventory WHERE reservation_key=?", req.Key)
		if err != nil {
			return err
		}
		count, err := removed.RowsAffected()
		if err != nil || count != 1 {
			return ErrConflict
		}
		at := s.now().UTC()
		if at.IsZero() {
			return ErrConflict
		}
		if _, err := tx.Exec("INSERT INTO releases(request_key,pool,host,cpu,ram,disk,runners,absence_digest,record,released_at) VALUES(?,?,?,?,?,?,?,?,?,?)", req.Key, req.Pool, req.Host, req.Resources.CPU, req.Resources.RAM, req.Resources.Disk, req.Resources.Runners, digest, encoded, at.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if err := sqliteAudit(tx, "released_confirmed_absence", req.Key, at); err != nil {
			return err
		}
		result = Release{Request: req, AbsenceDigest: digest, ReleasedAt: at}
		return nil
	})
	if err != nil {
		return Release{}, err
	}
	return result, nil
}

// LookupRelease returns one indexed retirement tombstone through a physically
// read-only transaction. It answers idempotent replay only; it is never
// absence proof for anything else.
func (s *SQLiteStore) LookupRelease(ctx context.Context, key string) (Release, error) {
	if !bindingName(key) {
		return Release{}, ErrReleaseEvidence
	}
	var result Release
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { var err error; result, err = releaseRecord(tx, key); return err })
	if err != nil {
		return Release{}, err
	}
	return result, nil
}
