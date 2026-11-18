package admission

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

var ErrRetirementAuthority = errors.New("retirement requires qualified host ownership and job completion authority")

const sqliteRetirementSchema = `
CREATE TABLE IF NOT EXISTS retirement_intents (
 request_key TEXT PRIMARY KEY NOT NULL REFERENCES create_evidence(request_key),
 subject_fingerprint TEXT NOT NULL CHECK(length(subject_fingerprint)=64),
 record BLOB NOT NULL CHECK(length(record)>0 AND length(record)<=16384)
) STRICT;
CREATE TRIGGER IF NOT EXISTS retirement_intents_immutable_update BEFORE UPDATE ON retirement_intents
BEGIN SELECT RAISE(ABORT,'retirement intents are immutable'); END;
CREATE TRIGGER IF NOT EXISTS retirement_intents_immutable_delete BEFORE DELETE ON retirement_intents
BEGIN SELECT RAISE(ABORT,'retirement intents are immutable'); END;
CREATE TABLE IF NOT EXISTS retirement_attempts (
 request_key TEXT PRIMARY KEY NOT NULL REFERENCES retirement_intents(request_key),
 attempt_id TEXT UNIQUE NOT NULL CHECK(length(attempt_id)=36),
 operation_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('unknown','submitted','terminal_observed')),
 observation BLOB NOT NULL DEFAULT X''
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS retirement_operation_unique ON retirement_attempts(operation_id) WHERE operation_id!='';
CREATE TRIGGER IF NOT EXISTS retirement_attempt_identity_immutable BEFORE UPDATE ON retirement_attempts
WHEN NEW.request_key!=OLD.request_key OR NEW.attempt_id!=OLD.attempt_id
 OR (OLD.operation_id!='' AND NEW.operation_id!=OLD.operation_id)
 OR (OLD.state='submitted' AND NEW.state='unknown')
 OR (OLD.state='terminal_observed' AND (NEW.state!=OLD.state OR NEW.observation!=OLD.observation))
BEGIN SELECT RAISE(ABORT,'retirement attempt cannot be rebound'); END;
CREATE TRIGGER IF NOT EXISTS retirement_attempts_no_delete BEFORE DELETE ON retirement_attempts
BEGIN SELECT RAISE(ABORT,'retirement attempts are durable'); END;
PRAGMA user_version=15;
`

// RetirementIntent pins the ORIGINAL creation incarnation, not whichever object
// currently has the same name. A stored intent is not permission to delete it.
type RetirementIntent struct {
	Request     Request        `json:"request"`
	Creation    CreateEvidence `json:"creation"`
	RequestedAt time.Time      `json:"requested_at"`
}

type RetirementRecovery struct {
	Intent      RetirementIntent `json:"intent"`
	State       string           `json:"state"`
	OperationID string           `json:"operation_id,omitempty"`
	// RecoveryAction never requests another DELETE. An unknown/lost receipt is
	// held, not inferred to have failed before dispatch.
	RecoveryAction string `json:"recovery_action"`
}

func retirementIntent(tx *sql.Tx, key string) (RetirementIntent, error) {
	var data []byte
	var fingerprint string
	var result RetirementIntent
	if err := tx.QueryRow("SELECT subject_fingerprint,record FROM retirement_intents WHERE request_key=?", key).Scan(&fingerprint, &data); err != nil {
		return result, err
	}
	if len(data) > 16384 || json.Unmarshal(data, &result) != nil || result.Request.Key != key || result.Request != result.Creation.Request || !validCreateEvidence(result.Creation) || result.RequestedAt.Before(result.Creation.StoredAt) || fingerprint != result.Creation.Observation.Identity.SubjectFingerprint {
		return RetirementIntent{}, ErrConflict
	}
	return result, nil
}

// RequestRetirement atomically journals intent against immutable original-create
// evidence and the exact still-charged reservation. Replays return the ORIGINAL
// record. It does not mark deleting, open a network connection or grant authority.
func (s *SQLiteStore) RequestRetirement(ctx context.Context, req Request) (RetirementIntent, error) {
	var result RetirementIntent
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
		r, ok := reservations[req.Key]
		if !ok || r.Request != req {
			return ErrConflict
		}
		creation, err := lookupCreateEvidence(tx, req.Key)
		if err != nil {
			return err
		}
		if creation.Request != req {
			return ErrConflict
		}
		old, err := retirementIntent(tx, req.Key)
		if err == nil {
			if old.Request != req || old.Creation.Observation.Identity.SubjectFingerprint != creation.Observation.Identity.SubjectFingerprint {
				return ErrConflict
			}
			result = old
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		result = RetirementIntent{Request: req, Creation: creation, RequestedAt: s.now().UTC()}
		if result.RequestedAt.Before(creation.StoredAt) || result.RequestedAt.IsZero() {
			return ErrConflict
		}
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded) > 16384 {
			return ErrConflict
		}
		if _, err := tx.Exec("INSERT INTO retirement_intents(request_key,subject_fingerprint,record) VALUES(?,?,?)", req.Key, creation.Observation.Identity.SubjectFingerprint, encoded); err != nil {
			return err
		}
		return sqliteAudit(tx, "retirement_requested_held", req.Key, s.now())
	})
	if err != nil {
		return RetirementIntent{}, err
	}
	return result, nil
}

// No production constructor currently exists for a dispatch permit. Future
// authority implementation must commit a unique attempt BEFORE invoking a backend.
// The journal then retains ambiguous delivery across restart; it never reissues.
type RetirementPermit struct{ key, attempt string }

func (s *SQLiteStore) ClaimRetirement(ctx context.Context, req Request) (*RetirementPermit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrRetirementAuthority
}

// RecordRetirementSubmission binds the returned operation to the committed
// one-shot attempt. Repeated receipt persistence is idempotent, but a different
// operation is a conflict. It does not invoke a backend or release capacity.
func (s *SQLiteStore) RecordRetirementSubmission(ctx context.Context, permit RetirementPermit, operation string) error {
	if !bindingName(permit.key) || !validOperationID(permit.attempt) || !validOperationID(operation) {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		intent, err := retirementIntent(tx, permit.key)
		if err != nil {
			return err
		}
		if operation == intent.Creation.Observation.OperationID {
			return ErrConflict
		}
		var attempt, old string
		if err := tx.QueryRow("SELECT attempt_id,operation_id FROM retirement_attempts WHERE request_key=?", permit.key).Scan(&attempt, &old); err != nil {
			return err
		}
		if attempt != permit.attempt || (old != "" && old != operation) {
			return ErrConflict
		}
		if old == operation {
			return nil
		}
		if _, err := tx.Exec("UPDATE retirement_attempts SET operation_id=?,state='submitted' WHERE request_key=?", operation, permit.key); err != nil {
			return err
		}
		return sqliteAudit(tx, "retirement_operation_recorded", permit.key, s.now())
	})
}

// StoreRetirementObservation persists only authenticated terminal evidence for
// the ORIGINAL recorded operation and subject. Even terminal success remains
// charged: Incus operation resources are name-only, not exclusive authority.
func (s *SQLiteStore) StoreRetirementObservation(ctx context.Context, key string, observation incusapi.TerminalOperationObservation) error {
	if !observation.Valid() {
		return ErrDenied
	}
	record := observation.Record()
	if record.Kind != incusapi.RecoveryDelete {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		intent, err := retirementIntent(tx, key)
		if err != nil {
			return err
		}
		if record.Identity.SubjectFingerprint != intent.Creation.Observation.Identity.SubjectFingerprint || record.Identity.Subject != intent.Creation.Observation.Identity.Subject {
			return ErrConflict
		}
		var operation, state string
		var saved []byte
		if err := tx.QueryRow("SELECT operation_id,state,observation FROM retirement_attempts WHERE request_key=?", key).Scan(&operation, &state, &saved); err != nil {
			return err
		}
		if operation == "" || operation != record.OperationID {
			return ErrConflict
		}
		if state == "terminal_observed" {
			var old incusapi.TerminalOperationRecord
			if json.Unmarshal(saved, &old) != nil || record.CapturedAt.Before(old.CapturedAt) || record.Identity.ObservedAt.Before(old.Identity.ObservedAt) {
				return ErrConflict
			}
			fresh := record
			fresh.CapturedAt = old.CapturedAt
			fresh.Identity.ObservedAt = old.Identity.ObservedAt
			normalized, err := json.Marshal(fresh)
			if err != nil || !bytes.Equal(normalized, saved) {
				return ErrConflict
			}
			return nil
		}
		encoded, err := json.Marshal(record)
		if err != nil || len(encoded) > 16384 {
			return ErrConflict
		}
		if _, err := tx.Exec("UPDATE retirement_attempts SET state='terminal_observed',observation=? WHERE request_key=?", encoded, key); err != nil {
			return err
		}
		return sqliteAudit(tx, "retirement_terminal_observed_still_charged", key, s.now())
	})
}

func (s *SQLiteStore) RecoverRetirement(ctx context.Context, key string) (RetirementRecovery, error) {
	var result RetirementRecovery
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		intent, err := retirementIntent(tx, key)
		if err != nil {
			return err
		}
		result.Intent = intent
		err = tx.QueryRow("SELECT state,operation_id FROM retirement_attempts WHERE request_key=?", key).Scan(&result.State, &result.OperationID)
		if errors.Is(err, sql.ErrNoRows) {
			result.State = "authority_blocked"
			result.RecoveryAction = "retain_charge_no_dispatch"
			return nil
		}
		if err != nil {
			return err
		}
		switch result.State {
		case "unknown":
			result.RecoveryAction = "retain_charge_resolve_lost_response_no_redispatch"
		case "submitted":
			if !validOperationID(result.OperationID) {
				return ErrConflict
			}
			result.RecoveryAction = "observe_original_operation_only"
		case "terminal_observed":
			if !validOperationID(result.OperationID) {
				return ErrConflict
			}
			result.RecoveryAction = "retain_charge_await_qualified_finalizer"
		default:
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return RetirementRecovery{}, err
	}
	return result, nil
}
