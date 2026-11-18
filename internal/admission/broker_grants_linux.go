package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

var ErrBrokerGrant = fmt.Errorf("%w: matching durable broker grant required", ErrDenied)

// BrokerGrantSpec is server-owned admission configuration, never provider JSON.
// Execution.Name must be empty: the real stock runner identity exists only at
// redemption. A grant is a capacity charge, not permission to execute a create.
type BrokerGrantSpec struct {
	Authority           StockProviderAuthority `json:"authority"`
	Host                string                 `json:"host"`
	HostBindingSequence int64                  `json:"host_binding_sequence"`
	Resources           Resources              `json:"resources"`
	Execution           ExecutionImage         `json:"execution"`
	ExpiresAt           time.Time              `json:"expires_at"`
}
type BrokerGrant struct {
	ID         string          `json:"id"`
	Spec       BrokerGrantSpec `json:"spec"`
	State      string          `json:"state"`
	RequestKey string          `json:"request_key,omitempty"`
}
type BrokerGrantBatch struct {
	ID               string            `json:"id"`
	ExpectedSequence int64             `json:"expected_sequence"`
	PolicyRevision   int64             `json:"policy_revision"`
	Grants           []BrokerGrantSpec `json:"grants"`
}
type BrokerGrantState struct {
	DomainOverheads []NativeDomainOverhead
	NativeDomains   map[int64]NativeDomainBinding
	PoolProfiles    map[string]BrokerPoolProfile
	Allocated       Allocations
	HostBindings    map[string]HostBinding
	ExecutionPlans  map[string]ExecutionPlan
	Sequence        int64
	Policy          BrokerPolicy
	Config          Config
	Ready           bool
	Epoch           int64
	Reservations    map[string]Reservation
	Inventory       []InventoryResource
	Released        map[string]bool
	Grants          []BrokerGrant
}

const sqliteBrokerGrantSchema = `
CREATE TABLE broker_grant_legacy_stock (request_key TEXT PRIMARY KEY NOT NULL REFERENCES execution_plans(request_key)) STRICT;
INSERT INTO broker_grant_legacy_stock SELECT request_key FROM stock_provider_origins;
CREATE TRIGGER broker_grant_legacy_no_insert BEFORE INSERT ON broker_grant_legacy_stock BEGIN SELECT RAISE(ABORT,'legacy grant marker closed'); END;
CREATE TRIGGER broker_grant_legacy_no_update BEFORE UPDATE ON broker_grant_legacy_stock BEGIN SELECT RAISE(ABORT,'legacy grant marker immutable'); END;
CREATE TRIGGER broker_grant_legacy_no_delete BEFORE DELETE ON broker_grant_legacy_stock BEGIN SELECT RAISE(ABORT,'legacy grant marker permanent'); END;
CREATE TABLE broker_grant_batches (
 batch_id TEXT PRIMARY KEY NOT NULL, payload TEXT NOT NULL, payload_hash TEXT NOT NULL,
 sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TABLE broker_grants (
 grant_id TEXT PRIMARY KEY NOT NULL,
 batch_id TEXT NOT NULL REFERENCES broker_grant_batches(batch_id),
 payload TEXT NOT NULL, payload_hash TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('unused','consumed','revoked','expired')),
 request_key TEXT UNIQUE REFERENCES execution_plans(request_key),
 CHECK((state='consumed' AND request_key IS NOT NULL) OR (state!='consumed' AND request_key IS NULL))
) STRICT;
CREATE TRIGGER broker_batch_no_update BEFORE UPDATE ON broker_grant_batches BEGIN SELECT RAISE(ABORT,'grant batch immutable'); END;
CREATE TRIGGER broker_batch_no_delete BEFORE DELETE ON broker_grant_batches BEGIN SELECT RAISE(ABORT,'grant batch permanent'); END;
CREATE TRIGGER broker_grant_no_delete BEFORE DELETE ON broker_grants BEGIN SELECT RAISE(ABORT,'grant permanent'); END;
CREATE TRIGGER broker_grant_identity_immutable BEFORE UPDATE ON broker_grants
WHEN OLD.grant_id IS NOT NEW.grant_id OR OLD.batch_id IS NOT NEW.batch_id OR OLD.payload IS NOT NEW.payload OR OLD.payload_hash IS NOT NEW.payload_hash
BEGIN SELECT RAISE(ABORT,'grant identity immutable'); END;
CREATE TRIGGER broker_grant_terminal BEFORE UPDATE ON broker_grants
WHEN OLD.state!='unused' OR NEW.state='unused'
BEGIN SELECT RAISE(ABORT,'grant transition forbidden'); END;
ALTER TABLE reservations ADD COLUMN accounting_version INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER reservation_current_accounting BEFORE INSERT ON reservations
WHEN NEW.accounting_version!=17
BEGIN SELECT RAISE(ABORT,'current shared grant accounting required'); END;
ALTER TABLE dispatches ADD COLUMN grant_accounting_version INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER dispatch_current_accounting BEFORE INSERT ON dispatches
WHEN NEW.grant_accounting_version!=17
BEGIN SELECT RAISE(ABORT,'current shared grant accounting required'); END;
PRAGMA user_version=17;
`

func grantHash(v any) (string, string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", "", e
	}
	h := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(h[:]), nil
}
func validGrantSpec(g BrokerGrantSpec) bool {
	if g.Execution.Name != "" || g.HostBindingSequence <= 0 || g.ExpiresAt.IsZero() {
		return false
	}
	e := g.Execution
	e.Name = "grant-validation"
	r := Request{Pool: g.Authority.PoolID, Host: g.Host, Resources: g.Resources, Key: executionKey(e, g.Authority.PoolID)}
	return validRequest(r) && validExecution(e, r) && validStockOrigin(stockProviderOrigin{Authority: g.Authority, CommandDigest: fmt.Sprintf("%064d", 0)}, PlacementRequest{Pool: r.Pool, Execution: e})
}
func grantProfileMatches(g BrokerGrantSpec, r PlacementRequest, a StockProviderAuthority) bool {
	e := r.Execution
	e.Name = ""
	return g.Authority == a && g.Resources == r.Resources && reflect.DeepEqual(g.Execution, e) && contains(r.Hosts, g.Host)
}
func sqliteBrokerGrants(tx *sql.Tx) ([]BrokerGrant, error) {
	rows, e := tx.Query("SELECT grant_id,payload,payload_hash,state,COALESCE(request_key,'') FROM broker_grants WHERE payload!='' ORDER BY grant_id LIMIT 100001")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []BrokerGrant{}
	for rows.Next() {
		var g BrokerGrant
		var raw, hash string
		if e = rows.Scan(&g.ID, &raw, &hash, &g.State, &g.RequestKey); e != nil {
			return nil, e
		}
		if len(out) >= 100000 || !bindingFingerprint(g.ID) || len(raw) > 16384 || json.Unmarshal([]byte(raw), &g.Spec) != nil || !validGrantSpec(g.Spec) {
			return nil, ErrBrokerGrant
		}
		canonical, digest, e := grantHash(g.Spec)
		if e != nil || canonical != raw || digest != hash {
			return nil, ErrBrokerGrant
		}
		if (g.State == "consumed") != (g.RequestKey != "") {
			return nil, ErrBrokerGrant
		}
		out = append(out, g)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	if e = validateBrokerGrantHistory(tx, out); e != nil {
		return nil, e
	}
	return out, nil
}

// Every canonical DB allocation consumer uses this wrapper. Expired-but-unused
// grants remain charged until a durable terminal transition. Clock movement
// alone cannot release capacity.
func sqliteAllocationsWithGrants(tx *sql.Tx, c Config, r map[string]Reservation, i []InventoryResource, released map[string]bool) (Allocations, error) {
	out, e := sqliteAllocations(c, r, i, released)
	if e != nil {
		return out, e
	}
	grants, e := sqliteBrokerGrants(tx)
	if e != nil {
		return Allocations{}, e
	}
	for _, g := range grants {

		if g.State == "consumed" {
			plan, e := executionPlanRecord(tx, g.RequestKey)
			if e != nil {
				return Allocations{}, ErrBrokerGrant
			}
			if plan.Request.Host != g.Spec.Host || plan.HostBindingSequence != g.Spec.HostBindingSequence || !grantProfileMatches(g.Spec, PlacementRequest{Execution: plan.Execution, Resources: plan.Request.Resources, Hosts: []string{plan.Request.Host}}, g.Spec.Authority) {
				return Allocations{}, ErrBrokerGrant
			}
			var originRaw string
			if tx.QueryRow("SELECT payload FROM stock_provider_origins WHERE request_key=?", g.RequestKey).Scan(&originRaw) != nil {
				return Allocations{}, ErrBrokerGrant
			}
			var origin stockProviderOrigin
			if json.Unmarshal([]byte(originRaw), &origin) != nil || origin.Authority != g.Spec.Authority {
				return Allocations{}, ErrBrokerGrant
			}
			reservation, ok := r[g.RequestKey]
			if !ok {
				cancelled, e := cancellationRecord(tx, g.RequestKey)
				if e != nil {
					return Allocations{}, ErrBrokerGrant
				}
				reservation.Request = cancelled.Request
			}
			if reservation.Request.Host != g.Spec.Host || reservation.Request.Pool != g.Spec.Authority.PoolID || reservation.Request.Resources != g.Spec.Resources {
				return Allocations{}, ErrBrokerGrant
			}
			continue
		}
		if g.State != "unused" {
			continue
		}
		if _, ok := c.Hosts[g.Spec.Host]; !ok {
			return Allocations{}, ErrBrokerGrant
		}
		if _, ok := c.Pools[g.Spec.Authority.PoolID]; !ok {
			return Allocations{}, ErrBrokerGrant
		}
		h, p := out.Hosts[g.Spec.Host], out.Pools[g.Spec.Authority.PoolID]
		if !accumulate(&out.Global, g.Spec.Resources) || !accumulate(&h, g.Spec.Resources) || !accumulate(&p, g.Spec.Resources) {
			return Allocations{}, ErrBrokerGrant
		}
		out.Hosts[g.Spec.Host], out.Pools[g.Spec.Authority.PoolID] = h, p
	}
	records, e := sqliteNativeDomainOverheads(tx)
	if e != nil {
		return Allocations{}, e
	}
	if e = addDomainOverheads(&out, c, records); e != nil {
		return Allocations{}, e
	}
	return out, nil
}
func sqliteBrokerPolicy(tx *sql.Tx) (BrokerPolicy, error) {
	var p BrokerPolicy
	var raw []byte
	e := tx.QueryRow("SELECT revision,document FROM broker_policy WHERE id=1").Scan(&p.Revision, &raw)
	if e != nil {
		return p, e
	}
	if json.Unmarshal(raw, &p.Repositories) != nil || ValidateBrokerPolicy(p) != nil {
		return p, ErrBrokerGrant
	}
	return p, nil
}
func (s *SQLiteStore) ReadBrokerGrantState(ctx context.Context) (BrokerGrantState, error) {
	var v BrokerGrantState
	e := s.readTransaction(ctx, func(tx *sql.Tx) error {
		var e error
		v.Config, v.Ready, v.Epoch, _, e = sqliteMetadata(tx)
		if e != nil {
			return e
		}
		v.Ready = v.Ready && sqliteObservationsFresh(v.Config, s.now())
		if v.HostBindings, e = sqliteHostBindings(tx, v.Config); e != nil {
			return e
		}
		if v.PoolProfiles, e = sqliteBrokerPoolProfiles(tx); e != nil {
			return e
		}
		// Resolve the exact immutable enrollment referenced by each saved profile
		// inside this transaction, never via a later independently timed read.
		v.NativeDomains = map[int64]NativeDomainBinding{}
		for _, profile := range v.PoolProfiles {
			for _, ref := range profile.Domains {
				if _, exists := v.NativeDomains[ref.Sequence]; exists {
					continue
				}
				if len(v.NativeDomains) >= 1000 {
					return ErrBrokerGrant
				}
				domain, err := nativeDomainRecord(tx, ref.Sequence)
				if err != nil {
					return err
				}
				v.NativeDomains[ref.Sequence] = domain
			}
		}
		if v.Policy, e = sqliteBrokerPolicy(tx); e != nil {
			return e
		}
		if v.Reservations, e = sqliteReservations(tx); e != nil {
			return e
		}
		if v.Inventory, e = sqliteInventory(tx); e != nil {
			return e
		}
		if v.Released, e = sqliteReleasedKeys(tx); e != nil {
			return e
		}
		if v.Grants, e = sqliteBrokerGrants(tx); e != nil {
			return e
		}
		if v.DomainOverheads, e = sqliteNativeDomainOverheads(tx); e != nil {
			return e
		}
		if v.Allocated, e = sqliteAllocationsWithGrants(tx, v.Config, v.Reservations, v.Inventory, v.Released); e != nil {
			return e
		}
		v.ExecutionPlans = map[string]ExecutionPlan{}
		for _, g := range v.Grants {
			if g.State != "consumed" || v.Released[g.RequestKey] {
				continue
			}
			if _, active := v.Reservations[g.RequestKey]; !active {
				continue
			}
			p, err := executionPlanRecord(tx, g.RequestKey)
			if err != nil {
				return err
			}
			v.ExecutionPlans[g.RequestKey] = p
		}
		return tx.QueryRow("SELECT COALESCE(max(sequence),0) FROM audit").Scan(&v.Sequence)
	})
	return v, e
}

// IssueBrokerGrants validates one exact coherent read revision and commits all
// grants plus audit under BEGIN IMMEDIATE. Caller is the trusted broker, not a
// public API. Exact batch retry cannot allocate twice, even after terminal state.
func (s *SQLiteStore) IssueBrokerGrants(ctx context.Context, b BrokerGrantBatch) ([]BrokerGrant, error) {
	return s.issueBrokerGrants(ctx, b, 100000)
}

// A lower internal bound is used by boundary regression tests, never by ingress.
func (s *SQLiteStore) issueBrokerGrants(ctx context.Context, b BrokerGrantBatch, historyLimit int) ([]BrokerGrant, error) {
	if historyLimit < 1 || historyLimit > 100000 {
		return nil, ErrBrokerGrant
	}

	if !bindingFingerprint(b.ID) || b.ExpectedSequence < 0 || b.PolicyRevision < 0 || len(b.Grants) == 0 || len(b.Grants) > 1000 {
		return nil, ErrBrokerGrant
	}
	raw, hash, e := grantHash(b)
	if e != nil || len(raw) > 1<<20 {
		return nil, ErrBrokerGrant
	}
	// Own a detached canonical copy before entering the transaction.
	var owned BrokerGrantBatch
	if json.Unmarshal([]byte(raw), &owned) != nil {
		return nil, ErrBrokerGrant
	}
	b = owned
	for _, g := range b.Grants {
		if !validGrantSpec(g) {
			return nil, ErrBrokerGrant
		}
	}
	var result []BrokerGrant
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		var stored string
		err := tx.QueryRow("SELECT payload_hash FROM broker_grant_batches WHERE batch_id=?", b.ID).Scan(&stored)
		if err == nil {
			if stored != hash {
				return ErrConflict
			}
			if e := validateBrokerArchiveMetadata(tx); e != nil {
				return e
			}
			loaded, e := loadBrokerBatch(tx, b.ID)
			if e != nil {
				return e
			}
			result, e = brokerBatchGrantRows(tx, loaded)
			return e
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var grantCount, batchCount int
		if tx.QueryRow("SELECT count(*) FROM broker_grants WHERE payload!=''").Scan(&grantCount) != nil || tx.QueryRow("SELECT count(*) FROM broker_grant_batches WHERE payload!=''").Scan(&batchCount) != nil {
			return ErrBrokerGrant
		}
		if len(b.Grants) > historyLimit || grantCount > historyLimit-len(b.Grants) || batchCount >= historyLimit {
			return ErrBrokerGrant
		}
		var seq int64
		if tx.QueryRow("SELECT COALESCE(max(sequence),0) FROM audit").Scan(&seq) != nil || seq != b.ExpectedSequence {
			return ErrBrokerConflict
		}
		policy, e := sqliteBrokerPolicy(tx)
		if e != nil {
			return e
		}
		if policy.Revision != b.PolicyRevision {
			return ErrBrokerConflict
		}
		config, ready, epoch, _, e := sqliteMetadata(tx)
		if e != nil {
			return e
		}
		now := s.now()
		if !ready || epoch == int64(1<<63-1) || !sqliteObservationsFresh(config, now) {
			return ErrReconciliation
		}
		reservations, e := sqliteReservations(tx)
		if e != nil {
			return e
		}
		inventory, e := sqliteInventory(tx)
		if e != nil {
			return e
		}
		released, e := sqliteReleasedKeys(tx)
		if e != nil {
			return e
		}
		allocated, e := sqliteAllocationsWithGrants(tx, config, reservations, inventory, released)
		if e != nil {
			return e
		}
		bindings, e := sqliteHostBindings(tx, config)
		if e != nil {
			return e
		}
		for _, g := range b.Grants {
			host, ok := config.Hosts[g.Host]
			pool, pok := config.Pools[g.Authority.PoolID]
			binding, bok := bindings[g.Host]
			if !ok || !pok || !bok || binding.Sequence != g.HostBindingSequence || !contains(pool.Hosts, g.Host) || host.Mode != "enabled" || !host.Healthy || !g.ExpiresAt.After(now) || g.ExpiresAt.After(now.Add(24*time.Hour)) || !fits(allocated.Global, g.Resources, config.Global) || !fits(allocated.Hosts[g.Host], g.Resources, host.Budget) || !fits(allocated.Pools[g.Authority.PoolID], g.Resources, pool.Budget) {
				return ErrDenied
			}
			h, p := allocated.Hosts[g.Host], allocated.Pools[g.Authority.PoolID]
			if !accumulate(&allocated.Global, g.Resources) || !accumulate(&h, g.Resources) || !accumulate(&p, g.Resources) {
				return ErrBrokerGrant
			}
			allocated.Hosts[g.Host], allocated.Pools[g.Authority.PoolID] = h, p
		}
		if e = sqliteAudit(tx, "broker_grants_issued", b.ID, now); e != nil {
			return e
		}
		if e = tx.QueryRow("SELECT max(sequence) FROM audit").Scan(&seq); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO broker_grant_batches VALUES(?,?,?,?)", b.ID, raw, hash, seq); e != nil {
			return e
		}
		for index, g := range b.Grants {
			payload, digest, e := grantHash(g)
			if e != nil {
				return e
			}
			id := brokerGrantID(b.ID, index)
			if _, e = tx.Exec("INSERT INTO broker_grants VALUES(?,?,?,?,'unused',NULL)", id, b.ID, payload, digest); e != nil {
				return e
			}
			result = append(result, BrokerGrant{ID: id, Spec: g, State: "unused"})
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return result, nil
}
func brokerGrantID(batch string, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", batch, index)))
	return hex.EncodeToString(h[:])
}

// RetireUnusedBrokerGrant never releases a consumed/in-flight reservation. The
// transition is permanent, including when the wall clock subsequently rolls back.
func (s *SQLiteStore) RetireUnusedBrokerGrant(ctx context.Context, id string, expire bool) error {
	if !bindingFingerprint(id) {
		return ErrBrokerGrant
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		g, e := lookupBrokerGrant(tx, id)
		if e != nil {
			return e
		}
		target := "revoked"
		if expire {
			target = "expired"
		}
		if g.State == target {
			return nil
		}
		if g.State != "unused" || (expire && s.now().Before(g.Spec.ExpiresAt)) {
			return ErrBrokerGrant
		}
		if _, e = tx.Exec("UPDATE broker_grants SET state=? WHERE grant_id=? AND state='unused'", target, id); e != nil {
			return e
		}
		return sqliteAudit(tx, "broker_grant_"+target, id, s.now())
	})
}

func selectStockGrant(tx *sql.Tx, r PlacementRequest, a StockProviderAuthority, now time.Time, config Config, allocated Allocations) (BrokerGrant, error) {
	all, e := sqliteBrokerGrants(tx)
	if e != nil {
		return BrokerGrant{}, e
	}
	for _, g := range all {
		if g.State != "unused" || !g.Spec.ExpiresAt.After(now) || !grantProfileMatches(g.Spec, r, a) {
			continue
		}
		host, hostOK := config.Hosts[g.Spec.Host]
		pool, poolOK := config.Pools[r.Pool]
		// Redemption replaces an equal charge; it cannot evade reduced budgets.
		if !hostOK || !poolOK || host.Mode != "enabled" || !host.Healthy || !contains(pool.Hosts, g.Spec.Host) || !fits(allocated.Global, Resources{}, config.Global) || !fits(allocated.Pools[r.Pool], Resources{}, pool.Budget) || !fits(allocated.Hosts[g.Spec.Host], Resources{}, host.Budget) {
			continue
		}
		var sequence int64
		err := tx.QueryRow("SELECT bound_sequence FROM host_bindings WHERE host=?", g.Spec.Host).Scan(&sequence)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return BrokerGrant{}, err
		}
		if sequence != g.Spec.HostBindingSequence {
			continue
		}
		return g, nil
	}
	return BrokerGrant{}, ErrBrokerGrant
}
func subtractGrant(a, b Resources) Resources {
	return Resources{CPU: a.CPU - b.CPU, RAM: a.RAM - b.RAM, Disk: a.Disk - b.Disk, Runners: a.Runners - b.Runners}
}

func checkStockGrantLink(tx *sql.Tx, p ExecutionPlan, origin stockProviderOrigin) error {
	var legacy int
	if e := tx.QueryRow("SELECT count(*) FROM broker_grant_legacy_stock WHERE request_key=?", p.Request.Key).Scan(&legacy); e != nil {
		return e
	}
	if legacy == 1 {
		return nil
	}
	var id string
	if e := tx.QueryRow("SELECT grant_id FROM broker_grants WHERE request_key=?", p.Request.Key).Scan(&id); e != nil {
		return ErrBrokerGrant
	}
	g, e := lookupBrokerGrant(tx, id)
	if e != nil {
		return e
	}
	if g.State == "consumed" && g.RequestKey == p.Request.Key && g.Spec.Host == p.Request.Host && g.Spec.HostBindingSequence == p.HostBindingSequence && grantProfileMatches(g.Spec, PlacementRequest{Execution: p.Execution, Resources: p.Request.Resources, Hosts: []string{p.Request.Host}}, origin.Authority) {
		return nil
	}

	return ErrBrokerGrant
}

// Permanent issuance manifests make a missing grant fail closed instead of
// silently freeing a charge. The manifests and audit sequence are canonical.
func validateBrokerGrantHistory(tx *sql.Tx, grants []BrokerGrant) error {
	if e := validateBrokerArchiveMetadata(tx); e != nil {
		return e
	}
	var missing bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM audit a LEFT JOIN broker_grant_batches b ON b.sequence=a.sequence AND b.batch_id=a.request_key WHERE a.kind='broker_grants_issued' AND b.batch_id IS NULL LIMIT 1)`).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return ErrBrokerGrant
	}

	byID := map[string]BrokerGrant{}
	for _, g := range grants {
		byID[g.ID] = g
	}
	rows, e := tx.Query("SELECT b.batch_id,b.payload,b.payload_hash,a.kind,a.request_key FROM broker_grant_batches b LEFT JOIN audit a ON a.sequence=b.sequence WHERE b.payload!='' ORDER BY b.batch_id LIMIT 100001")
	if e != nil {
		return e
	}
	defer rows.Close()
	expected := 0
	batches := 0
	for rows.Next() {
		batches++
		if batches > 100000 {
			return ErrBrokerGrant
		}
		var id, raw, hash string
		var kind, key sql.NullString
		if e = rows.Scan(&id, &raw, &hash, &kind, &key); e != nil {
			return e
		}
		var b BrokerGrantBatch
		if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &b) != nil || b.ID != id || !bindingFingerprint(id) || len(b.Grants) == 0 || len(b.Grants) > 1000 || !kind.Valid || kind.String != "broker_grants_issued" || !key.Valid || key.String != id {
			return ErrBrokerGrant
		}
		canonical, digest, e := grantHash(b)
		if e != nil || canonical != raw || digest != hash {
			return ErrBrokerGrant
		}
		for i, spec := range b.Grants {
			expected++
			if expected > 100000 {
				return ErrBrokerGrant
			}
			g, ok := byID[brokerGrantID(id, i)]
			if !ok || !reflect.DeepEqual(g.Spec, spec) {
				return ErrBrokerGrant
			}
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if expected != len(grants) {
		return ErrBrokerGrant
	}
	return nil
}
