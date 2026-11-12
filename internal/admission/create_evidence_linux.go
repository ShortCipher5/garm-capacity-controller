package admission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

var ErrCreateEvidence = errors.New("authenticated original-create observation unavailable or conflicting")

const sqliteCreateEvidenceSchema = `
CREATE TABLE create_evidence (
 request_key TEXT PRIMARY KEY NOT NULL REFERENCES dispatches(reservation_key),
 scope TEXT NOT NULL CHECK(length(scope)=64),
 operation_id TEXT NOT NULL CHECK(length(operation_id)=36),
 subject_fingerprint TEXT NOT NULL CHECK(length(subject_fingerprint)=64),
 host_binding_sequence INTEGER NOT NULL REFERENCES audit(sequence) CHECK(host_binding_sequence>0),
 digest TEXT NOT NULL CHECK(length(digest)=64),
 record BLOB NOT NULL CHECK(length(record)>0 AND length(record)<=16384),
 UNIQUE(scope,operation_id)
) STRICT;
CREATE TRIGGER create_evidence_immutable_update BEFORE UPDATE ON create_evidence
BEGIN SELECT RAISE(ABORT,'create evidence is immutable'); END;
CREATE TRIGGER create_evidence_immutable_delete BEFORE DELETE ON create_evidence
BEGIN SELECT RAISE(ABORT,'create evidence is immutable'); END;
PRAGMA user_version=5;
`

// CreateEvidence is durable, whitelisted observation history, NOT a verified
// constructor, exclusive incarnation history, retirement or release authority.
// Reopening/decoding it never manufactures an opaque authenticated observation.
type CreateEvidence struct {
	Version             int                              `json:"version"`
	Request             Request                          `json:"request"`
	HostBindingSequence int64                            `json:"host_binding_sequence"`
	Observation         incusapi.TerminalOperationRecord `json:"observation"`
	StoredAt            time.Time                        `json:"stored_at"`
	Replay              bool                             `json:"-"`
}

func evidenceDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// Match the qualified transport's bounded name/project grammar on disk too.
func evidenceName(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for i, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r == '-' && i > 0)) {
			return false
		}
	}
	return true
}

func validCreateEvidence(e CreateEvidence) bool {
	r, subject := e.Observation, e.Observation.Identity.Subject
	if e.Version != 1 || e.HostBindingSequence <= 0 || !validRequest(e.Request) || e.StoredAt.IsZero() || r.Kind != incusapi.RecoveryCreate || r.Class != "task" || r.Description != "Creating instance" || r.StatusCode != 200 || !r.ResourceReadbackMatched || !r.RequiresExclusiveFence || !validOperationID(r.OperationID) || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || r.CapturedAt.IsZero() || r.Identity.ObservedAt.IsZero() || r.Identity.ObservedAt.After(r.CapturedAt) || r.CapturedAt.After(e.StoredAt) {
		return false
	}
	if !bindingFingerprint(subject.BackendKeyFingerprint) || !bindingFingerprint(subject.OriginFingerprint) || !bindingFingerprint(subject.ScopeFingerprint) || !bindingName(subject.ServerName) || subject.ServerVersion != "6.0.4" || !evidenceName(subject.Project) || !evidenceName(subject.Name) || !validOperationID(subject.UUID) || !validOperationID(subject.Generation) || subject.CreatedAt.IsZero() || !bindingName(subject.ControllerID) || subject.PoolID != e.Request.Pool || subject.ReservationKey != e.Request.Key || subject.CPUMillis != e.Request.Resources.CPU || subject.RAMMiB != e.Request.Resources.RAM {
		return false
	}
	if strings.ContainsRune(subject.ControllerID, 0) || strings.ContainsRune(subject.PoolID, 0) || strings.ContainsRune(subject.Name, 0) || evidenceDigest([]byte(subject.ControllerID+"\x00"+subject.PoolID+"\x00"+subject.Name)) != e.Request.Key {
		return false
	}
	encoded, err := json.Marshal(subject)
	return err == nil && evidenceDigest(encoded) == r.Identity.SubjectFingerprint
}

func lookupCreateEvidence(tx *sql.Tx, key string) (CreateEvidence, error) {
	var scope, operation, subject, digest string
	var data []byte
	var sequence int64
	if err := tx.QueryRow("SELECT scope,operation_id,subject_fingerprint,host_binding_sequence,digest,record FROM create_evidence WHERE request_key=?", key).Scan(&scope, &operation, &subject, &sequence, &digest, &data); err != nil {
		return CreateEvidence{}, err
	}
	if len(data) == 0 || len(data) > 16384 || !bindingFingerprint(digest) || evidenceDigest(data) != digest {
		return CreateEvidence{}, ErrCreateEvidence
	}
	var record CreateEvidence
	if err := json.Unmarshal(data, &record); err != nil || !validCreateEvidence(record) || record.HostBindingSequence != sequence || record.Request.Key != key || record.Observation.Identity.Subject.ScopeFingerprint != scope || record.Observation.OperationID != operation || record.Observation.Identity.SubjectFingerprint != subject {
		return CreateEvidence{}, ErrCreateEvidence
	}
	// Reject unknown, duplicate, noncanonical or trailing JSON, rather than letting
	// a future reader interpret more fields than the original whitelisted record.
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(canonical, data) {
		return CreateEvidence{}, ErrCreateEvidence
	}
	return record, nil
}

// StoreCreateObservation appends an authenticated CREATE observation only when
// its exact request, original KNOWN dispatch operation/resource and immutable v4
// backend binding still match in ONE BEGIN IMMEDIATE transaction. Unknown intent
// without an original operation UUID is never upgraded. DELETE needs a separate
// durable retirement-intent model and is deliberately refused here.
//
// Capturing while admission is blocked is allowed: it changes no charge, dispatch
// state, inventory, host binding, readiness or external resource. Local timestamps
// enforce append order only, not remote clocks/job safety/exclusive writer fencing.
// Any lost/uncertain commit retains the normal store poison behavior; exact retry
// after reopening is safe and returns the FIRST immutable observation.
func (s *SQLiteStore) StoreCreateObservation(ctx context.Context, req Request, observation incusapi.TerminalOperationObservation) (CreateEvidence, error) {
	if !validRequest(req) || !observation.Valid() {
		return CreateEvidence{}, ErrCreateEvidence
	}
	candidate := CreateEvidence{Version: 1, Request: req, Observation: observation.Record()}
	var result CreateEvidence
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		candidate.StoredAt = s.now().UTC()
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		pool, ok := config.Pools[req.Pool]
		if _, hostOK := config.Hosts[req.Host]; !ok || !hostOK || !contains(pool.Hosts, req.Host) {
			return ErrCreateEvidence
		}
		var current Request
		var state string
		if err := tx.QueryRow("SELECT request_key,pool,host,cpu,ram,runners,state FROM reservations WHERE request_key=?", req.Key).Scan(&current.Key, &current.Pool, &current.Host, &current.Resources.CPU, &current.Resources.RAM, &current.Resources.Runners, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCreateEvidence
			}
			return err
		}
		if current != req || !validState(state) {
			return ErrCreateEvidence
		}
		dispatch, _, err := dispatchRecord(tx, req.Key)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCreateEvidence
			}
			return err
		}
		subject := candidate.Observation.Identity.Subject
		if (dispatch.State != "submitted" && dispatch.State != "confirmed" && dispatch.State != "unknown") || dispatch.OperationID != candidate.Observation.OperationID || dispatch.ResourceID != subject.Name || dispatch.CreatedAt.IsZero() || dispatch.CreatedAt.After(candidate.Observation.Identity.ObservedAt) {
			return ErrCreateEvidence
		}
		candidate.HostBindingSequence = dispatch.HostBindingSequence
		if !validCreateEvidence(candidate) {
			return ErrCreateEvidence
		}
		bindings, err := sqliteHostBindings(tx, config)
		if err != nil {
			return err
		}
		bound, exists := bindings[req.Host]
		if !exists || bound.Sequence <= 0 || bound.Sequence != dispatch.HostBindingSequence || bound.ServerKeyFingerprint != subject.BackendKeyFingerprint || bound.OriginFingerprint != subject.OriginFingerprint || bound.ScopeFingerprint != subject.ScopeFingerprint || bound.ServerName != subject.ServerName || bound.ServerVersion != subject.ServerVersion || bound.ObservedAt.After(dispatch.CreatedAt) {
			return ErrCreateEvidence
		}
		var excluded bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM host_binding_history_exclusions WHERE host=? UNION ALL SELECT 1 FROM cancellations WHERE request_key=? LIMIT 1)", req.Host, req.Key).Scan(&excluded); err != nil {
			return err
		}
		if excluded {
			return ErrCreateEvidence
		}
		if previous, err := lookupCreateEvidence(tx, req.Key); err == nil {
			if candidate.StoredAt.Before(previous.StoredAt) || candidate.Observation.CapturedAt.Before(previous.Observation.CapturedAt) || candidate.Observation.Identity.ObservedAt.Before(previous.Observation.Identity.ObservedAt) {
				return ErrCreateEvidence
			}
			// A new authenticated capture may have later LOCAL observation/capture times.
			// Compare every remote identity/operation field, preserve the original times.
			old, fresh := previous.Observation, candidate.Observation
			fresh.Identity.ObservedAt = old.Identity.ObservedAt
			fresh.Identity.InstanceStatus = old.Identity.InstanceStatus
			fresh.CapturedAt = old.CapturedAt
			if previous.Request != req || previous.HostBindingSequence != candidate.HostBindingSequence || fresh != old {
				return ErrCreateEvidence
			}
			previous.Replay = true
			result = previous
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		encoded, err := json.Marshal(candidate)
		if err != nil || len(encoded) > 16384 {
			return ErrCreateEvidence
		}
		if _, err := tx.Exec("INSERT INTO create_evidence(request_key,scope,operation_id,subject_fingerprint,host_binding_sequence,digest,record) VALUES(?,?,?,?,?,?,?)", req.Key, subject.ScopeFingerprint, candidate.Observation.OperationID, candidate.Observation.Identity.SubjectFingerprint, candidate.HostBindingSequence, evidenceDigest(encoded), encoded); err != nil {
			return err
		}
		if err := sqliteAudit(tx, "create_observation_recorded", req.Key, candidate.StoredAt); err != nil {
			return err
		}
		result = candidate
		return nil
	})
	if err != nil {
		return CreateEvidence{}, err
	}
	return result, nil
}

// LookupCreateEvidence returns one indexed historical record through a physically
// read-only transaction. This is history, not live absence/retirement authority.
func (s *SQLiteStore) LookupCreateEvidence(ctx context.Context, key string) (CreateEvidence, error) {
	if !bindingName(key) {
		return CreateEvidence{}, ErrCreateEvidence
	}
	var result CreateEvidence
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { var err error; result, err = lookupCreateEvidence(tx, key); return err })
	if err != nil {
		return CreateEvidence{}, err
	}
	return result, nil
}
