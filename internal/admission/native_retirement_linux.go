package admission

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"reflect"
	"time"
)

const NativeRetireStop = "stop"
const NativeRetireDelete = "delete"

type NativeRetirementIntent struct {
	Version                                                                                                                                       string
	PermitID, EnvelopeDigest, RequestKey, DomainID, PolicyDigest, NativeName, ResourceID, Dataset, DatasetGUID, RootFenceDigest, StartOperationID string
	Epoch                                                                                                                                         int64
	RequestedAt                                                                                                                                   time.Time
}
type NativeRetirementClaim struct {
	ID, PermitID, IntentDigest, Step, RequestDigest string
	ClaimedAt                                       time.Time
}
type NativeRetirementReceipt struct {
	Record          nativeidentity.RetirementOperationRecord
	OwnerServerLeaf string
	ObservedAt      time.Time
}

const sqliteNativeRetirementSchema = `
CREATE TABLE native_retirement_intents(permit_id TEXT PRIMARY KEY NOT NULL REFERENCES native_root_fences(permit_id),payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)) STRICT;
CREATE TABLE native_retirement_claims(claim_id TEXT PRIMARY KEY NOT NULL CHECK(length(claim_id)=64),permit_id TEXT NOT NULL REFERENCES native_retirement_intents(permit_id),step TEXT NOT NULL CHECK(step IN ('stop','delete')),payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence),UNIQUE(permit_id,step)) STRICT;
CREATE TABLE native_retirement_receipts(claim_id TEXT NOT NULL REFERENCES native_retirement_claims(claim_id),kind TEXT NOT NULL CHECK(kind IN ('accepted','succeeded','failed','unknown')),operation_id TEXT NOT NULL,domain_id TEXT NOT NULL,payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence),PRIMARY KEY(claim_id,kind)) STRICT;
CREATE UNIQUE INDEX native_retirement_operation_once ON native_retirement_receipts(domain_id,operation_id) WHERE kind='accepted';
CREATE TRIGGER native_retirement_intent_immutable BEFORE UPDATE ON native_retirement_intents BEGIN SELECT RAISE(ABORT,'retirement immutable'); END;
CREATE TRIGGER native_retirement_intent_permanent BEFORE DELETE ON native_retirement_intents BEGIN SELECT RAISE(ABORT,'retirement permanent'); END;
CREATE TRIGGER native_retirement_claim_immutable BEFORE UPDATE ON native_retirement_claims BEGIN SELECT RAISE(ABORT,'claim immutable'); END;
CREATE TRIGGER native_retirement_claim_permanent BEFORE DELETE ON native_retirement_claims BEGIN SELECT RAISE(ABORT,'claim permanent'); END;
CREATE TRIGGER native_retirement_receipt_immutable BEFORE UPDATE ON native_retirement_receipts BEGIN SELECT RAISE(ABORT,'receipt immutable'); END;
CREATE TRIGGER native_retirement_receipt_permanent BEFORE DELETE ON native_retirement_receipts BEGIN SELECT RAISE(ABORT,'receipt permanent'); END;
CREATE TRIGGER native_retirement_terminal_once BEFORE INSERT ON native_retirement_receipts WHEN EXISTS(SELECT 1 FROM native_retirement_receipts WHERE claim_id=NEW.claim_id AND kind IN ('succeeded','failed','unknown')) BEGIN SELECT RAISE(ABORT,'terminal permanent'); END;
CREATE TRIGGER native_retirement_terminal_exact BEFORE INSERT ON native_retirement_receipts WHEN NEW.kind IN ('succeeded','failed') AND NOT EXISTS(SELECT 1 FROM native_retirement_receipts WHERE claim_id=NEW.claim_id AND kind='accepted' AND operation_id=NEW.operation_id) BEGIN SELECT RAISE(ABORT,'accepted operation required'); END;
CREATE TRIGGER native_operation_not_retirement BEFORE INSERT ON native_operation_receipts WHEN NEW.kind='accepted' AND EXISTS(SELECT 1 FROM native_retirement_receipts WHERE domain_id=NEW.domain_id AND operation_id=NEW.operation_id AND kind='accepted') BEGIN SELECT RAISE(ABORT,'operation used by retirement'); END;
CREATE TRIGGER native_retirement_not_create BEFORE INSERT ON native_retirement_receipts WHEN NEW.kind='accepted' AND EXISTS(SELECT 1 FROM native_operation_receipts WHERE domain_id=NEW.domain_id AND operation_id=NEW.operation_id AND kind='accepted') BEGIN SELECT RAISE(ABORT,'operation used by creation'); END;
PRAGMA user_version=25;
`

func nativeRetirementBase(tx *sql.Tx, permit string) (NativeRetirementIntent, error) {
	var v NativeRetirementIntent
	f, e := nativeRootFence(tx, permit)
	if e != nil {
		return v, e
	}
	_, fh, e := nativeJSON(f)
	if e != nil {
		return v, e
	}
	env, eh, e := nativeEnvelopeRecord(tx, permit)
	if e != nil {
		return v, e
	}
	start, e := nativeOperationByStep(tx, permit, NativeStart)
	if e != nil {
		return v, e
	}
	done, e := nativeReceiptRecord(tx, start.ID, "succeeded")
	if e != nil {
		return v, e
	}
	create, e := nativeOperationByStep(tx, permit, NativeCreateStopped)
	if e != nil {
		return v, e
	}
	quota, e := nativeQuotaRecord(tx, create.ID)
	if e != nil {
		return v, e
	}
	return NativeRetirementIntent{Version: "native-retirement/v1", PermitID: permit, EnvelopeDigest: eh, RequestKey: env.RequestKey, DomainID: env.DomainID, PolicyDigest: env.PolicyDigest, NativeName: env.NativeName, ResourceID: f.Journal.ResourceID, Dataset: quota.Record.Dataset, DatasetGUID: quota.Record.DatasetGUID, RootFenceDigest: fh, StartOperationID: done.Record.OperationID, Epoch: env.ReconciliationEpoch}, nil
}
func nativeRetirementIntent(tx *sql.Tx, permit string) (NativeRetirementIntent, string, error) {
	var v NativeRetirementIntent
	var raw, hash string
	var seq int64
	if e := tx.QueryRow("SELECT payload,payload_hash,record_sequence FROM native_retirement_intents WHERE permit_id=?", permit).Scan(&raw, &hash, &seq); e != nil {
		return v, "", e
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return v, "", ErrRetirementAuthority
	}
	canonical, digest, e := nativeJSON(v)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_retirement_requested", permit) {
		return v, "", ErrRetirementAuthority
	}
	expected, e := nativeRetirementBase(tx, permit)
	if e != nil {
		return v, "", e
	}
	expected.RequestedAt = v.RequestedAt
	if v.RequestedAt.IsZero() || !reflect.DeepEqual(expected, v) {
		return v, "", ErrRetirementAuthority
	}
	return v, hash, nil
}
func nativeRetirementCurrent(tx *sql.Tx, v NativeRetirementIntent, now time.Time) error {
	cfg, ready, epoch, _, e := sqliteMetadata(tx)
	if e != nil {
		return e
	}
	if !ready || epoch != v.Epoch || !sqliteObservationsFresh(cfg, now) {
		return ErrFence
	}
	env, _, e := nativeEnvelopeRecord(tx, v.PermitID)
	if e != nil {
		return e
	}
	var sequence int64
	if e = tx.QueryRow("SELECT bound_sequence FROM host_bindings WHERE host=?", env.Host).Scan(&sequence); e != nil || sequence != env.HostBindingSequence {
		return ErrFence
	}
	var state, resource, operation string
	if e = tx.QueryRow("SELECT state,resource_id,operation_id FROM dispatches WHERE permit_id=?", v.PermitID).Scan(&state, &resource, &operation); e != nil || state != "confirmed" || resource != v.ResourceID || operation != v.StartOperationID {
		return ErrRetirementAuthority
	}
	plan, e := executionPlanRecord(tx, v.RequestKey)
	if e != nil {
		return e
	}
	reservations, e := sqliteReservations(tx)
	if e != nil {
		return e
	}
	reservation, ok := reservations[v.RequestKey]
	if !ok || reservation.Request != plan.Request || (reservation.State != "creating" && reservation.State != "idle" && reservation.State != "running") {
		return ErrRetirementAuthority
	}
	if _, e = releaseRecord(tx, v.RequestKey); !errors.Is(e, sql.ErrNoRows) {
		return ErrRetirementAuthority
	}
	return nil
}

// Request is non-destructive and pins current exact root/job/creation lineage.
func (s *SQLiteStore) RequestNativeRetirement(ctx context.Context, permit string, observation nativeidentity.ProtectedJournalObservation, policy nativeapi.Policy) (NativeRetirementIntent, error) {
	var out NativeRetirementIntent
	if !observation.Valid() || observation.Record().PermitID != permit {
		return out, ErrRetirementAuthority
	}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := s.correlateProtectedJournal(tx, observation, policy); e != nil {
			return e
		}
		f, e := nativeRootFence(tx, permit)
		if e != nil || f.Journal != observation.Record() {
			return ErrRetirementAuthority
		}
		v, e := nativeRetirementBase(tx, permit)
		if e != nil {
			return e
		}
		if e = nativeRetirementCurrent(tx, v, s.now()); e != nil {
			return e
		}
		old, _, e := nativeRetirementIntent(tx, permit)
		if e == nil {
			out = old
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var seen bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_retirement_requested' AND request_key=?)", permit).Scan(&seen); e != nil {
			return e
		}
		if seen {
			return ErrRetirementAuthority
		}
		v.RequestedAt = s.now().UTC()
		raw, hash, e := nativeJSON(v)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_retirement_requested", permit, s.now())
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO native_retirement_intents VALUES(?,?,?,?)", permit, raw, hash, seq); e != nil {
			return e
		}
		out = v
		return nil
	})
	return out, e
}

// Claim is implemented for offline protocol integration only. Runtime mode is
// unconditionally refused until genuine owner retirement capability is wired.
// Neither the schema24 record nor a configuration flag can remove that refusal.
func (s *SQLiteStore) ClaimNativeRetirement(ctx context.Context, owner NativeOwnerAuthority, permit, intentDigest, step string) (NativeRetirementClaim, error) {
	var out NativeRetirementClaim
	if s.RuntimeLineageBound() || (step != NativeRetireStop && step != NativeRetireDelete) {
		return out, ErrRetirementAuthority
	}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		v, hash, e := nativeRetirementIntent(tx, permit)
		if e != nil || hash != intentDigest {
			return ErrRetirementAuthority
		}
		if e = nativeRetirementCurrent(tx, v, s.now()); e != nil {
			return e
		}
		env, _, e := nativeEnvelopeRecord(tx, permit)
		if e != nil {
			return e
		}
		domain, e := nativeDomainRecord(tx, env.DomainBindingSequence)
		if e != nil || owner.leaf != domain.Spec.OwnerClientLeaf || !s.now().Before(owner.expires) {
			return ErrRetirementAuthority
		}
		if step == NativeRetireDelete {
			previous, e := nativeRetirementClaimByStep(tx, permit, NativeRetireStop)
			if e != nil {
				return e
			}
			if _, e = nativeRetirementReceipt(tx, previous.ID, "succeeded"); e != nil {
				return e
			}
		}
		var seen bool
		key := permit + ":" + step
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM native_retirement_claims WHERE permit_id=? AND step=?) OR EXISTS(SELECT 1 FROM audit WHERE kind='native_retirement_claimed' AND request_key=?)", permit, step, key).Scan(&seen); e != nil {
			return e
		}
		if seen {
			return ErrNativeOperationClaimed
		}
		var random [32]byte
		if _, e = rand.Read(random[:]); e != nil {
			return e
		}
		requestDigest, e := NativeRetirementRequestDigest(v, step)
		if e != nil {
			return e
		}
		out = NativeRetirementClaim{hex.EncodeToString(random[:]), permit, hash, step, requestDigest, s.now().UTC()}
		raw, digest, e := nativeJSON(out)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_retirement_claimed", key, s.now())
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_retirement_claims VALUES(?,?,?,?,?,?)", out.ID, permit, step, raw, digest, seq)
		return e
	})
	if e != nil {
		return NativeRetirementClaim{}, e
	}
	return out, nil
}
func nativeRetirementClaim(tx *sql.Tx, id string) (NativeRetirementClaim, error) {
	var c NativeRetirementClaim
	var permit, step, raw, hash string
	var seq int64
	if e := tx.QueryRow("SELECT permit_id,step,payload,payload_hash,record_sequence FROM native_retirement_claims WHERE claim_id=?", id).Scan(&permit, &step, &raw, &hash, &seq); e != nil {
		return c, e
	}
	if json.Unmarshal([]byte(raw), &c) != nil {
		return c, ErrRetirementAuthority
	}
	canon, digest, e := nativeJSON(c)
	if e != nil || canon != raw || digest != hash || c.ID != id || !bindingFingerprint(id) || c.PermitID != permit || c.Step != step || (step != NativeRetireStop && step != NativeRetireDelete) || c.ClaimedAt.IsZero() || !nativeAuditMatches(tx, seq, "native_retirement_claimed", permit+":"+step) {
		return c, ErrRetirementAuthority
	}
	v, h, e := nativeRetirementIntent(tx, permit)
	if e != nil || h != c.IntentDigest || c.ClaimedAt.Before(v.RequestedAt) {
		return c, ErrRetirementAuthority
	}
	expectedDigest, e := NativeRetirementRequestDigest(v, step)
	if e != nil || expectedDigest != c.RequestDigest {
		return c, ErrRetirementAuthority
	}
	if step == NativeRetireDelete {
		previous, e := nativeRetirementClaimByStep(tx, permit, NativeRetireStop)
		if e != nil {
			return c, e
		}
		r, e := nativeRetirementReceipt(tx, previous.ID, "succeeded")
		if e != nil || c.ClaimedAt.Before(r.ObservedAt) {
			return c, ErrRetirementAuthority
		}
	}
	return c, nil
}
func nativeRetirementClaimByStep(tx *sql.Tx, permit, step string) (NativeRetirementClaim, error) {
	var id string
	if e := tx.QueryRow("SELECT claim_id FROM native_retirement_claims WHERE permit_id=? AND step=?", permit, step).Scan(&id); e != nil {
		return NativeRetirementClaim{}, e
	}
	return nativeRetirementClaim(tx, id)
}
func nativeRetirementReceiptRow(tx *sql.Tx, id, kind string) (NativeRetirementReceipt, error) {
	var v NativeRetirementReceipt
	var raw, hash, operation, domain string
	var seq int64
	if e := tx.QueryRow("SELECT operation_id,domain_id,payload,payload_hash,record_sequence FROM native_retirement_receipts WHERE claim_id=? AND kind=?", id, kind).Scan(&operation, &domain, &raw, &hash, &seq); e != nil {
		return v, e
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return v, ErrRetirementAuthority
	}
	canon, digest, e := nativeJSON(v)
	if e != nil || canon != raw || digest != hash || v.Record.ClaimID != id || v.Record.Outcome != kind || v.Record.OperationID != operation || v.Record.DomainID != domain || !nativeAuditMatches(tx, seq, "native_retirement_recorded", id+":"+kind) {
		return v, ErrRetirementAuthority
	}
	return v, nil
}
func nativeRetirementReceipt(tx *sql.Tx, id, kind string) (NativeRetirementReceipt, error) {
	v, e := nativeRetirementReceiptRow(tx, id, kind)
	if e != nil {
		return v, e
	}
	operation, domain := v.Record.OperationID, v.Record.DomainID
	c, e := nativeRetirementClaim(tx, id)
	if e != nil {
		return v, e
	}
	intent, _, e := nativeRetirementIntent(tx, c.PermitID)
	if e != nil {
		return v, e
	}
	env, _, e := nativeEnvelopeRecord(tx, c.PermitID)
	if e != nil {
		return v, e
	}
	binding, e := nativeDomainRecord(tx, env.DomainBindingSequence)
	if e != nil {
		return v, e
	}
	if !nativeRetirementMatches(v.Record, c, intent) || v.OwnerServerLeaf != binding.Spec.OwnerServerLeaf || v.ObservedAt.Before(c.ClaimedAt) {
		return v, ErrRetirementAuthority
	}
	if kind == "accepted" {
		var reused bool
		if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM native_operation_receipts WHERE domain_id=? AND operation_id=?)", domain, operation).Scan(&reused); e != nil {
			return v, e
		}
		if reused {
			return v, ErrRetirementAuthority
		}
	}
	if kind == "unknown" || kind == "succeeded" || kind == "failed" {
		// The current claim/intent/domain are already validated above. Read the
		// accepted row without recursively re-walking that same provenance graph.
		accepted, e := nativeRetirementReceiptRow(tx, id, "accepted")
		if e == nil {
			if !nativeRetirementMatches(accepted.Record, c, intent) || accepted.OwnerServerLeaf != binding.Spec.OwnerServerLeaf || accepted.ObservedAt.Before(c.ClaimedAt) || v.ObservedAt.Before(accepted.ObservedAt) || (operation != "" && operation != accepted.Record.OperationID) {
				return v, ErrRetirementAuthority
			}
			var reused bool
			if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM native_operation_receipts WHERE domain_id=? AND operation_id=?)", domain, accepted.Record.OperationID).Scan(&reused); e != nil {
				return v, e
			}
			if reused {
				return v, ErrRetirementAuthority
			}
		} else if kind != "unknown" || !errors.Is(e, sql.ErrNoRows) {
			return v, e
		}
	}
	return v, nil
}
func nativeRetirementMatches(r nativeidentity.RetirementOperationRecord, c NativeRetirementClaim, i NativeRetirementIntent) bool {
	return r.PermitID == i.PermitID && r.IntentDigest == c.IntentDigest && r.ClaimID == c.ID && r.RequestDigest == c.RequestDigest && r.DomainID == i.DomainID && r.PolicyDigest == i.PolicyDigest && r.NativeName == i.NativeName && r.ResourceID == i.ResourceID && r.Dataset == i.Dataset && r.DatasetGUID == i.DatasetGUID && r.Step == c.Step && ((r.Outcome == "unknown" && (r.OperationID == "" || validOperationID(r.OperationID))) || ((r.Outcome == "accepted" || r.Outcome == "succeeded" || r.Outcome == "failed") && validOperationID(r.OperationID))) && r.OperationID != i.StartOperationID
}
func (s *SQLiteStore) RecordNativeRetirementOperation(ctx context.Context, o nativeidentity.RetirementOperationObservation) error {
	if !o.Valid() {
		return ErrRetirementAuthority
	}
	r := o.Record()
	return s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := nativeRetirementClaim(tx, r.ClaimID)
		if e != nil {
			return e
		}
		v, _, e := nativeRetirementIntent(tx, c.PermitID)
		if e != nil {
			return e
		}
		if e = nativeRetirementCurrent(tx, v, s.now()); e != nil {
			return e
		}
		env, _, e := nativeEnvelopeRecord(tx, c.PermitID)
		if e != nil {
			return e
		}
		domain, e := nativeDomainRecord(tx, env.DomainBindingSequence)
		if e != nil {
			return e
		}
		if !nativeRetirementMatches(r, c, v) || o.OwnerServerLeaf() != domain.Spec.OwnerServerLeaf || o.ObservedAt().Before(c.ClaimedAt) || o.ObservedAt().After(s.now()) || s.now().Sub(o.ObservedAt()) > time.Minute {
			return ErrRetirementAuthority
		}
		for _, kind := range []string{"accepted", "succeeded", "failed", "unknown"} {
			old, e := nativeRetirementReceipt(tx, c.ID, kind)
			if e == nil {
				if kind == r.Outcome {
					if old.Record != r {
						return ErrConflict
					}
					return nil
				}
				if kind == "accepted" && (o.ObservedAt().Before(old.ObservedAt) || (r.OperationID != "" && r.OperationID != old.Record.OperationID)) {
					return ErrConflict
				}
				if kind != "accepted" {
					return ErrConflict
				}
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if r.Outcome == "accepted" {
			var used bool
			if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM native_operation_receipts WHERE domain_id=? AND operation_id=?)", r.DomainID, r.OperationID).Scan(&used); e != nil {
				return e
			}
			if used {
				return ErrConflict
			}
		}
		var seen bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_retirement_recorded' AND request_key=?)", c.ID+":"+r.Outcome).Scan(&seen); e != nil {
			return e
		}
		if seen {
			return ErrRetirementAuthority
		}
		receipt := NativeRetirementReceipt{r, o.OwnerServerLeaf(), o.ObservedAt()}
		raw, hash, e := nativeJSON(receipt)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_retirement_recorded", c.ID+":"+r.Outcome, s.now())
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO native_retirement_receipts VALUES(?,?,?,?,?,?,?)", c.ID, r.Outcome, r.OperationID, r.DomainID, raw, hash, seq); e != nil {
			return e
		}
		_, e = nativeRetirementReceipt(tx, c.ID, r.Outcome)
		return e
	})
}
func (s *SQLiteStore) LookupNativeRetirement(ctx context.Context, permit string) (NativeRetirementIntent, string, error) {
	var v NativeRetirementIntent
	var h string
	e := s.readTransaction(ctx, func(tx *sql.Tx) error { var e error; v, h, e = nativeRetirementIntent(tx, permit); return e })
	return v, h, e
}
func (s *SQLiteStore) checkNativeRetirements(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		var bad int
		if e := tx.QueryRow(`SELECT count(*) FROM audit a WHERE (a.kind='native_retirement_requested' AND NOT EXISTS(SELECT 1 FROM native_retirement_intents v WHERE v.record_sequence=a.sequence)) OR (a.kind='native_retirement_claimed' AND NOT EXISTS(SELECT 1 FROM native_retirement_claims v WHERE v.record_sequence=a.sequence)) OR (a.kind='native_retirement_recorded' AND NOT EXISTS(SELECT 1 FROM native_retirement_receipts v WHERE v.record_sequence=a.sequence))`).Scan(&bad); e != nil {
			return e
		}
		if bad != 0 {
			return ErrRetirementAuthority
		}
		for _, table := range []string{"native_retirement_intents", "native_retirement_claims", "native_retirement_receipts"} {
			query := "SELECT permit_id,'' FROM " + table
			if table == "native_retirement_claims" {
				query = "SELECT claim_id,'' FROM " + table
			}
			if table == "native_retirement_receipts" {
				query = "SELECT claim_id,kind FROM " + table
			}
			rows, e := tx.Query(query)
			if e != nil {
				return e
			}
			var keys [][2]string
			for rows.Next() {
				var pair [2]string
				if e = rows.Scan(&pair[0], &pair[1]); e != nil {
					rows.Close()
					return e
				}
				keys = append(keys, pair)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, key := range keys {
				switch table {
				case "native_retirement_intents":
					_, _, e = nativeRetirementIntent(tx, key[0])
				case "native_retirement_claims":
					_, e = nativeRetirementClaim(tx, key[0])
				case "native_retirement_receipts":
					_, e = nativeRetirementReceipt(tx, key[0], key[1])
				}
				if e != nil {
					return e
				}
			}
		}
		return nil
	})
}

// NativeRetirementRequestDigest is a pure canonical command binding, not permission.
// Graceful stop applies ONLY to a previously proven empty guest. Runtime claims are
// still refused; a root journal alone is not such a production proof.
func NativeRetirementRequestDigest(v NativeRetirementIntent, step string) (string, error) {
	if v.Version != "native-retirement/v1" || (step != NativeRetireStop && step != NativeRetireDelete) {
		return "", ErrRetirementAuthority
	}
	body := ""
	if step == NativeRetireStop {
		body = `{"action":"stop","timeout":30,"force":false,"stateful":false}`
	}
	_, digest, e := nativeJSON(struct{ Version, Step, DomainID, PolicyDigest, NativeName, ResourceID, Body string }{v.Version, step, v.DomainID, v.PolicyDigest, v.NativeName, v.ResourceID, body})
	return digest, e
}
