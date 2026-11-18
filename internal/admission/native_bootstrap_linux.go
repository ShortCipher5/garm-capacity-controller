package admission

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
)

var ErrNativeBootstrap = errors.New("canonical protected bootstrap required; retain charge")
var ErrNativeBootstrapClaimed = errors.New("bootstrap dispatch already claimed; no retry")

type NativeBootstrapPlan struct {
	PermitID          string `json:"permit_id"`
	EnvelopeDigest    string `json:"envelope_digest"`
	ParentClaimID     string `json:"parent_claim_id"`
	CreateStepClaimID string `json:"create_step_claim_id"`
	CreateOperationID string `json:"create_operation_id"`
	DomainID          string `json:"domain_id"`
	PolicyDigest      string `json:"policy_digest"`
	NativeName        string `json:"native_name"`
	ResourceID        string `json:"resource_id"`
	ImageFingerprint  string `json:"image_fingerprint"`
	LaunchSHA256      string `json:"launch_sha256"`
	LaunchBytes       int64  `json:"launch_bytes"`
	JITSHA256         string `json:"jit_sha256"`
	JITBytes          int64  `json:"jit_bytes"`
}
type NativeBootstrapClaim struct {
	ID         string              `json:"id"`
	PlanDigest string              `json:"plan_digest"`
	Plan       NativeBootstrapPlan `json:"plan"`
	ClaimedAt  time.Time           `json:"claimed_at"`
}
type NativeBootstrapReceipt struct {
	Record          nativeidentity.BootstrapRecord `json:"record"`
	OwnerServerLeaf string                         `json:"owner_server_leaf"`
	ObservedAt      time.Time                      `json:"observed_at"`
}

const sqliteNativeBootstrapSchema = `
CREATE TABLE native_bootstrap_bindings (
 permit_id TEXT PRIMARY KEY NOT NULL REFERENCES native_execution_consumptions(permit_id),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TABLE native_bootstrap_intents (
 permit_id TEXT PRIMARY KEY NOT NULL REFERENCES native_bootstrap_bindings(permit_id),
 claim_id TEXT UNIQUE NOT NULL CHECK(length(claim_id)=64),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TABLE native_bootstrap_receipts (
 permit_id TEXT PRIMARY KEY NOT NULL REFERENCES native_bootstrap_intents(permit_id),
 outcome TEXT NOT NULL CHECK(outcome IN ('verified','unknown')),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TRIGGER native_bootstrap_binding_immutable BEFORE UPDATE ON native_bootstrap_bindings BEGIN SELECT RAISE(ABORT,'bootstrap binding immutable'); END;
CREATE TRIGGER native_bootstrap_binding_permanent BEFORE DELETE ON native_bootstrap_bindings BEGIN SELECT RAISE(ABORT,'bootstrap binding permanent'); END;
CREATE TRIGGER native_bootstrap_intent_immutable BEFORE UPDATE ON native_bootstrap_intents BEGIN SELECT RAISE(ABORT,'bootstrap intent immutable'); END;
CREATE TRIGGER native_bootstrap_intent_permanent BEFORE DELETE ON native_bootstrap_intents BEGIN SELECT RAISE(ABORT,'bootstrap intent permanent'); END;
CREATE TRIGGER native_bootstrap_receipt_immutable BEFORE UPDATE ON native_bootstrap_receipts BEGIN SELECT RAISE(ABORT,'bootstrap receipt immutable'); END;
CREATE TRIGGER native_bootstrap_receipt_permanent BEFORE DELETE ON native_bootstrap_receipts BEGIN SELECT RAISE(ABORT,'bootstrap receipt permanent'); END;
CREATE TRIGGER native_start_requires_bootstrap BEFORE INSERT ON native_operation_intents
WHEN NEW.step='start' AND NOT EXISTS(SELECT 1 FROM native_bootstrap_receipts WHERE permit_id=NEW.permit_id AND outcome='verified')
BEGIN SELECT RAISE(ABORT,'verified protected bootstrap required before start'); END;
PRAGMA user_version=22;
`

func nativeBootstrapPlan(tx *sql.Tx, permit string) (NativeBootstrapPlan, string, error) {
	var p NativeBootstrapPlan
	var raw, hash string
	var seq int64
	if e := tx.QueryRow("SELECT payload,payload_hash,record_sequence FROM native_bootstrap_bindings WHERE permit_id=?", permit).Scan(&raw, &hash, &seq); e != nil {
		return p, "", e
	}
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &p) != nil || p.PermitID != permit || !bindingFingerprint(p.LaunchSHA256) || !bindingFingerprint(p.JITSHA256) || p.LaunchBytes <= 0 || p.LaunchBytes > 1<<20 || p.JITBytes <= 0 || p.JITBytes > 1<<20 {
		return p, "", ErrNativeBootstrap
	}
	canonical, digest, e := nativeJSON(p)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_bootstrap_bound", permit) {
		return p, "", ErrNativeBootstrap
	}
	envelope, eh, e := nativeEnvelopeRecord(tx, permit)
	if e != nil {
		return p, "", e
	}
	create, e := nativeOperationByStep(tx, permit, NativeCreateStopped)
	if e != nil {
		return p, "", e
	}
	receipt, e := nativeReceiptRecord(tx, create.ID, "succeeded")
	if e != nil {
		return p, "", e
	}
	if p.EnvelopeDigest != eh || p.ParentClaimID != create.ParentClaimID || p.CreateStepClaimID != create.ID || p.CreateOperationID != receipt.Record.OperationID || p.ResourceID != receipt.Record.ResourceID || p.DomainID != envelope.DomainID || p.PolicyDigest != envelope.PolicyDigest || p.NativeName != envelope.NativeName || p.ImageFingerprint != envelope.Execution.Fingerprint {
		return p, "", ErrNativeBootstrap
	}
	return p, digest, nil
}

// BindNativeBootstrap verifies the original canonical native body, then derives
// exactly two files from its typed secret payload and the recorded CREATE UUID.
// Neither raw JIT nor manifest bytes enter the ledger, audit or error strings.
func (s *SQLiteStore) BindNativeBootstrap(ctx context.Context, permit, userData string, policy nativeapi.Policy) (NativeBootstrapPlan, error) {
	payload, e := protectedboot.Decode([]byte(userData))
	if e != nil {
		return NativeBootstrapPlan{}, ErrNativeBootstrap
	}
	var out NativeBootstrapPlan
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		envelope, eh, e := nativeEnvelopeRecord(tx, permit)
		if e != nil {
			return e
		}
		binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
		if e != nil {
			return e
		}
		execution, e := executionPlanRecord(tx, envelope.RequestKey)
		if e != nil {
			return e
		}
		if e = validateNativeDispatch(tx, execution, permit, binding, s.now()); e != nil {
			return e
		}
		_, _, epoch, _, e := sqliteMetadata(tx)
		if e != nil || epoch != envelope.ReconciliationEpoch {
			return ErrFence
		}
		image, ok := policy.ProtectedImage(envelope.Pool)
		if !ok || !reflect.DeepEqual(image, payload.Image) || policy.Digest() != envelope.PolicyDigest || policy.DomainID() != envelope.DomainID || payload.RunnerName != envelope.NativeName {
			return ErrNativeBootstrap
		}
		v := envelope.Execution
		r := envelope.Resources
		compiled, e := policy.Compile(nativeapi.Allocation{Controller: envelope.Controller, Host: envelope.Host, Pool: envelope.Pool, Name: envelope.NativeName, RequestKey: envelope.RequestKey, CPU: r.CPU, RAM: r.RAM, Disk: r.Disk, Runners: r.Runners, Flavor: v.Flavor, Image: v.Image, Fingerprint: v.Fingerprint, Type: v.Type, Arch: v.Arch, Profiles: v.Profiles}, userData)
		if e != nil || compiled.Digest() != envelope.BodyDigest {
			return ErrNativeBootstrap
		}
		create, e := nativeOperationByStep(tx, permit, NativeCreateStopped)
		if e != nil {
			return e
		}
		receipt, e := nativeReceiptRecord(tx, create.ID, "succeeded")
		if e != nil {
			return e
		}
		files, e := protectedboot.Materialize(payload, protectedboot.Binding{DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest, ResourceID: receipt.Record.ResourceID, ImageFingerprint: envelope.Execution.Fingerprint})
		if e != nil {
			return ErrNativeBootstrap
		}
		out = NativeBootstrapPlan{permit, eh, create.ParentClaimID, create.ID, receipt.Record.OperationID, envelope.DomainID, envelope.PolicyDigest, envelope.NativeName, receipt.Record.ResourceID, envelope.Execution.Fingerprint, files[0].SHA256, files[0].Bytes, files[1].SHA256, files[1].Bytes}
		old, _, e := nativeBootstrapPlan(tx, permit)
		if e == nil {
			if old != out {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var historical bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_bootstrap_bound' AND request_key=?)", permit).Scan(&historical); e != nil {
			return e
		}
		if historical {
			return ErrNativeBootstrap
		}
		raw, hash, e := nativeJSON(out)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_bootstrap_bound", permit, s.now())
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_bootstrap_bindings(permit_id,payload,payload_hash,record_sequence)VALUES(?,?,?,?)", permit, raw, hash, seq)
		return e
	})
	if e != nil {
		return NativeBootstrapPlan{}, e
	}
	return out, nil
}
func nativeBootstrapClaim(tx *sql.Tx, permit string) (NativeBootstrapClaim, error) {
	var c NativeBootstrapClaim
	var raw, hash, id string
	var seq int64
	if e := tx.QueryRow("SELECT claim_id,payload,payload_hash,record_sequence FROM native_bootstrap_intents WHERE permit_id=?", permit).Scan(&id, &raw, &hash, &seq); e != nil {
		return c, e
	}
	if json.Unmarshal([]byte(raw), &c) != nil || c.ID != id || !bindingFingerprint(id) || c.ClaimedAt.IsZero() {
		return c, ErrNativeBootstrap
	}
	canonical, digest, e := nativeJSON(c)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_bootstrap_claimed", permit) {
		return c, ErrNativeBootstrap
	}
	p, ph, e := nativeBootstrapPlan(tx, permit)
	if e != nil || c.Plan != p || c.PlanDigest != ph {
		return c, ErrNativeBootstrap
	}
	created, e := nativeReceiptRecord(tx, p.CreateStepClaimID, "succeeded")
	if e != nil || c.ClaimedAt.Before(created.ObservedAt) {
		return c, ErrNativeBootstrap
	}
	return c, nil
}
func (s *SQLiteStore) ClaimNativeBootstrap(ctx context.Context, owner NativeOwnerAuthority, permit, envelopeDigest, parentClaimID string) (NativeBootstrapClaim, error) {
	var out NativeBootstrapClaim
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := validateNativeOperationParent(tx, owner, permit, envelopeDigest, parentClaimID, s.now()); e != nil {
			return e
		}
		p, hash, e := nativeBootstrapPlan(tx, permit)
		if e != nil {
			return e
		}
		if _, e := nativeBootstrapClaim(tx, permit); e == nil {
			return ErrNativeBootstrapClaimed
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var seen bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_bootstrap_claimed' AND request_key=?)", permit).Scan(&seen); e != nil {
			return e
		}
		if seen {
			return ErrNativeBootstrapClaimed
		}
		var nonce [32]byte
		if _, e = rand.Read(nonce[:]); e != nil {
			return e
		}
		out = NativeBootstrapClaim{hex.EncodeToString(nonce[:]), hash, p, s.now().UTC()}
		raw, digest, e := nativeJSON(out)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_bootstrap_claimed", permit, s.now())
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_bootstrap_intents(permit_id,claim_id,payload,payload_hash,record_sequence)VALUES(?,?,?,?,?)", permit, out.ID, raw, digest, seq)
		return e
	})
	if e != nil {
		return NativeBootstrapClaim{}, e
	}
	return out, nil
}
func bootstrapMatches(r nativeidentity.BootstrapRecord, c NativeBootstrapClaim) bool {
	p := c.Plan
	return r.ClaimID == c.ID && r.PlanDigest == c.PlanDigest && r.PermitID == p.PermitID && r.EnvelopeDigest == p.EnvelopeDigest && r.ParentClaimID == p.ParentClaimID && r.DomainID == p.DomainID && r.PolicyDigest == p.PolicyDigest && r.NativeName == p.NativeName && r.ResourceID == p.ResourceID && r.ImageFingerprint == p.ImageFingerprint && r.LaunchSHA256 == p.LaunchSHA256 && r.LaunchBytes == p.LaunchBytes && r.JITSHA256 == p.JITSHA256 && r.JITBytes == p.JITBytes
}
func nativeBootstrapReceipt(tx *sql.Tx, permit string) (NativeBootstrapReceipt, error) {
	var r NativeBootstrapReceipt
	var raw, hash, outcome string
	var seq int64
	if e := tx.QueryRow("SELECT outcome,payload,payload_hash,record_sequence FROM native_bootstrap_receipts WHERE permit_id=?", permit).Scan(&outcome, &raw, &hash, &seq); e != nil {
		return r, e
	}
	if json.Unmarshal([]byte(raw), &r) != nil || !r.Record.Valid() || r.Record.Outcome != outcome || r.ObservedAt.IsZero() {
		return r, ErrNativeBootstrap
	}
	canonical, digest, e := nativeJSON(r)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_bootstrap_recorded", permit) {
		return r, ErrNativeBootstrap
	}
	c, e := nativeBootstrapClaim(tx, permit)
	if e != nil || !bootstrapMatches(r.Record, c) || r.ObservedAt.Before(c.ClaimedAt) {
		return r, ErrNativeBootstrap
	}
	envelope, _, e := nativeEnvelopeRecord(tx, permit)
	if e != nil {
		return r, e
	}
	binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
	if e != nil || r.OwnerServerLeaf != binding.Spec.OwnerServerLeaf {
		return r, ErrNativeBootstrap
	}
	return r, nil
}
func (s *SQLiteStore) RecordNativeBootstrap(ctx context.Context, observation nativeidentity.BootstrapObservation) error {
	if !observation.Valid() {
		return ErrNativeBootstrap
	}
	r := observation.Record()
	return s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := nativeBootstrapClaim(tx, r.PermitID)
		if e != nil {
			return e
		}
		envelope, _, e := nativeEnvelopeRecord(tx, r.PermitID)
		if e != nil {
			return e
		}
		binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
		if e != nil {
			return e
		}
		if !bootstrapMatches(r, c) || observation.OwnerServerLeaf() != binding.Spec.OwnerServerLeaf || observation.ObservedAt().Before(c.ClaimedAt) || !nativeEvidenceFresh(tx, observation.ObservedAt(), s.now()) {
			return ErrNativeBootstrap
		}
		old, e := nativeBootstrapReceipt(tx, r.PermitID)
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
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_bootstrap_recorded' AND request_key=?)", r.PermitID).Scan(&historical); e != nil {
			return e
		}
		if historical {
			return ErrNativeBootstrap
		}
		receipt := NativeBootstrapReceipt{r, observation.OwnerServerLeaf(), observation.ObservedAt()}
		raw, hash, e := nativeJSON(receipt)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_bootstrap_recorded", r.PermitID, s.now())
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_bootstrap_receipts(permit_id,outcome,payload,payload_hash,record_sequence)VALUES(?,?,?,?,?)", r.PermitID, r.Outcome, raw, hash, seq)
		return e
	})
}
func requireNativeBootstrap(tx *sql.Tx, permit string, now time.Time) error {
	r, e := nativeBootstrapReceipt(tx, permit)
	if e != nil || r.Record.Outcome != "verified" || !nativeEvidenceFresh(tx, r.ObservedAt, now) {
		return ErrNativeBootstrap
	}
	return nil
}
func (s *SQLiteStore) checkNativeBootstraps(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		var migrations int
		if e := tx.QueryRow("SELECT count(*) FROM audit WHERE kind='schema_migrated_v22'").Scan(&migrations); e != nil {
			return e
		}
		if migrations != 1 {
			return ErrNativeBootstrap
		}
		var bad bool
		if e := tx.QueryRow(`SELECT EXISTS(
SELECT 1 FROM audit a LEFT JOIN native_bootstrap_bindings b ON b.record_sequence=a.sequence AND b.permit_id=a.request_key WHERE a.kind='native_bootstrap_bound' AND b.permit_id IS NULL
UNION ALL SELECT 1 FROM audit a LEFT JOIN native_bootstrap_intents i ON i.record_sequence=a.sequence AND i.permit_id=a.request_key WHERE a.kind='native_bootstrap_claimed' AND i.permit_id IS NULL
UNION ALL SELECT 1 FROM audit a LEFT JOIN native_bootstrap_receipts r ON r.record_sequence=a.sequence AND r.permit_id=a.request_key WHERE a.kind='native_bootstrap_recorded' AND r.permit_id IS NULL
UNION ALL SELECT 1 FROM native_bootstrap_intents i LEFT JOIN native_bootstrap_bindings b ON b.permit_id=i.permit_id WHERE b.permit_id IS NULL
UNION ALL SELECT 1 FROM native_bootstrap_receipts r LEFT JOIN native_bootstrap_intents i ON i.permit_id=r.permit_id WHERE i.permit_id IS NULL
UNION ALL SELECT 1 FROM native_operation_intents i LEFT JOIN native_bootstrap_receipts r ON r.permit_id=i.permit_id AND r.outcome='verified' WHERE i.step='start' AND i.record_sequence>(SELECT min(sequence) FROM audit WHERE kind='schema_migrated_v22') AND r.permit_id IS NULL)`).Scan(&bad); e != nil {
			return e
		}
		if bad {
			return ErrNativeBootstrap
		}
		rows, e := tx.Query("SELECT permit_id FROM native_bootstrap_bindings")
		if e != nil {
			return e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
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
			if _, _, e = nativeBootstrapPlan(tx, id); e != nil {
				return e
			}
			if _, e = nativeBootstrapClaim(tx, id); e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if _, e = nativeBootstrapReceipt(tx, id); e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		return nil
	})
}
