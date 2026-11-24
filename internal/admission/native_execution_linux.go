package admission

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

var ErrNativeExecution = errors.New("exact enrolled native execution required")
var ErrNativeConsumed = errors.New("native execution already consumed; no renewed authority")

// NativeOwnerAuthority is minted from the actual verified TLS connection, never
// from request JSON. Enrollment independently selects the exact owner-role leaf.
type NativeOwnerAuthority struct {
	leaf    string
	expires time.Time
}

func AuthenticateNativeOwner(state *tls.ConnectionState) (NativeOwnerAuthority, error) {
	if state == nil || state.Version < tls.VersionTLS13 || !state.HandshakeComplete || len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
		return NativeOwnerAuthority{}, ErrNativeExecution
	}
	leaf := state.PeerCertificates[0]
	matched := false
	for _, chain := range state.VerifiedChains {
		if len(chain) > 0 && chain[0].Equal(leaf) {
			matched = true
		}
	}
	if !matched || leaf.NotAfter.IsZero() {
		return NativeOwnerAuthority{}, ErrNativeExecution
	}
	h := sha256.Sum256(leaf.Raw)
	return NativeOwnerAuthority{leaf: hex.EncodeToString(h[:]), expires: leaf.NotAfter}, nil
}

// NativeDomainSpec is trusted controller enrollment configuration. It is never
// sufficient evidence by itself; EnrollNativeDomain also requires a live,
// authenticated opaque owner observation matching every configured field.
type NativeDomainSpec struct {
	Controller          string `json:"controller"`
	Host                string `json:"host"`
	HostBindingSequence int64  `json:"host_binding_sequence"`
	DomainID            string `json:"domain_id"`
	PolicyDigest        string `json:"policy_digest"`
	OwnerClientLeaf     string `json:"owner_client_leaf"`
	OwnerServerLeaf     string `json:"owner_server_leaf"`
}
type NativeDomainBinding struct {
	Spec       NativeDomainSpec `json:"spec"`
	Sequence   int64            `json:"sequence"`
	EnrolledAt time.Time        `json:"enrolled_at"`
}
type NativeExecutionEnvelope struct {
	ReconciliationEpoch   int64          `json:"reconciliation_epoch"`
	Version               string         `json:"version"`
	PermitID              string         `json:"permit_id"`
	RequestKey            string         `json:"request_key"`
	DomainBindingSequence int64          `json:"domain_binding_sequence"`
	DomainID              string         `json:"domain_id"`
	PolicyDigest          string         `json:"policy_digest"`
	HostBindingSequence   int64          `json:"host_binding_sequence"`
	Controller            string         `json:"controller"`
	Pool                  string         `json:"pool"`
	Host                  string         `json:"host"`
	NativeName            string         `json:"native_name"`
	PlanDigest            string         `json:"plan_digest"`
	BodyDigest            string         `json:"body_digest"`
	Operation             string         `json:"operation"`
	Resources             Resources      `json:"resources"`
	Execution             ExecutionImage `json:"execution"`
}

// FreshNativeClaim is returned only after the FIRST durable consume commit.
// There is no lookup/replay API returning it. It is not a detached bearer token:
// the owner must obtain it through its own fresh authenticated claim exchange.
type FreshNativeClaim struct {
	ClaimID        string                  `json:"claim_id"`
	EnvelopeDigest string                  `json:"envelope_digest"`
	Envelope       NativeExecutionEnvelope `json:"envelope"`
	ConsumedAt     time.Time               `json:"consumed_at"`
}

const sqliteNativeExecutionSchema = `
CREATE TABLE native_domain_bindings (
 domain_id TEXT PRIMARY KEY NOT NULL, host TEXT UNIQUE NOT NULL,
 payload TEXT NOT NULL CHECK(length(payload)<=16384), payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE UNIQUE INDEX native_owner_client_once ON native_domain_bindings(json_extract(payload,'$.spec.owner_client_leaf'));
CREATE UNIQUE INDEX native_owner_server_once ON native_domain_bindings(json_extract(payload,'$.spec.owner_server_leaf'));
CREATE TRIGGER native_owner_roles_distinct BEFORE INSERT ON native_domain_bindings
WHEN EXISTS(SELECT 1 FROM native_domain_bindings WHERE
 json_extract(payload,'$.spec.owner_client_leaf') IN (json_extract(NEW.payload,'$.spec.owner_client_leaf'),json_extract(NEW.payload,'$.spec.owner_server_leaf')) OR
 json_extract(payload,'$.spec.owner_server_leaf') IN (json_extract(NEW.payload,'$.spec.owner_client_leaf'),json_extract(NEW.payload,'$.spec.owner_server_leaf')))
BEGIN SELECT RAISE(ABORT,'native owner role leaves cannot be reused'); END;
CREATE TRIGGER native_owner_not_stock_origin BEFORE INSERT ON stock_provider_origins
WHEN EXISTS(SELECT 1 FROM native_domain_bindings WHERE json_extract(NEW.payload,'$.authority.client_fingerprint') IN (json_extract(payload,'$.spec.owner_client_leaf'),json_extract(payload,'$.spec.owner_server_leaf')))
BEGIN SELECT RAISE(ABORT,'native owner leaf cannot authorize provider origin'); END;
CREATE TRIGGER native_owner_not_preparation BEFORE INSERT ON original_preparations
WHEN EXISTS(SELECT 1 FROM native_domain_bindings WHERE json_extract(NEW.payload,'$.source.client_fingerprint') IN (json_extract(payload,'$.spec.owner_client_leaf'),json_extract(payload,'$.spec.owner_server_leaf')))
BEGIN SELECT RAISE(ABORT,'native owner leaf cannot authorize preparation'); END;
CREATE TRIGGER native_domain_no_update BEFORE UPDATE ON native_domain_bindings BEGIN SELECT RAISE(ABORT,'native domain enrollment immutable'); END;
CREATE TRIGGER native_domain_no_delete BEFORE DELETE ON native_domain_bindings BEGIN SELECT RAISE(ABORT,'native domain enrollment permanent'); END;
CREATE TABLE native_executions (
 permit_id TEXT PRIMARY KEY NOT NULL REFERENCES dispatches(permit_id),
 request_key TEXT UNIQUE NOT NULL REFERENCES execution_plans(request_key),
 domain_id TEXT NOT NULL REFERENCES native_domain_bindings(domain_id), native_name TEXT NOT NULL,
 payload TEXT NOT NULL CHECK(length(payload)<=16384), payload_hash TEXT UNIQUE NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence),
 UNIQUE(domain_id,native_name)
) STRICT;
CREATE TRIGGER native_execution_no_update BEFORE UPDATE ON native_executions BEGIN SELECT RAISE(ABORT,'native execution immutable'); END;
CREATE TRIGGER native_execution_no_delete BEFORE DELETE ON native_executions BEGIN SELECT RAISE(ABORT,'native execution permanent'); END;
CREATE TABLE native_execution_consumptions (
 permit_id TEXT PRIMARY KEY NOT NULL REFERENCES native_executions(permit_id),
 claim_id TEXT UNIQUE NOT NULL CHECK(length(claim_id)=64),
 consumed_at TEXT NOT NULL, owner_leaf TEXT NOT NULL CHECK(length(owner_leaf)=64),
 record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TRIGGER native_consumption_no_update BEFORE UPDATE ON native_execution_consumptions BEGIN SELECT RAISE(ABORT,'native consume immutable'); END;
CREATE TRIGGER native_consumption_no_delete BEFORE DELETE ON native_execution_consumptions BEGIN SELECT RAISE(ABORT,'native consume permanent'); END;
CREATE TRIGGER native_outcome_requires_consumption BEFORE UPDATE OF state ON dispatches
WHEN NEW.state IN ('submitted','confirmed') AND EXISTS(SELECT 1 FROM native_executions WHERE permit_id=NEW.permit_id)
 AND NOT EXISTS(SELECT 1 FROM native_execution_consumptions WHERE permit_id=NEW.permit_id)
BEGIN SELECT RAISE(ABORT,'native outcome requires original consumed claim'); END;
PRAGMA user_version=19;
`

func validNativeDomain(s NativeDomainSpec) bool {
	return bindingName(s.Controller) && bindingName(s.Host) && s.HostBindingSequence > 0 && bindingFingerprint(s.DomainID) && bindingFingerprint(s.PolicyDigest) && bindingFingerprint(s.OwnerClientLeaf) && bindingFingerprint(s.OwnerServerLeaf) && s.OwnerClientLeaf != s.OwnerServerLeaf
}
func nativeJSON(v any) (string, string, error) {
	b, e := json.Marshal(v)
	if e != nil || len(b) > 16384 {
		return "", "", ErrNativeExecution
	}
	h := sha256.Sum256(b)
	return string(b), hex.EncodeToString(h[:]), nil
}
func NativeEnvelopeDigest(e NativeExecutionEnvelope) (string, error) {
	_, h, err := nativeJSON(e)
	return h, err
}
func nativeAuditSequence(tx *sql.Tx, kind, key string, now time.Time) (int64, error) {
	if e := sqliteAudit(tx, kind, key, now); e != nil {
		return 0, e
	}
	var seq int64
	e := tx.QueryRow("SELECT max(sequence) FROM audit").Scan(&seq)
	return seq, e
}
func nativeAuditMatches(tx *sql.Tx, seq int64, kind, key string) bool {
	var k, r string
	return seq > 0 && tx.QueryRow("SELECT kind,request_key FROM audit WHERE sequence=?", seq).Scan(&k, &r) == nil && k == kind && r == key
}
func nativeDomainRecord(tx *sql.Tx, seq int64) (NativeDomainBinding, error) {
	var b NativeDomainBinding
	var raw, hash, domain, host string
	if e := tx.QueryRow("SELECT domain_id,host,payload,payload_hash FROM native_domain_bindings WHERE record_sequence=?", seq).Scan(&domain, &host, &raw, &hash); e != nil {
		return b, e
	}
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &b) != nil || !validNativeDomain(b.Spec) || b.Sequence != seq || b.Spec.DomainID != domain || b.Spec.Host != host || b.EnrolledAt.IsZero() {
		return NativeDomainBinding{}, ErrNativeExecution
	}
	canonical, digest, e := nativeJSON(b)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_domain_enrolled", domain) {
		return NativeDomainBinding{}, ErrNativeExecution
	}
	return b, nil
}
func nativeEnvelopeRecord(tx *sql.Tx, permit string) (NativeExecutionEnvelope, string, error) {
	var e NativeExecutionEnvelope
	var raw, hash, key, domain, name string
	var seq int64
	if err := tx.QueryRow("SELECT request_key,domain_id,native_name,payload,payload_hash,record_sequence FROM native_executions WHERE permit_id=?", permit).Scan(&key, &domain, &name, &raw, &hash, &seq); err != nil {
		return e, "", err
	}
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &e) != nil || e.ReconciliationEpoch <= 0 || e.Version != "native-execution/v1" || e.Operation != "create_and_start" || e.PermitID != permit || e.RequestKey != key || e.DomainID != domain || e.NativeName != name || !bindingFingerprint(e.BodyDigest) || !bindingFingerprint(e.PlanDigest) {
		return NativeExecutionEnvelope{}, "", ErrNativeExecution
	}
	canonical, digest, err := nativeJSON(e)
	if err != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, seq, "native_execution_bound", permit) {
		return NativeExecutionEnvelope{}, "", ErrNativeExecution
	}
	return e, hash, nil
}

// BindNativeExecution runs only in the trusted controller's one-shot canonical
// dispatch callback, BEFORE remote delivery. All identities/resources come from
// the ledger; the body digest identifies the adapter's validated canonical body.
func (s *SQLiteStore) BindNativeExecution(ctx context.Context, permit DispatchPermit, domainSequence int64, bodyDigest string) (NativeExecutionEnvelope, error) {
	var out NativeExecutionEnvelope
	if permit.key == "" || permit.id == "" || !bindingFingerprint(bodyDigest) {
		return out, ErrNativeExecution
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		binding, e := nativeDomainRecord(tx, domainSequence)
		if e != nil {
			return e
		}
		plan, e := executionPlanRecord(tx, permit.key)
		if e != nil {
			return e
		}
		if e = validateNativeDispatch(tx, plan, permit.id, binding, s.now()); e != nil {
			return e
		}
		digest, e := executionPlanDigest(plan)
		if e != nil {
			return e
		}
		_, _, epoch, _, e := sqliteMetadata(tx)
		if e != nil {
			return e
		}
		out = NativeExecutionEnvelope{ReconciliationEpoch: epoch, Version: "native-execution/v1", PermitID: permit.id, RequestKey: permit.key, DomainBindingSequence: domainSequence, DomainID: binding.Spec.DomainID, PolicyDigest: binding.Spec.PolicyDigest, HostBindingSequence: plan.HostBindingSequence, Controller: plan.Execution.Controller, Pool: plan.Request.Pool, Host: plan.Request.Host, NativeName: plan.Execution.Name, PlanDigest: digest, BodyDigest: bodyDigest, Operation: "create_and_start", Resources: plan.Request.Resources, Execution: plan.Execution}
		raw, hash, e := nativeJSON(out)
		if e != nil {
			return e
		}
		old, oldHash, e := nativeEnvelopeRecord(tx, permit.id)
		if e == nil {
			if oldHash != hash {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var history bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_execution_bound' AND request_key=?)", permit.id).Scan(&history); e != nil {
			return e
		}
		if history {
			return ErrNativeExecution
		}
		seq, e := nativeAuditSequence(tx, "native_execution_bound", permit.id, s.now())
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_executions(permit_id,request_key,domain_id,native_name,payload,payload_hash,record_sequence) VALUES(?,?,?,?,?,?,?)", permit.id, permit.key, out.DomainID, out.NativeName, raw, hash, seq)
		return e
	})
	if err != nil {
		return NativeExecutionEnvelope{}, err
	}
	return out, nil
}

func validateNativeDispatch(tx *sql.Tx, p ExecutionPlan, permit string, b NativeDomainBinding, now time.Time) error {
	if p.Execution.Controller != b.Spec.Controller || p.Request.Host != b.Spec.Host || p.HostBindingSequence != b.Spec.HostBindingSequence {
		return ErrNativeExecution
	}
	record, id, e := dispatchRecord(tx, p.Request.Key)
	if e != nil || id != permit || record.State != "intent" || record.StockProviderSequence <= 0 || record.OriginalPreparationSequence != 0 || record.HostBindingSequence != p.HostBindingSequence {
		return ErrNativeExecution
	}
	var raw string
	if tx.QueryRow("SELECT payload FROM stock_provider_origins WHERE request_key=?", p.Request.Key).Scan(&raw) != nil {
		return ErrNativeExecution
	}
	var origin stockProviderOrigin
	if json.Unmarshal([]byte(raw), &origin) != nil {
		return ErrNativeExecution
	}
	if e := checkStockProviderOrigin(tx, p, &origin); e != nil {
		return e
	}
	var cancelled bool
	if tx.QueryRow("SELECT EXISTS(SELECT 1 FROM cancellations WHERE request_key=?)", p.Request.Key).Scan(&cancelled) != nil || cancelled {
		return ErrNativeExecution
	}
	config, ready, epoch, _, e := sqliteMetadata(tx)
	if e != nil {
		return e
	}
	if !ready || epoch == int64(1<<63-1) || !sqliteObservationsFresh(config, now) {
		return ErrReconciliation
	}
	host, ok := config.Hosts[p.Request.Host]
	pool, pok := config.Pools[p.Request.Pool]
	if !ok || !pok || !host.Healthy || host.Mode != "enabled" || !contains(pool.Hosts, p.Request.Host) {
		return ErrNativeExecution
	}
	current, e := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", p.Request.Host).Scan)
	if e != nil || current.Sequence != b.Spec.HostBindingSequence {
		return ErrNativeExecution
	}
	reservations, e := sqliteReservations(tx)
	if e != nil {
		return e
	}
	r, ok := reservations[p.Request.Key]
	if !ok || r.Request != p.Request || r.State != "creating" {
		return ErrNativeExecution
	}
	released, e := sqliteReleasedKeys(tx)
	if e != nil {
		return e
	}
	if released[p.Request.Key] {
		return ErrNativeExecution
	}
	inventory, e := sqliteInventory(tx)
	if e != nil {
		return e
	}
	for _, item := range inventory {
		if item.ReservationKey == p.Request.Key {
			return ErrNativeExecution
		}
	}
	freed, e := sqliteReuseFreed(tx)
	if e != nil {
		return e
	}
	allocations, e := sqliteAllocationsWithGrants(tx, config, reservations, inventory, released, freed)
	if e != nil {
		return e
	}
	if !fits(allocations.Global, Resources{}, config.Global) || !fits(allocations.Hosts[p.Request.Host], Resources{}, host.Budget) || !fits(allocations.Pools[p.Request.Pool], Resources{}, pool.Budget) {
		return ErrDenied
	}
	return nil
}

// ConsumeNativeExecution grants only the first authenticated consume response.
// Lost ACK, owner crash, concurrent duplicate or controller restart never returns
// another claim. This changes no capacity charge and authorizes no deletion.
func (s *SQLiteStore) ConsumeNativeExecution(ctx context.Context, authority NativeOwnerAuthority, permitID, envelopeDigest string) (FreshNativeClaim, error) {
	var result FreshNativeClaim
	if !bindingFingerprint(authority.leaf) || !bindingFingerprint(envelopeDigest) || permitID == "" {
		return result, ErrNativeExecution
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		envelope, digest, e := nativeEnvelopeRecord(tx, permitID)
		if e != nil || digest != envelopeDigest {
			return ErrNativeExecution
		}
		domain, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
		if e != nil {
			return e
		}
		if authority.leaf != domain.Spec.OwnerClientLeaf || !s.now().Before(authority.expires) {
			return ErrNativeExecution
		}
		var consumed bool
		if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM native_execution_consumptions WHERE permit_id=?)", permitID).Scan(&consumed); e != nil {
			return e
		}
		if consumed {
			return ErrNativeConsumed
		}
		var historical bool
		if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_execution_consumed' AND request_key=?)", permitID).Scan(&historical); e != nil {
			return e
		}
		if historical {
			return ErrNativeConsumed
		}
		plan, e := executionPlanRecord(tx, envelope.RequestKey)
		if e != nil {
			return e
		}
		if e := validateNativeDispatch(tx, plan, permitID, domain, s.now()); e != nil {
			return e
		}
		_, _, epoch, _, e := sqliteMetadata(tx)
		if e != nil || epoch != envelope.ReconciliationEpoch {
			return ErrFence
		}
		digestPlan, e := executionPlanDigest(plan)
		if e != nil || digestPlan != envelope.PlanDigest || !nativeEnvelopeMatchesPlan(envelope, plan, domain) {
			return ErrNativeExecution
		}
		var nonce [32]byte
		if _, e = rand.Read(nonce[:]); e != nil {
			return e
		}
		now := s.now().UTC()
		seq, e := nativeAuditSequence(tx, "native_execution_consumed", permitID, now)
		if e != nil {
			return e
		}
		claim := hex.EncodeToString(nonce[:])
		if _, e = tx.Exec("INSERT INTO native_execution_consumptions(permit_id,claim_id,consumed_at,owner_leaf,record_sequence) VALUES(?,?,?,?,?)", permitID, claim, now.Format(time.RFC3339Nano), authority.leaf, seq); e != nil {
			return e
		}
		result = FreshNativeClaim{ClaimID: claim, EnvelopeDigest: digest, Envelope: envelope, ConsumedAt: now}
		return nil
	})
	if err != nil {
		return FreshNativeClaim{}, err
	}
	return result, nil
}

// ObservedNativeHostBinding converts only an opaque live observation. The caller
// must collect it AFTER BeginReconciliation and include it in the complete host
// set passed to BindHostIdentities. It cannot adopt an already-bound different
// daemon or a legacy charged host.
func ObservedNativeHostBinding(host string, observation nativeidentity.Observation) (HostBinding, error) {
	if !bindingName(host) || !observation.Valid() {
		return HostBinding{}, ErrNativeExecution
	}
	d := observation.Record()
	b := HostBinding{Host: host, ServerKeyFingerprint: d.NativeServerKey, OriginFingerprint: d.NativeOrigin, ScopeFingerprint: d.NativeScope, ServerName: d.NativeServerName, ServerVersion: d.NativeServerVersion, ObservedAt: observation.ObservedAt()}
	if !validHostBinding(b) {
		return HostBinding{}, ErrHostBinding
	}
	return b, nil
}

// EnrollNativeDomain is a trusted controller enrollment step, never an owner
// claim endpoint. Configured policy/role identities must agree with a live pinned
// owner observation and the SAME independently bound native daemon. No raw JSON,
// installation receipt or operator boolean can be promoted to observation.
func (s *SQLiteStore) EnrollNativeDomain(ctx context.Context, spec NativeDomainSpec, observation nativeidentity.Observation) (NativeDomainBinding, error) {
	var out NativeDomainBinding
	if !validNativeDomain(spec) || !observation.Valid() {
		return out, ErrNativeExecution
	}
	d := observation.Record()
	if d.DomainID != spec.DomainID || d.PolicyDigest != spec.PolicyDigest || d.OwnerClientLeaf != spec.OwnerClientLeaf || d.OwnerServerLeaf != spec.OwnerServerLeaf {
		return out, ErrNativeExecution
	}
	observed, e := ObservedNativeHostBinding(spec.Host, observation)
	if e != nil {
		return out, e
	}
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		config, _, _, _, e := sqliteMetadata(tx)
		if e != nil {
			return e
		}
		now := s.now().UTC()
		if _, ok := config.Hosts[spec.Host]; !ok || observation.ObservedAt().After(now) || now.Sub(observation.ObservedAt()) > config.MaxObservationAge {
			return ErrNativeExecution
		}
		current, e := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", spec.Host).Scan)
		if e != nil || current.Sequence != spec.HostBindingSequence || !current.Matches(observed) {
			return ErrHostBinding
		}
		var reused bool
		if e = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM stock_provider_origins WHERE json_extract(payload,'$.authority.client_fingerprint') IN (?,?) UNION ALL SELECT 1 FROM original_preparations WHERE json_extract(payload,'$.source.client_fingerprint') IN (?,?))`, spec.OwnerClientLeaf, spec.OwnerServerLeaf, spec.OwnerClientLeaf, spec.OwnerServerLeaf).Scan(&reused); e != nil {
			return e
		}
		if reused {
			return ErrNativeExecution
		}
		var seq int64
		e = tx.QueryRow("SELECT record_sequence FROM native_domain_bindings WHERE domain_id=?", spec.DomainID).Scan(&seq)
		if e == nil {
			old, e := nativeDomainRecord(tx, seq)
			if e != nil {
				return e
			}
			if old.Spec != spec {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var history bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_domain_enrolled' AND request_key=?)", spec.DomainID).Scan(&history); e != nil {
			return e
		}
		if history {
			return ErrNativeExecution
		}
		seq, e = nativeAuditSequence(tx, "native_domain_enrolled", spec.DomainID, now)
		if e != nil {
			return e
		}
		out = NativeDomainBinding{Spec: spec, Sequence: seq, EnrolledAt: now}
		raw, hash, e := nativeJSON(out)
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_domain_bindings(domain_id,host,payload,payload_hash,record_sequence) VALUES(?,?,?,?,?)", spec.DomainID, spec.Host, raw, hash, seq)
		return e
	})
	if e != nil {
		return NativeDomainBinding{}, e
	}
	return out, nil
}

func nativeEnvelopeMatchesPlan(e NativeExecutionEnvelope, p ExecutionPlan, b NativeDomainBinding) bool {
	digest, err := executionPlanDigest(p)
	if err != nil {
		return false
	}
	expected := NativeExecutionEnvelope{ReconciliationEpoch: e.ReconciliationEpoch, Version: "native-execution/v1", PermitID: e.PermitID, RequestKey: p.Request.Key, DomainBindingSequence: b.Sequence, DomainID: b.Spec.DomainID, PolicyDigest: b.Spec.PolicyDigest, HostBindingSequence: p.HostBindingSequence, Controller: p.Execution.Controller, Pool: p.Request.Pool, Host: p.Request.Host, NativeName: p.Execution.Name, PlanDigest: digest, BodyDigest: e.BodyDigest, Operation: "create_and_start", Resources: p.Request.Resources, Execution: p.Execution}
	a, _, errA := nativeJSON(expected)
	c, _, errC := nativeJSON(e)
	return errA == nil && errC == nil && a == c
}

func sqliteCheckNativeExecutions(ctx context.Context, s *SQLiteStore) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		var missing bool
		if e := tx.QueryRow(`SELECT EXISTS(
   SELECT 1 FROM audit a LEFT JOIN native_domain_bindings d ON d.record_sequence=a.sequence AND d.domain_id=a.request_key WHERE a.kind='native_domain_enrolled' AND d.domain_id IS NULL
   UNION ALL SELECT 1 FROM audit a LEFT JOIN native_executions n ON n.record_sequence=a.sequence AND n.permit_id=a.request_key WHERE a.kind='native_execution_bound' AND n.permit_id IS NULL
   UNION ALL SELECT 1 FROM audit a LEFT JOIN native_execution_consumptions c ON c.record_sequence=a.sequence AND c.permit_id=a.request_key WHERE a.kind='native_execution_consumed' AND c.permit_id IS NULL)`).Scan(&missing); e != nil {
			return e
		}
		if missing {
			return ErrNativeExecution
		}
		domainRows, e := tx.Query("SELECT record_sequence FROM native_domain_bindings")
		if e != nil {
			return e
		}
		var seqs []int64
		for domainRows.Next() {
			var seq int64
			if e := domainRows.Scan(&seq); e != nil {
				domainRows.Close()
				return e
			}
			seqs = append(seqs, seq)
		}
		e = domainRows.Err()
		domainRows.Close()
		if e != nil {
			return e
		}
		for _, seq := range seqs {
			b, e := nativeDomainRecord(tx, seq)
			if e != nil {
				return e
			}
			var actual int64
			if tx.QueryRow("SELECT bound_sequence FROM host_bindings WHERE host=?", b.Spec.Host).Scan(&actual) != nil || actual != b.Spec.HostBindingSequence {
				return ErrNativeExecution
			}
		}
		rows, e := tx.Query("SELECT permit_id FROM native_executions")
		if e != nil {
			return e
		}
		var permits []string
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			permits = append(permits, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range permits {
			envelope, _, e := nativeEnvelopeRecord(tx, id)
			if e != nil {
				return e
			}
			binding, e := nativeDomainRecord(tx, envelope.DomainBindingSequence)
			if e != nil {
				return e
			}
			plan, e := executionPlanRecord(tx, envelope.RequestKey)
			if e != nil {
				return e
			}
			if !nativeEnvelopeMatchesPlan(envelope, plan, binding) {
				return ErrNativeExecution
			}
			dispatch, permit, e := dispatchRecord(tx, envelope.RequestKey)
			if e != nil || permit != id || dispatch.StockProviderSequence <= 0 || dispatch.OriginalPreparationSequence != 0 || dispatch.HostBindingSequence != envelope.HostBindingSequence {
				return ErrNativeExecution
			}
			var claim, at, leaf string
			var seq int64
			e = tx.QueryRow("SELECT claim_id,consumed_at,owner_leaf,record_sequence FROM native_execution_consumptions WHERE permit_id=?", id).Scan(&claim, &at, &leaf, &seq)
			if errors.Is(e, sql.ErrNoRows) {
				if dispatch.State == "submitted" || dispatch.State == "confirmed" {
					return ErrNativeExecution
				}
				continue
			}
			if e != nil {
				return e
			}
			consumed, e := time.Parse(time.RFC3339Nano, at)
			if e != nil || consumed.Before(plan.CreatedAt) || consumed.Before(binding.EnrolledAt) || !bindingFingerprint(claim) || leaf != binding.Spec.OwnerClientLeaf || !nativeAuditMatches(tx, seq, "native_execution_consumed", id) {
				return ErrNativeExecution
			}
		}
		var orphan bool
		if e := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM native_execution_consumptions c LEFT JOIN native_executions n ON c.permit_id=n.permit_id WHERE n.permit_id IS NULL)").Scan(&orphan); e != nil {
			return e
		}
		if orphan {
			return ErrNativeExecution
		}
		return nil
	})
}

// LookupNativeDomainBinding returns validated immutable enrollment data only.
// It is not execution authority and does not reopen readiness or replace proof.
func (s *SQLiteStore) LookupNativeDomainBinding(ctx context.Context, sequence int64) (NativeDomainBinding, error) {
	var out NativeDomainBinding
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { var e error; out, e = nativeDomainRecord(tx, sequence); return e })
	if err != nil {
		return NativeDomainBinding{}, err
	}
	return out, nil
}
