package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"
)

// Schema10 guard storage. There is deliberately NO active-transition writer or
// constructor: only future qualified coordination may create an active state.
const sqlitePoolRouteStateSchema = `
CREATE INDEX pool_route_audit_history ON audit(kind,request_key);
CREATE TABLE pool_route_states (
 pool_id TEXT NOT NULL REFERENCES pool_routes(pool_id),
 revision INTEGER NOT NULL CHECK(revision>0),
 identity_sequence INTEGER NOT NULL REFERENCES pool_routes(identity_sequence),
 state_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence),
 mode TEXT NOT NULL CHECK(mode IN ('inactive','active')),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 PRIMARY KEY(pool_id,revision)
) STRICT;
CREATE UNIQUE INDEX pool_route_enable_intent_once ON pool_route_states(
 json_extract(payload,'$.enable.management_identity'),json_extract(payload,'$.enable.journal_lineage'),json_extract(payload,'$.enable.request_id')) WHERE mode='active';
CREATE TRIGGER pool_route_states_no_update BEFORE UPDATE ON pool_route_states
BEGIN SELECT RAISE(ABORT,'pool route state history is immutable'); END;
CREATE TRIGGER pool_route_states_no_delete BEFORE DELETE ON pool_route_states
BEGIN SELECT RAISE(ABORT,'pool route state history is permanent'); END;
CREATE TRIGGER pool_route_states_sequence BEFORE INSERT ON pool_route_states
WHEN NEW.revision != COALESCE((SELECT max(revision) FROM pool_route_states WHERE pool_id=NEW.pool_id),0)+1
OR NEW.mode=COALESCE((SELECT mode FROM pool_route_states WHERE pool_id=NEW.pool_id ORDER BY revision DESC LIMIT 1),'inactive')
OR NOT EXISTS(SELECT 1 FROM pool_routes WHERE pool_id=NEW.pool_id AND identity_sequence=NEW.identity_sequence)
BEGIN SELECT RAISE(ABORT,'pool route state sequence mismatch'); END;

ALTER TABLE reservations ADD COLUMN pool_route_identity_sequence INTEGER NOT NULL DEFAULT 0 CHECK(pool_route_identity_sequence>=0);
ALTER TABLE reservations ADD COLUMN pool_route_revision INTEGER NOT NULL DEFAULT 0 CHECK(pool_route_revision>=0);
ALTER TABLE dispatches ADD COLUMN pool_route_identity_sequence INTEGER NOT NULL DEFAULT 0 CHECK(pool_route_identity_sequence>=0);
ALTER TABLE dispatches ADD COLUMN pool_route_revision INTEGER NOT NULL DEFAULT 0 CHECK(pool_route_revision>=0);
DROP TRIGGER inactive_pool_no_new_reservation;
DROP TRIGGER inactive_pool_no_new_dispatch;
CREATE TRIGGER pool_route_reservation_provenance BEFORE INSERT ON reservations
BEGIN
 SELECT CASE WHEN EXISTS(SELECT 1 FROM pool_routes p WHERE p.pool_id=NEW.pool) THEN
 CASE WHEN NOT EXISTS(SELECT 1 FROM pool_route_states s JOIN pool_routes p ON p.pool_id=s.pool_id
 WHERE s.pool_id=NEW.pool AND s.revision=(SELECT max(revision) FROM pool_route_states WHERE pool_id=NEW.pool)
 AND s.mode='active' AND s.identity_sequence=p.identity_sequence
 AND NEW.pool_route_identity_sequence=p.identity_sequence AND NEW.pool_route_revision=s.revision)
 THEN RAISE(ABORT,'pool route inactive or provenance missing') END
 ELSE CASE WHEN NEW.pool_route_identity_sequence!=0 OR NEW.pool_route_revision!=0
 OR EXISTS(SELECT 1 FROM audit WHERE kind='pool_route_registered_inactive' AND request_key=NEW.pool)
 THEN RAISE(ABORT,'pool route history missing') END END;
END;
CREATE TRIGGER pool_route_plan_provenance BEFORE INSERT ON execution_plans
WHEN NOT EXISTS(SELECT 1 FROM reservations r WHERE r.request_key=NEW.request_key
 AND r.pool_route_identity_sequence=COALESCE(json_extract(NEW.payload,'$.pool_route_identity_sequence'),0)
 AND r.pool_route_revision=COALESCE(json_extract(NEW.payload,'$.pool_route_revision'),0))
BEGIN SELECT RAISE(ABORT,'pool route plan provenance missing'); END;
CREATE TRIGGER pool_route_dispatch_provenance BEFORE INSERT ON dispatches
BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM reservations r WHERE r.request_key=NEW.reservation_key
 AND r.pool_route_identity_sequence=NEW.pool_route_identity_sequence AND r.pool_route_revision=NEW.pool_route_revision)
 THEN RAISE(ABORT,'pool route dispatch provenance missing') END;
 SELECT CASE WHEN EXISTS(SELECT 1 FROM reservations r JOIN pool_routes p ON p.pool_id=r.pool WHERE r.request_key=NEW.reservation_key)
 AND NOT EXISTS(SELECT 1 FROM reservations r JOIN pool_route_states s ON s.pool_id=r.pool JOIN pool_routes p ON p.pool_id=r.pool
 WHERE r.request_key=NEW.reservation_key AND s.mode='active' AND s.identity_sequence=p.identity_sequence
 AND s.revision=(SELECT max(revision) FROM pool_route_states WHERE pool_id=r.pool)
 AND NEW.pool_route_identity_sequence=p.identity_sequence AND NEW.pool_route_revision=s.revision)
 THEN RAISE(ABORT,'pool route no longer active') END;
END;
CREATE TRIGGER pool_route_reservation_identity_immutable BEFORE UPDATE OF pool_route_identity_sequence,pool_route_revision ON reservations
WHEN OLD.pool_route_identity_sequence!=NEW.pool_route_identity_sequence OR OLD.pool_route_revision!=NEW.pool_route_revision
BEGIN SELECT RAISE(ABORT,'pool route reservation provenance immutable'); END;
CREATE TRIGGER pool_route_dispatch_identity_immutable BEFORE UPDATE OF pool_route_identity_sequence,pool_route_revision ON dispatches
WHEN OLD.pool_route_identity_sequence!=NEW.pool_route_identity_sequence OR OLD.pool_route_revision!=NEW.pool_route_revision
BEGIN SELECT RAISE(ABORT,'pool route dispatch provenance immutable'); END;
CREATE TRIGGER pool_route_source_provenance BEFORE INSERT ON original_preparations
WHEN NOT EXISTS(SELECT 1 FROM reservations r WHERE r.request_key=NEW.request_key
 AND r.pool_route_identity_sequence=COALESCE(json_extract(NEW.payload,'$.pool_route_identity_sequence'),0)
 AND r.pool_route_revision=COALESCE(json_extract(NEW.payload,'$.pool_route_revision'),0))
BEGIN SELECT RAISE(ABORT,'pool route source provenance missing'); END;
PRAGMA user_version=10;
`

var ErrPoolRouteInactive = errors.New("pool route inactive or admission revision obsolete")

// PoolRouteEnableReference is historical whitelist data, NOT authority accepted
// from a caller or a JSON file. Only a separately qualified trusted coordinator
// may eventually construct and record ACTIVE state after bound-journal confirmed
// enable + fresh exact read. No such writer exists in this checkpoint.
type PoolRouteEnableReference struct {
	LedgerLineage      string    `json:"ledger_lineage"`
	ReviewDigest       string    `json:"review_digest"`
	ManagementIdentity string    `json:"management_identity"`
	JournalLineage     string    `json:"journal_lineage"`
	RequestID          string    `json:"request_id"`
	PoolID             string    `json:"pool_id"`
	Generation         uint64    `json:"generation"`
	SpecFingerprint    string    `json:"spec_fingerprint"`
	PoolFingerprint    string    `json:"pool_fingerprint"`
	ObservedAt         time.Time `json:"observed_at"`
}

// PoolRouteState is an immutable historical read result, never a permit or a
// native/job fence. Revision0/inactive is the original registration state.
type PoolRouteState struct {
	Protocol         string                    `json:"protocol"`
	PoolID           string                    `json:"pool_id"`
	IdentitySequence int64                     `json:"identity_sequence"`
	Revision         int64                     `json:"revision"`
	StateSequence    int64                     `json:"state_sequence"`
	Mode             string                    `json:"mode"`
	Reason           string                    `json:"reason"`
	RecordedAt       time.Time                 `json:"recorded_at"`
	Enable           *PoolRouteEnableReference `json:"enable,omitempty"`
	Replay           bool                      `json:"-"`
}

func validPoolRouteState(s PoolRouteState, route PoolRouteRecord) bool {
	if s.Protocol != "pool-route-state/v1" || s.PoolID != route.Identity.PoolID || s.IdentitySequence != route.Identity.IdentitySequence || s.Revision <= 0 || s.StateSequence <= route.Identity.IdentitySequence || s.RecordedAt.IsZero() || s.RecordedAt.Before(route.Identity.RegisteredAt) || !validAdminText(s.Reason, 256) {
		return false
	}
	if s.Mode == "inactive" {
		return s.Enable == nil
	}
	if s.Mode != "active" || s.Enable == nil {
		return false
	}
	p := s.Enable
	return bindingFingerprint(p.LedgerLineage) && bindingFingerprint(p.ReviewDigest) && p.ManagementIdentity == route.Identity.ManagementIdentity && p.JournalLineage == route.Identity.JournalLineage && routeUUID(p.RequestID) && p.PoolID == s.PoolID && p.Generation == route.Identity.Generation && p.SpecFingerprint == route.Identity.SpecFingerprint && bindingFingerprint(p.PoolFingerprint) && !p.ObservedAt.IsZero() && !p.ObservedAt.Before(route.Identity.RegisteredAt) && !p.ObservedAt.After(s.RecordedAt)
}
func poolRouteStateRecord(tx *sql.Tx, route PoolRouteRecord, revision int64) (PoolRouteState, error) {
	var state PoolRouteState
	var payload, hash, mode string
	var identity, seq int64
	if err := tx.QueryRow("SELECT identity_sequence,state_sequence,mode,payload,payload_hash FROM pool_route_states WHERE pool_id=? AND revision=?", route.Identity.PoolID, revision).Scan(&identity, &seq, &mode, &payload, &hash); err != nil {
		return state, err
	}
	sum := sha256.Sum256([]byte(payload))
	if len(payload) > 16384 || hex.EncodeToString(sum[:]) != hash || json.Unmarshal([]byte(payload), &state) != nil || !validPoolRouteState(state, route) || state.Revision != revision || state.IdentitySequence != identity || state.StateSequence != seq || state.Mode != mode {
		return PoolRouteState{}, ErrPoolRoute
	}
	canonical, err := json.Marshal(state)
	if err != nil || string(canonical) != payload {
		return PoolRouteState{}, ErrPoolRoute
	}
	kind := "pool_route_deactivated"
	if mode == "active" {
		kind = "pool_route_activated"
	}
	var actual, subject string
	if tx.QueryRow("SELECT kind,request_key FROM audit WHERE sequence=?", seq).Scan(&actual, &subject) != nil || actual != kind || subject != state.PoolID {
		return PoolRouteState{}, ErrPoolRoute
	}
	if state.Mode == "active" {
		var ledgerLineage string
		if tx.QueryRow("SELECT lineage_id FROM ledger_lineage WHERE id=1").Scan(&ledgerLineage) != nil || state.Enable.LedgerLineage != ledgerLineage {
			return PoolRouteState{}, ErrRuntimeLineage
		}
		var used int
		if err := tx.QueryRow("SELECT count(*) FROM pool_route_states WHERE mode='active' AND json_extract(payload,'$.enable.management_identity')=? AND json_extract(payload,'$.enable.journal_lineage')=? AND json_extract(payload,'$.enable.request_id')=?", state.Enable.ManagementIdentity, state.Enable.JournalLineage, state.Enable.RequestID).Scan(&used); err != nil {
			return PoolRouteState{}, err
		}
		if used != 1 {
			return PoolRouteState{}, ErrPoolRoute
		}
	}
	return state, nil
}
func poolRouteLatestState(tx *sql.Tx, route PoolRouteRecord) (PoolRouteState, error) {
	var latest, count int64
	if err := tx.QueryRow("SELECT coalesce(max(revision),0),count(*) FROM pool_route_states WHERE pool_id=?", route.Identity.PoolID).Scan(&latest, &count); err != nil {
		return PoolRouteState{}, err
	}
	if latest != count {
		return PoolRouteState{}, ErrPoolRoute
	}
	// Reverse permanent audit completeness prevents a deleted last transition from
	// silently reopening an older ACTIVE revision or falling back to registration.
	var missing int
	if err := tx.QueryRow(`SELECT count(*) FROM audit a LEFT JOIN pool_route_states s ON s.state_sequence=a.sequence
WHERE a.request_key=? AND a.kind IN ('pool_route_activated','pool_route_deactivated') AND (s.state_sequence IS NULL OR s.pool_id!=a.request_key)`, route.Identity.PoolID).Scan(&missing); err != nil {
		return PoolRouteState{}, err
	}
	if missing != 0 {
		return PoolRouteState{}, ErrPoolRoute
	}
	if latest == 0 {
		return PoolRouteState{Protocol: "pool-route-state/v1", PoolID: route.Identity.PoolID, IdentitySequence: route.Identity.IdentitySequence, Mode: "inactive", RecordedAt: route.Identity.RegisteredAt}, nil
	}
	var previous time.Time
	previousSequence := route.Identity.IdentitySequence
	previousMode := "inactive"
	var state PoolRouteState
	for revision := int64(1); revision <= latest; revision++ {
		s, err := poolRouteStateRecord(tx, route, revision)
		if err != nil || (!previous.IsZero() && s.RecordedAt.Before(previous)) || s.StateSequence <= previousSequence || s.Mode == previousMode {
			return PoolRouteState{}, ErrPoolRoute
		}
		previous = s.RecordedAt
		previousSequence = s.StateSequence
		previousMode = s.Mode
		state = s
	}
	return state, nil
}
func (s *SQLiteStore) LookupPoolRouteState(ctx context.Context, poolID string) (PoolRouteState, error) {
	var result PoolRouteState
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		route, err := poolRouteRecord(tx, poolID)
		if err != nil {
			return err
		}
		result, err = poolRouteLatestState(tx, route)
		return err
	})
	if err != nil {
		return PoolRouteState{}, err
	}
	return result, nil
}

// requirePoolRouteRevision is a transaction-local eligibility check only. A
// static pool uses 0/0. A missing historically registered route never becomes
// static, and a known dynamic route needs its exact current ACTIVE revision.
// This cannot revoke an already committed/delivered dispatch permit.
func requirePoolRouteRevision(tx *sql.Tx, poolID string, identity, revision int64) error {
	route, err := poolRouteRecord(tx, poolID)
	if errors.Is(err, sql.ErrNoRows) {
		var prior int
		if err := tx.QueryRow("SELECT count(*) FROM audit WHERE kind='pool_route_registered_inactive' AND request_key=?", poolID).Scan(&prior); err != nil {
			return err
		}
		if prior != 0 || identity != 0 || revision != 0 {
			return ErrPoolRoute
		}
		return nil
	}
	if err != nil {
		return err
	}
	current, err := poolRouteLatestState(tx, route)
	if err != nil {
		return err
	}
	if current.Mode != "active" || identity != route.Identity.IdentitySequence || revision <= 0 || revision != current.Revision {
		return ErrPoolRouteInactive
	}
	return nil
}

func validPoolRouteProvenance(identity, revision int64) bool {
	return (identity == 0 && revision == 0) || (identity > 0 && revision > 0)
}

// validRoutePlanPolicy compares historical execution to the immutable route;
// it does not grant current eligibility or consult/rewrite today's private refs.
func validRoutePlanPolicy(route PoolRouteRecord, plan ExecutionPlan) bool {
	image := route.Identity.Template.Execution
	image.Name = plan.Execution.Name
	return plan.PoolRouteIdentitySequence == route.Identity.IdentitySequence && plan.PoolRouteRevision > 0 && plan.Request.Pool == route.Identity.PoolID && plan.Request.Resources == route.Identity.Template.PerRunner && reflect.DeepEqual(plan.Execution, image) && contains(route.Identity.Template.Hosts, plan.Request.Host) && plan.HostBindingSequence == route.Identity.HostBindingSequences[plan.Request.Host] && !plan.CreatedAt.Before(route.Identity.RegisteredAt)
}
func checkPlacementRoutePolicy(tx *sql.Tx, request PlacementRequest) error {
	if err := requirePoolRouteRevision(tx, request.Pool, request.PoolRouteIdentitySequence, request.PoolRouteRevision); err != nil {
		return err
	}
	if request.PoolRouteIdentitySequence == 0 {
		return nil
	}
	route, err := poolRouteRecord(tx, request.Pool)
	if err != nil {
		return err
	}
	hosts := append([]string(nil), request.Hosts...)
	sort.Strings(hosts)
	image := route.Identity.Template.Execution
	image.Name = request.Execution.Name
	if request.Strategy != route.Identity.Template.Strategy || !reflect.DeepEqual(hosts, route.Identity.Template.Hosts) || request.Resources != route.Identity.Template.PerRunner || !reflect.DeepEqual(request.Execution, image) {
		return ErrPoolRoute
	}
	for host, sequence := range route.Identity.HostBindingSequences {
		binding, err := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", host).Scan)
		if err != nil || binding.Sequence != sequence {
			return ErrHostBinding
		}
	}
	return nil
}
func checkHistoricalRoutePlan(tx *sql.Tx, plan ExecutionPlan) error {
	route, err := poolRouteRecord(tx, plan.Request.Pool)
	if errors.Is(err, sql.ErrNoRows) {
		var prior int
		if err := tx.QueryRow("SELECT count(*) FROM audit WHERE kind='pool_route_registered_inactive' AND request_key=?", plan.Request.Pool).Scan(&prior); err != nil {
			return err
		}
		if prior != 0 || plan.PoolRouteIdentitySequence != 0 || plan.PoolRouteRevision != 0 {
			return ErrPoolRoute
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !validRoutePlanPolicy(route, plan) {
		return ErrPoolRoute
	}
	original, err := poolRouteStateRecord(tx, route, plan.PoolRouteRevision)
	if err != nil || original.Mode != "active" || original.RecordedAt.After(plan.CreatedAt) {
		return ErrPoolRoute
	}
	return nil
}

func checkPoolRouteStateAuditAnchors(tx *sql.Tx) error {
	var missing int
	if err := tx.QueryRow(`SELECT count(*) FROM audit a LEFT JOIN pool_route_states s ON s.state_sequence=a.sequence
 LEFT JOIN pool_routes p ON p.pool_id=s.pool_id AND p.identity_sequence=s.identity_sequence
 WHERE a.kind IN ('pool_route_activated','pool_route_deactivated')
 AND (s.state_sequence IS NULL OR s.pool_id!=a.request_key OR p.pool_id IS NULL)`).Scan(&missing); err != nil {
		return err
	}
	if missing != 0 {
		return ErrPoolRoute
	}
	return nil
}

// DeactivatePoolRoute is local stop-new only. It cannot enable a route, contact
// Garm, revoke an already delivered callback, drain jobs or release any charge.
// The exact original identity/current revision is a transactional CAS. Existing
// inactive state is returned as a read-only no-op, not proof of a native fence.
func (s *SQLiteStore) DeactivatePoolRoute(ctx context.Context, poolID string, identity, expectedRevision int64, reason string) (PoolRouteState, error) {
	var result PoolRouteState
	if !routeUUID(poolID) || identity <= 0 || expectedRevision < 0 || !validAdminText(reason, 256) {
		return result, ErrPoolRoute
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		route, err := poolRouteRecord(tx, poolID)
		if err != nil {
			return err
		}
		if route.Identity.IdentitySequence != identity {
			return ErrConflict
		}
		current, err := poolRouteLatestState(tx, route)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrPolicyConflict
		}
		if current.Mode == "inactive" {
			current.Replay = true
			result = current
			return nil
		}
		if current.Revision == int64(1<<63-1) {
			return ErrPoolRoute
		}
		now := s.now().UTC()
		if now.Before(current.RecordedAt) {
			return ErrPoolRoute
		}
		if err := sqliteAudit(tx, "pool_route_deactivated", poolID, now); err != nil {
			return err
		}
		var sequence int64
		if err := tx.QueryRow("SELECT last_insert_rowid()").Scan(&sequence); err != nil {
			return err
		}
		state := PoolRouteState{Protocol: "pool-route-state/v1", PoolID: poolID, IdentitySequence: identity, Revision: current.Revision + 1, StateSequence: sequence, Mode: "inactive", Reason: reason, RecordedAt: now}
		if !validPoolRouteState(state, route) || sequence <= current.StateSequence {
			return ErrPoolRoute
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if _, err := tx.Exec("INSERT INTO pool_route_states(pool_id,revision,identity_sequence,state_sequence,mode,payload,payload_hash) VALUES(?,?,?,?,?,?,?)", poolID, state.Revision, identity, sequence, state.Mode, string(raw), hex.EncodeToString(sum[:])); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE metadata SET ready=0,nonce='',began_at='' WHERE id=1"); err != nil {
			return err
		}
		result = state
		return nil
	})
	if err != nil {
		return PoolRouteState{}, err
	}
	return result, nil
}
