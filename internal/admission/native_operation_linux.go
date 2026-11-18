package admission

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"time"
)

var ErrNativeOperation = errors.New("exact canonical native suboperation required")
var ErrNativeOperationClaimed = errors.New("native suboperation already claimed; no retry authority")

// NativeOperationClaim is returned only after the first durable suboperation
// intent. Like the parent claim it must be received through the owner's own
// authenticated exchange, never accepted as a detached bearer capability.
type NativeOperationClaim struct {
	ID             string    `json:"id"`
	ParentClaimID  string    `json:"parent_claim_id"`
	PermitID       string    `json:"permit_id"`
	EnvelopeDigest string    `json:"envelope_digest"`
	Step           string    `json:"step"`
	RequestDigest  string    `json:"request_digest"`
	ClaimedAt      time.Time `json:"claimed_at"`
}

const NativeCreateStopped = "create_stopped"
const NativeStart = "start"

const sqliteNativeOperationSchema = `
CREATE TABLE native_operation_intents (
 permit_id TEXT NOT NULL REFERENCES native_execution_consumptions(permit_id),
 step TEXT NOT NULL CHECK(step IN ('create_stopped','start')),
 claim_id TEXT NOT NULL UNIQUE CHECK(length(claim_id)=64),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER NOT NULL UNIQUE REFERENCES audit(sequence),
 PRIMARY KEY(permit_id,step)
) STRICT;
CREATE TRIGGER native_operation_intent_no_update BEFORE UPDATE ON native_operation_intents BEGIN SELECT RAISE(ABORT,'native suboperation intent immutable'); END;
CREATE TRIGGER native_operation_intent_no_delete BEFORE DELETE ON native_operation_intents BEGIN SELECT RAISE(ABORT,'native suboperation intent permanent'); END;
CREATE TABLE native_operation_receipts (
 claim_id TEXT NOT NULL REFERENCES native_operation_intents(claim_id),
 kind TEXT NOT NULL CHECK(kind IN ('accepted','succeeded','failed','unknown')),
 operation_id TEXT NOT NULL,
 domain_id TEXT NOT NULL,
 payload TEXT NOT NULL CHECK(length(payload)<=16384),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER NOT NULL UNIQUE REFERENCES audit(sequence),
 PRIMARY KEY(claim_id,kind)
) STRICT;
CREATE UNIQUE INDEX native_operation_id_once ON native_operation_receipts(domain_id,operation_id) WHERE kind='accepted';
CREATE TRIGGER native_operation_receipt_no_update BEFORE UPDATE ON native_operation_receipts BEGIN SELECT RAISE(ABORT,'native suboperation receipt immutable'); END;
CREATE TRIGGER native_operation_receipt_no_delete BEFORE DELETE ON native_operation_receipts BEGIN SELECT RAISE(ABORT,'native suboperation receipt permanent'); END;
CREATE TRIGGER native_operation_terminal_once BEFORE INSERT ON native_operation_receipts
WHEN EXISTS(SELECT 1 FROM native_operation_receipts WHERE claim_id=NEW.claim_id AND kind IN ('succeeded','failed','unknown'))
BEGIN SELECT RAISE(ABORT,'native terminal result permanent'); END;
CREATE TRIGGER native_operation_exact_accepted BEFORE INSERT ON native_operation_receipts
WHEN NEW.kind IN ('succeeded','failed') AND NOT EXISTS(SELECT 1 FROM native_operation_receipts WHERE claim_id=NEW.claim_id AND kind='accepted' AND operation_id=NEW.operation_id)
BEGIN SELECT RAISE(ABORT,'native terminal result requires exact accepted operation'); END;
CREATE TABLE native_operation_quotas (
 create_claim_id TEXT PRIMARY KEY NOT NULL REFERENCES native_operation_intents(claim_id),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER NOT NULL UNIQUE REFERENCES audit(sequence)
) STRICT;
CREATE TRIGGER native_operation_quota_no_update BEFORE UPDATE ON native_operation_quotas BEGIN SELECT RAISE(ABORT,'native stopped-volume quota evidence immutable'); END;
CREATE TRIGGER native_operation_quota_no_delete BEFORE DELETE ON native_operation_quotas BEGIN SELECT RAISE(ABORT,'native quota evidence permanent'); END;
CREATE TRIGGER native_steps_outcome BEFORE UPDATE OF state ON dispatches
WHEN EXISTS(SELECT 1 FROM native_executions WHERE permit_id=NEW.permit_id) AND NEW.state IN ('submitted','confirmed')
BEGIN SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM native_operation_intents i JOIN native_operation_receipts r ON r.claim_id=i.claim_id WHERE i.permit_id=NEW.permit_id AND i.step='start' AND r.operation_id=NEW.operation_id AND ((NEW.state='submitted' AND r.kind='accepted') OR (NEW.state='confirmed' AND r.kind='succeeded' AND json_extract(r.payload,'$.record.resource_id')=NEW.resource_id)) AND NOT EXISTS(SELECT 1 FROM native_operation_receipts bad WHERE bad.claim_id=i.claim_id AND bad.kind IN ('failed','unknown'))) THEN RAISE(ABORT,'native start receipt required') END; END;
PRAGMA user_version=21;
`

// validateNativeOperationParent validates the still-charged, same-epoch parent
// before granting a new step. An accepted parent consumption alone does not
// prove any physical effect or permit an unknown dispatch to advance.
func validateNativeOperationParent(tx *sql.Tx, owner NativeOwnerAuthority, permitID, envelopeDigest, parentClaimID string, now time.Time) (NativeExecutionEnvelope, error) {
	envelope, digest, e := nativeEnvelopeRecord(tx, permitID)
	if e != nil || digest != envelopeDigest || !bindingFingerprint(parentClaimID) {
		return NativeExecutionEnvelope{}, ErrNativeOperation
	}
	binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
	if e != nil || owner.leaf != binding.Spec.OwnerClientLeaf || !now.Before(owner.expires) {
		return NativeExecutionEnvelope{}, ErrNativeOperation
	}
	var actual, leaf string
	if e := tx.QueryRow("SELECT claim_id,owner_leaf FROM native_execution_consumptions WHERE permit_id=?", permitID).Scan(&actual, &leaf); e != nil || actual != parentClaimID || leaf != owner.leaf {
		return NativeExecutionEnvelope{}, ErrNativeOperation
	}
	plan, e := executionPlanRecord(tx, envelope.RequestKey)
	if e != nil || !nativeEnvelopeMatchesPlan(envelope, plan, binding) {
		return NativeExecutionEnvelope{}, ErrNativeOperation
	}
	if e := validateNativeDispatch(tx, plan, permitID, binding, now); e != nil {
		return NativeExecutionEnvelope{}, e
	}
	_, _, epoch, _, e := sqliteMetadata(tx)
	if e != nil || epoch != envelope.ReconciliationEpoch {
		return NativeExecutionEnvelope{}, ErrFence
	}
	return envelope, nil
}

// ClaimNativeCreate persists the single stopped-create dispatch BEFORE the
// native HTTP request. A repeat cannot retrieve the old fresh receipt.
func (s *SQLiteStore) ClaimNativeCreate(ctx context.Context, owner NativeOwnerAuthority, permitID, envelopeDigest, parentClaimID string) (NativeOperationClaim, error) {
	var result NativeOperationClaim
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		envelope, e := validateNativeOperationParent(tx, owner, permitID, envelopeDigest, parentClaimID, s.now())
		if e != nil {
			return e
		}
		result, e = insertNativeOperationIntent(tx, permitID, envelopeDigest, parentClaimID, NativeCreateStopped, envelope.BodyDigest, s.now().UTC())
		return e
	})
	if e != nil {
		return NativeOperationClaim{}, e
	}
	return result, nil
}
func insertNativeOperationIntent(tx *sql.Tx, permitID, envelopeDigest, parentClaimID, step, requestDigest string, now time.Time) (NativeOperationClaim, error) {
	var seen bool
	if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM native_operation_intents WHERE permit_id=? AND step=?)", permitID, step).Scan(&seen); e != nil {
		return NativeOperationClaim{}, e
	}
	if seen {
		return NativeOperationClaim{}, ErrNativeOperationClaimed
	}
	key := permitID + ":" + step
	if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_operation_claimed' AND request_key=?)", key).Scan(&seen); e != nil {
		return NativeOperationClaim{}, e
	}
	if seen {
		return NativeOperationClaim{}, ErrNativeOperationClaimed
	}
	var nonce [32]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return NativeOperationClaim{}, e
	}
	claim := NativeOperationClaim{ID: hex.EncodeToString(nonce[:]), ParentClaimID: parentClaimID, PermitID: permitID, EnvelopeDigest: envelopeDigest, Step: step, RequestDigest: requestDigest, ClaimedAt: now}
	raw, hash, e := nativeJSON(claim)
	if e != nil {
		return NativeOperationClaim{}, e
	}
	seq, e := nativeAuditSequence(tx, "native_operation_claimed", key, now)
	if e != nil {
		return NativeOperationClaim{}, e
	}
	if _, e := tx.Exec("INSERT INTO native_operation_intents(permit_id,step,claim_id,payload,payload_hash,record_sequence) VALUES(?,?,?,?,?,?)", permitID, step, claim.ID, raw, hash, seq); e != nil {
		return NativeOperationClaim{}, e
	}
	return claim, nil
}

type NativeOperationReceipt struct {
	Record          nativeidentity.OperationRecord `json:"record"`
	OwnerServerLeaf string                         `json:"owner_server_leaf"`
	ObservedAt      time.Time                      `json:"observed_at"`
}
type NativeQuotaReceipt struct {
	Record          nativeidentity.QuotaRecord `json:"record"`
	OwnerServerLeaf string                     `json:"owner_server_leaf"`
	ObservedAt      time.Time                  `json:"observed_at"`
}

func nativeOperationRecord(tx *sql.Tx, id string) (NativeOperationClaim, error) {
	var c NativeOperationClaim
	var raw, hash, permit, step string
	var seq int64
	if e := tx.QueryRow("SELECT permit_id,step,payload,payload_hash,record_sequence FROM native_operation_intents WHERE claim_id=?", id).Scan(&permit, &step, &raw, &hash, &seq); e != nil {
		return c, e
	}
	if json.Unmarshal([]byte(raw), &c) != nil || c.ID != id || c.PermitID != permit || c.Step != step || !bindingFingerprint(c.ID) || !bindingFingerprint(c.ParentClaimID) || !bindingFingerprint(c.EnvelopeDigest) || !bindingFingerprint(c.RequestDigest) || c.ClaimedAt.IsZero() || (step != NativeCreateStopped && step != NativeStart) {
		return NativeOperationClaim{}, ErrNativeOperation
	}
	canonical, digest, e := nativeJSON(c)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_operation_claimed", permit+":"+step) {
		return NativeOperationClaim{}, ErrNativeOperation
	}
	envelope, eh, e := nativeEnvelopeRecord(tx, permit)
	if e != nil || eh != c.EnvelopeDigest {
		return NativeOperationClaim{}, ErrNativeOperation
	}
	var parent string
	if tx.QueryRow("SELECT claim_id FROM native_execution_consumptions WHERE permit_id=?", permit).Scan(&parent) != nil || parent != c.ParentClaimID {
		return NativeOperationClaim{}, ErrNativeOperation
	}
	expected := envelope.BodyDigest
	if step == NativeStart {
		expected = nativeidentity.NativeStartDigest()
	}
	if c.RequestDigest != expected {
		return NativeOperationClaim{}, ErrNativeOperation
	}
	return c, nil
}
func nativeOperationByStep(tx *sql.Tx, permit, step string) (NativeOperationClaim, error) {
	var id string
	if e := tx.QueryRow("SELECT claim_id FROM native_operation_intents WHERE permit_id=? AND step=?", permit, step).Scan(&id); e != nil {
		return NativeOperationClaim{}, e
	}
	return nativeOperationRecord(tx, id)
}
func nativeReceiptRecord(tx *sql.Tx, id, kind string) (NativeOperationReceipt, error) {
	var r NativeOperationReceipt
	var raw, hash, op, domain string
	var seq int64
	if e := tx.QueryRow("SELECT operation_id,domain_id,payload,payload_hash,record_sequence FROM native_operation_receipts WHERE claim_id=? AND kind=?", id, kind).Scan(&op, &domain, &raw, &hash, &seq); e != nil {
		return r, e
	}
	if json.Unmarshal([]byte(raw), &r) != nil || !r.Record.Valid() || r.Record.StepClaimID != id || r.Record.Outcome != kind || r.Record.OperationID != op || r.Record.DomainID != domain || r.ObservedAt.IsZero() {
		return NativeOperationReceipt{}, ErrNativeOperation
	}
	canonical, digest, e := nativeJSON(r)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_operation_recorded", id+":"+kind) {
		return NativeOperationReceipt{}, ErrNativeOperation
	}
	claim, e := nativeOperationRecord(tx, id)
	if e != nil {
		return NativeOperationReceipt{}, e
	}
	envelope, _, e := nativeEnvelopeRecord(tx, claim.PermitID)
	if e != nil {
		return NativeOperationReceipt{}, e
	}
	binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
	if e != nil {
		return NativeOperationReceipt{}, e
	}
	if !nativeReceiptMatches(r.Record, claim, envelope) || r.OwnerServerLeaf != binding.Spec.OwnerServerLeaf || r.ObservedAt.Before(claim.ClaimedAt) {
		return NativeOperationReceipt{}, ErrNativeOperation
	}
	return r, nil
}
func nativeReceiptMatches(r nativeidentity.OperationRecord, c NativeOperationClaim, e NativeExecutionEnvelope) bool {
	return r.PermitID == c.PermitID && r.EnvelopeDigest == c.EnvelopeDigest && r.ParentClaimID == c.ParentClaimID && r.StepClaimID == c.ID && r.Step == c.Step && r.DomainID == e.DomainID && r.PolicyDigest == e.PolicyDigest && r.NativeName == e.NativeName
}
func nativeEvidenceFresh(tx *sql.Tx, at, now time.Time) bool {
	config, _, _, _, e := sqliteMetadata(tx)
	return e == nil && !at.IsZero() && !at.After(now) && now.Sub(at) <= config.MaxObservationAge
}

// RecordNativeOperation accepts only a controller-origin live pinned read, not
// an owner-posted success flag. Recording evidence never returns dispatch authority.
func (s *SQLiteStore) RecordNativeOperation(ctx context.Context, observation nativeidentity.OperationObservation) error {
	if !observation.Valid() {
		return ErrNativeOperation
	}
	r := observation.Record()
	return s.transaction(ctx, func(tx *sql.Tx) error {
		claim, e := nativeOperationRecord(tx, r.StepClaimID)
		if e != nil {
			return e
		}
		envelope, _, e := nativeEnvelopeRecord(tx, claim.PermitID)
		if e != nil {
			return e
		}
		binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
		if e != nil {
			return e
		}
		if observation.OwnerServerLeaf() != binding.Spec.OwnerServerLeaf || !nativeReceiptMatches(r, claim, envelope) || observation.ObservedAt().Before(claim.ClaimedAt) || !nativeEvidenceFresh(tx, observation.ObservedAt(), s.now()) {
			return ErrNativeOperation
		}
		old, e := nativeReceiptRecord(tx, claim.ID, r.Outcome)
		if e == nil {
			if old.Record != r {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var historical bool
		if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_operation_recorded' AND request_key=?)", claim.ID+":"+r.Outcome).Scan(&historical); e != nil {
			return e
		}
		if historical {
			return ErrNativeOperation
		}
		if r.Outcome != "accepted" {
			accepted, e := nativeReceiptRecord(tx, claim.ID, "accepted")
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e == nil && (observation.ObservedAt().Before(accepted.ObservedAt) || r.OperationID != accepted.Record.OperationID || (accepted.Record.ResourceID != "" && r.ResourceID != accepted.Record.ResourceID)) {
				return ErrNativeOperation
			}
			if (r.Outcome == "succeeded" || r.Outcome == "failed") && e != nil {
				return ErrNativeOperation
			}
		}
		if r.Step == NativeStart && r.Outcome == "succeeded" {
			create, e := nativeOperationByStep(tx, claim.PermitID, NativeCreateStopped)
			if e != nil {
				return e
			}
			receipt, e := nativeReceiptRecord(tx, create.ID, "succeeded")
			if e != nil || receipt.Record.ResourceID != r.ResourceID {
				return ErrNativeOperation
			}
		}
		receipt := NativeOperationReceipt{Record: r, OwnerServerLeaf: observation.OwnerServerLeaf(), ObservedAt: observation.ObservedAt()}
		raw, hash, e := nativeJSON(receipt)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_operation_recorded", claim.ID+":"+r.Outcome, s.now())
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_operation_receipts(claim_id,kind,operation_id,domain_id,payload,payload_hash,record_sequence) VALUES(?,?,?,?,?,?,?)", claim.ID, r.Outcome, r.OperationID, r.DomainID, raw, hash, seq)
		return e
	})
}
func nativeQuotaRecord(tx *sql.Tx, id string) (NativeQuotaReceipt, error) {
	var r NativeQuotaReceipt
	var raw, hash string
	var seq int64
	if e := tx.QueryRow("SELECT payload,payload_hash,record_sequence FROM native_operation_quotas WHERE create_claim_id=?", id).Scan(&raw, &hash, &seq); e != nil {
		return r, e
	}
	if json.Unmarshal([]byte(raw), &r) != nil || !r.Record.Valid() || r.Record.CreateStepClaimID != id || r.ObservedAt.IsZero() {
		return NativeQuotaReceipt{}, ErrNativeOperation
	}
	canonical, digest, e := nativeJSON(r)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_quota_recorded", id) {
		return NativeQuotaReceipt{}, ErrNativeOperation
	}
	claim, e := nativeOperationRecord(tx, id)
	if e != nil {
		return NativeQuotaReceipt{}, e
	}
	envelope, _, e := nativeEnvelopeRecord(tx, claim.PermitID)
	if e != nil {
		return NativeQuotaReceipt{}, e
	}
	binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
	if e != nil {
		return NativeQuotaReceipt{}, e
	}
	receipt, e := nativeReceiptRecord(tx, id, "succeeded")
	if e != nil {
		return NativeQuotaReceipt{}, e
	}
	if !nativeQuotaMatches(r.Record, claim, envelope, receipt.Record) || r.OwnerServerLeaf != binding.Spec.OwnerServerLeaf || r.ObservedAt.Before(receipt.ObservedAt) {
		return NativeQuotaReceipt{}, ErrNativeOperation
	}
	return r, nil
}
func nativeQuotaMatches(r nativeidentity.QuotaRecord, c NativeOperationClaim, e NativeExecutionEnvelope, receipt nativeidentity.OperationRecord) bool {
	return c.Step == NativeCreateStopped && r.PermitID == c.PermitID && r.EnvelopeDigest == c.EnvelopeDigest && r.ParentClaimID == c.ParentClaimID && r.CreateStepClaimID == c.ID && r.DomainID == e.DomainID && r.PolicyDigest == e.PolicyDigest && r.NativeName == e.NativeName && r.ResourceID == receipt.ResourceID && r.CreateOperationID == receipt.OperationID && e.Resources.Disk > 0 && e.Resources.Disk <= (1<<63-1)/(1<<20) && r.RequestedBytes == e.Resources.Disk*(1<<20) && r.EnforcedBytes == r.RequestedBytes && r.Backend == "zfs" && r.InstanceState == "stopped"
}
func (s *SQLiteStore) RecordNativeQuota(ctx context.Context, observation nativeidentity.QuotaObservation) error {
	if !observation.Valid() {
		return ErrNativeOperation
	}
	r := observation.Record()
	return s.transaction(ctx, func(tx *sql.Tx) error {
		claim, e := nativeOperationRecord(tx, r.CreateStepClaimID)
		if e != nil {
			return e
		}
		envelope, _, e := nativeEnvelopeRecord(tx, claim.PermitID)
		if e != nil {
			return e
		}
		binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
		if e != nil {
			return e
		}
		receipt, e := nativeReceiptRecord(tx, claim.ID, "succeeded")
		if e != nil {
			return e
		}
		if observation.OwnerServerLeaf() != binding.Spec.OwnerServerLeaf || !nativeQuotaMatches(r, claim, envelope, receipt.Record) || observation.ObservedAt().Before(receipt.ObservedAt) || !nativeEvidenceFresh(tx, observation.ObservedAt(), s.now()) {
			return ErrNativeOperation
		}
		old, e := nativeQuotaRecord(tx, claim.ID)
		if e == nil {
			if old.Record != r {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var historical bool
		if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_quota_recorded' AND request_key=?)", claim.ID).Scan(&historical); e != nil {
			return e
		}
		if historical {
			return ErrNativeOperation
		}
		out := NativeQuotaReceipt{Record: r, OwnerServerLeaf: observation.OwnerServerLeaf(), ObservedAt: observation.ObservedAt()}
		raw, hash, e := nativeJSON(out)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_quota_recorded", claim.ID, s.now())
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_operation_quotas(create_claim_id,payload,payload_hash,record_sequence) VALUES(?,?,?,?)", claim.ID, raw, hash, seq)
		return e
	})
}
func (s *SQLiteStore) ClaimNativeStart(ctx context.Context, owner NativeOwnerAuthority, permitID, envelopeDigest, parentClaimID string) (NativeOperationClaim, error) {
	var out NativeOperationClaim
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := validateNativeOperationParent(tx, owner, permitID, envelopeDigest, parentClaimID, s.now())
		if e != nil {
			return e
		}
		create, e := nativeOperationByStep(tx, permitID, NativeCreateStopped)
		if e != nil {
			return e
		}
		quota, e := nativeQuotaRecord(tx, create.ID)
		if e != nil {
			return e
		}
		if !nativeEvidenceFresh(tx, quota.ObservedAt, s.now()) {
			return ErrNativeOperation
		}
		out, e = insertNativeOperationIntent(tx, permitID, envelopeDigest, parentClaimID, NativeStart, nativeidentity.NativeStartDigest(), s.now().UTC())
		return e
	})
	if e != nil {
		return NativeOperationClaim{}, e
	}
	return out, nil
}

func (s *SQLiteStore) checkNativeOperations(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		var missing bool
		if e := tx.QueryRow(`SELECT EXISTS(
 SELECT 1 FROM audit a LEFT JOIN native_operation_intents i ON i.record_sequence=a.sequence AND i.permit_id||':'||i.step=a.request_key WHERE a.kind='native_operation_claimed' AND i.claim_id IS NULL
 UNION ALL SELECT 1 FROM audit a LEFT JOIN native_operation_receipts r ON r.record_sequence=a.sequence AND r.claim_id||':'||r.kind=a.request_key WHERE a.kind='native_operation_recorded' AND r.claim_id IS NULL
 UNION ALL SELECT 1 FROM audit a LEFT JOIN native_operation_quotas q ON q.record_sequence=a.sequence AND q.create_claim_id=a.request_key WHERE a.kind='native_quota_recorded' AND q.create_claim_id IS NULL
 UNION ALL SELECT 1 FROM native_operation_receipts r LEFT JOIN native_operation_intents i ON i.claim_id=r.claim_id WHERE i.claim_id IS NULL
 UNION ALL SELECT 1 FROM native_operation_quotas q LEFT JOIN native_operation_intents i ON i.claim_id=q.create_claim_id WHERE i.claim_id IS NULL)`).Scan(&missing); e != nil {
			return e
		}
		if missing {
			return ErrNativeOperation
		}
		rows, e := tx.Query("SELECT claim_id FROM native_operation_intents")
		if e != nil {
			return e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			claim, e := nativeOperationRecord(tx, id)
			if e != nil {
				return e
			}
			var consumedAt string
			if e := tx.QueryRow("SELECT consumed_at FROM native_execution_consumptions WHERE permit_id=?", claim.PermitID).Scan(&consumedAt); e != nil {
				return e
			}
			at, e := time.Parse(time.RFC3339Nano, consumedAt)
			if e != nil || claim.ClaimedAt.Before(at) {
				return ErrNativeOperation
			}
			accepted, ae := nativeReceiptRecord(tx, id, "accepted")
			if ae != nil && !errors.Is(ae, sql.ErrNoRows) {
				return ae
			}
			terminals := 0
			for _, kind := range []string{"succeeded", "failed", "unknown"} {
				r, e := nativeReceiptRecord(tx, id, kind)
				if errors.Is(e, sql.ErrNoRows) {
					continue
				}
				if e != nil {
					return e
				}
				terminals++
				if kind != "unknown" && ae != nil {
					return ErrNativeOperation
				}
				if ae == nil && (r.Record.OperationID != accepted.Record.OperationID || r.ObservedAt.Before(accepted.ObservedAt)) {
					return ErrNativeOperation
				}
			}
			if terminals > 1 {
				return ErrNativeOperation
			}
			if claim.Step == NativeStart {
				create, e := nativeOperationByStep(tx, claim.PermitID, NativeCreateStopped)
				if e != nil {
					return e
				}
				quota, e := nativeQuotaRecord(tx, create.ID)
				if e != nil || claim.ClaimedAt.Before(quota.ObservedAt) {
					return ErrNativeOperation
				}
				start, se := nativeReceiptRecord(tx, id, "succeeded")
				if se != nil && !errors.Is(se, sql.ErrNoRows) {
					return se
				}
				if se == nil && start.Record.ResourceID != quota.Record.ResourceID {
					return ErrNativeOperation
				}
			}
			quota, e := nativeQuotaRecord(tx, id)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e == nil && quota.Record.CreateStepClaimID != id {
				return ErrNativeOperation
			}
		}
		return nil
	})
}
