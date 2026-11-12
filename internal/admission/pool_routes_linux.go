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
	"strings"
	"time"
)

var ErrPoolRoute = errors.New("inactive pool route registration unavailable")

// Inactive-only schema9 storage. No activation API, dynamic runtime resolver or
// HTTP control exists yet; installing storage grants no creation capability.
const sqliteInactivePoolRouteSchema = `
CREATE TABLE pool_routes (
 pool_id TEXT PRIMARY KEY NOT NULL,
 request_id TEXT UNIQUE NOT NULL,
 payload TEXT NOT NULL CHECK(length(payload)<=32768),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 identity_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence),
 mode TEXT NOT NULL CHECK(mode='inactive')
) STRICT;
CREATE INDEX pool_route_execution_history ON execution_plans(json_extract(payload,'$.request.pool'));
CREATE TRIGGER pool_routes_no_update BEFORE UPDATE ON pool_routes
BEGIN SELECT RAISE(ABORT,'pool route identity is immutable'); END;
CREATE TRIGGER pool_routes_no_delete BEFORE DELETE ON pool_routes
BEGIN SELECT RAISE(ABORT,'pool route history is permanent'); END;
CREATE TRIGGER inactive_pool_no_new_reservation BEFORE INSERT ON reservations
WHEN EXISTS(SELECT 1 FROM pool_routes WHERE pool_id=NEW.pool AND mode='inactive')
BEGIN SELECT RAISE(ABORT,'pool route inactive'); END;
CREATE TRIGGER inactive_pool_no_new_dispatch BEFORE INSERT ON dispatches
WHEN EXISTS(SELECT 1 FROM reservations r JOIN pool_routes p ON p.pool_id=r.pool WHERE r.request_key=NEW.reservation_key AND p.mode='inactive')
BEGIN SELECT RAISE(ABORT,'pool route inactive'); END;
PRAGMA user_version=9;
`

// DisabledPoolFacts is returned ONLY by a trusted source adapter which looks up
// the exact CONFIRMED creation intent in its live bound journal, checks the global
// unresolved-write barrier, and freshly reads the actual authenticated disabled
// pool. Browser StagedPool JSON/booleans/GET similarity cannot implement it.
// It contains no credentials/JIT material or arbitrary upstream response bodies.
type DisabledPoolFacts struct {
	Protocol           string
	ManagementIdentity string
	JournalLineage     string
	RequestID          string
	PoolID             string
	RepositoryID       string
	ProviderName       string
	ForgeEndpointName  string
	Generation         uint64
	SpecFingerprint    string
	PoolFingerprint    string
	Image              string
	Flavor             string
	OSType             string
	OSArch             string
	TemplateID         uint64
	ShellEnabled       bool
	ExtraSpecsEmpty    bool
	Enabled            bool
	MinIdleRunners     uint64
	MaxRunners         uint64
	ObservedAt         time.Time
}
type DisabledPoolRegistrationSource interface {
	ConfirmedDisabledPool(context.Context, string) (DisabledPoolFacts, error)
}

// PoolRouteTemplate comes only from independently protected startup catalogs.
// Operator references resolve to exact immutable values, never caller URLs,
// credentials, scripts, arbitrary hosts, entity IDs or new source leaf grants.
type PoolRouteTemplate struct {
	Reference          string
	Revision           string
	ProviderName       string
	ForgeEndpointName  string
	JITEnabled         *bool
	RepositoryIDs      []string
	HostGroup          string
	Hosts              []string
	SourceActorGroup   string
	SourceFingerprints []string
	Execution          ExecutionImage // Controller/Name are filled by this trusted registrar.
	PerRunner          Resources
	MaxRunners         uint64
	Strategy           string
}
type PoolRegistrar struct {
	store                                          *SQLiteStore
	source                                         DisabledPoolRegistrationSource
	controller, managementIdentity, journalLineage string
	templates                                      map[string]PoolRouteTemplate
}

func routeUUID(value string) bool {
	if len(value) != 36 || value == "00000000-0000-0000-0000-000000000000" || strings.ToLower(value) != value {
		return false
	}
	if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(raw) == 16
}
func sortedRouteNames(values []string, max int) ([]string, bool) {
	if len(values) == 0 || len(values) > max {
		return nil, false
	}
	owned := append([]string(nil), values...)
	sort.Strings(owned)
	for i, value := range owned {
		if !bindingName(value) || (i > 0 && owned[i-1] == value) {
			return nil, false
		}
	}
	return owned, true
}
func routeTemplateDigest(t PoolRouteTemplate) string {
	raw, _ := json.Marshal(t)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func routeBudget(t PoolRouteTemplate, runners uint64) (Resources, bool) {
	const maximum = int64(9007199254740991)
	if runners == 0 || runners > t.MaxRunners || runners > uint64(maximum) || t.PerRunner.Runners != 1 || t.PerRunner.CPU <= 0 || t.PerRunner.RAM <= 0 || uint64(t.PerRunner.CPU) > uint64(maximum)/runners || uint64(t.PerRunner.RAM) > uint64(maximum)/runners {
		return Resources{}, false
	}
	return Resources{CPU: t.PerRunner.CPU * int64(runners), RAM: t.PerRunner.RAM * int64(runners), Runners: int64(runners)}, true
}
func normalizeRouteTemplate(t PoolRouteTemplate, controller string) (PoolRouteTemplate, error) {
	if !bindingName(t.Reference) || !bindingFingerprint(t.Revision) || !bindingName(t.ProviderName) || !bindingName(t.ForgeEndpointName) || len(t.ForgeEndpointName) > 128 || t.JITEnabled == nil || !bindingName(t.HostGroup) || !bindingName(t.SourceActorGroup) || (t.Strategy != "first_fit" && t.Strategy != "least_runners") {
		return PoolRouteTemplate{}, ErrPoolRoute
	}
	var ok bool
	t.Hosts, ok = sortedRouteNames(t.Hosts, 128)
	if !ok {
		return PoolRouteTemplate{}, ErrPoolRoute
	}
	t.RepositoryIDs, ok = sortedRouteNames(t.RepositoryIDs, 1000)
	if !ok {
		return PoolRouteTemplate{}, ErrPoolRoute
	}
	for _, id := range t.RepositoryIDs {
		if !routeUUID(id) {
			return PoolRouteTemplate{}, ErrPoolRoute
		}
	}
	t.SourceFingerprints, ok = sortedRouteNames(t.SourceFingerprints, 64)
	if !ok {
		return PoolRouteTemplate{}, ErrPoolRoute
	}
	for _, fp := range t.SourceFingerprints {
		if !bindingFingerprint(fp) {
			return PoolRouteTemplate{}, ErrPoolRoute
		}
	}
	t.Execution.Profiles = append([]string(nil), t.Execution.Profiles...)
	jit := *t.JITEnabled
	t.JITEnabled = &jit
	t.Execution.Controller = controller
	t.Execution.Name = "route-policy-validation"
	req := Request{Pool: "route-policy-validation", Host: t.Hosts[0], Resources: t.PerRunner}
	req.Key = executionKey(t.Execution, req.Pool)
	if t.Execution.Type != "container" || !validRequest(req) || !validExecution(t.Execution, req) {
		return PoolRouteTemplate{}, ErrPoolRoute
	}
	if _, ok := routeBudget(t, t.MaxRunners); !ok {
		return PoolRouteTemplate{}, ErrPoolRoute
	}

	return t, nil
}
func cloneRouteTemplate(t PoolRouteTemplate) PoolRouteTemplate {
	t.Hosts = append([]string(nil), t.Hosts...)
	t.RepositoryIDs = append([]string(nil), t.RepositoryIDs...)
	t.SourceFingerprints = append([]string(nil), t.SourceFingerprints...)
	t.Execution.Profiles = append([]string(nil), t.Execution.Profiles...)
	if t.JITEnabled != nil {
		jit := *t.JITEnabled
		t.JITEnabled = &jit
	}
	return t
}
func NewPoolRegistrar(store *SQLiteStore, source DisabledPoolRegistrationSource, controller, managementIdentity, journalLineage string, templates []PoolRouteTemplate) (*PoolRegistrar, error) {
	if store == nil || source == nil || !bindingName(controller) || !bindingFingerprint(managementIdentity) || !bindingFingerprint(journalLineage) || len(templates) == 0 || len(templates) > 128 {
		return nil, ErrPoolRoute
	}
	result := &PoolRegistrar{store: store, source: source, controller: controller, managementIdentity: managementIdentity, journalLineage: journalLineage, templates: map[string]PoolRouteTemplate{}}
	for _, template := range templates {
		t, err := normalizeRouteTemplate(template, controller)
		if err != nil || result.templates[t.Reference].Reference != "" {
			return nil, ErrPoolRoute
		}
		result.templates[t.Reference] = t
	}
	return result, nil
}

// PoolRouteIdentity is canonical immutable NONSECRET enrollment history. It does
// NOT prove native/whole-host scope, external writer fencing or job safety.
type PoolRouteIdentity struct {
	Protocol             string            `json:"protocol"`
	Controller           string            `json:"controller"`
	ManagementIdentity   string            `json:"management_identity"`
	JournalLineage       string            `json:"journal_lineage"`
	RequestID            string            `json:"request_id"`
	PoolID               string            `json:"pool_id"`
	RepositoryID         string            `json:"repository_id"`
	ProviderName         string            `json:"provider_name"`
	Generation           uint64            `json:"generation"`
	SpecFingerprint      string            `json:"spec_fingerprint"`
	PoolFingerprint      string            `json:"pool_fingerprint"`
	Template             PoolRouteTemplate `json:"template"`
	TemplateDigest       string            `json:"template_digest"`
	Budget               Resources         `json:"budget"`
	HostBindingSequences map[string]int64  `json:"host_binding_sequences"`
	IdentitySequence     int64             `json:"identity_sequence"`
	RegisteredAt         time.Time         `json:"registered_at"`
}
type PoolRouteRecord struct {
	Identity PoolRouteIdentity
	Mode     string
	Replay   bool
}

func validRouteIdentity(r PoolRouteIdentity) bool {
	normalized, err := normalizeRouteTemplate(r.Template, r.Controller)
	if err != nil || !reflect.DeepEqual(normalized, r.Template) {
		return false
	}
	if r.Protocol != "inactive-pool-route/v1" || !bindingName(r.Controller) || !bindingFingerprint(r.ManagementIdentity) || !bindingFingerprint(r.JournalLineage) || !routeUUID(r.RequestID) || !routeUUID(r.PoolID) || !routeUUID(r.RepositoryID) || r.ProviderName != r.Template.ProviderName || !contains(r.Template.RepositoryIDs, r.RepositoryID) || !bindingFingerprint(r.SpecFingerprint) || !bindingFingerprint(r.PoolFingerprint) || r.TemplateDigest != routeTemplateDigest(r.Template) || r.IdentitySequence <= 0 || r.RegisteredAt.IsZero() || r.Template.Execution.Controller != r.Controller || r.Template.Execution.Name != "route-policy-validation" || len(r.HostBindingSequences) != len(r.Template.Hosts) {
		return false
	}
	budget, ok := routeBudget(r.Template, uint64(r.Budget.Runners))
	if !ok || budget != r.Budget {
		return false
	}
	for _, host := range r.Template.Hosts {
		if r.HostBindingSequences[host] <= 0 {
			return false
		}
	}
	return true
}
func poolRouteRecord(tx *sql.Tx, pool string) (PoolRouteRecord, error) {
	var r PoolRouteRecord
	var raw, hash, request string
	var sequence int64
	if err := tx.QueryRow("SELECT request_id,payload,payload_hash,identity_sequence,mode FROM pool_routes WHERE pool_id=?", pool).Scan(&request, &raw, &hash, &sequence, &r.Mode); err != nil {
		return r, err
	}
	sum := sha256.Sum256([]byte(raw))
	if len(raw) > 32768 || hex.EncodeToString(sum[:]) != hash || json.Unmarshal([]byte(raw), &r.Identity) != nil || !validRouteIdentity(r.Identity) || r.Identity.PoolID != pool || r.Identity.RequestID != request || r.Identity.IdentitySequence != sequence || r.Mode != "inactive" {
		return PoolRouteRecord{}, ErrPoolRoute
	}
	canonical, err := json.Marshal(r.Identity)
	if err != nil || string(canonical) != raw {
		return PoolRouteRecord{}, ErrPoolRoute
	}
	var kind, subject string
	if tx.QueryRow("SELECT kind,request_key FROM audit WHERE sequence=?", sequence).Scan(&kind, &subject) != nil || kind != "pool_route_registered_inactive" || subject != pool {
		return PoolRouteRecord{}, ErrPoolRoute
	}
	return r, nil
}
func (s *SQLiteStore) LookupPoolRoute(ctx context.Context, pool string) (PoolRouteRecord, error) {
	var r PoolRouteRecord
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { var err error; r, err = poolRouteRecord(tx, pool); return err })
	if err != nil {
		return PoolRouteRecord{}, err
	}
	return r, nil
}
func (r *PoolRegistrar) historicalReplay(ctx context.Context, requestID string, template PoolRouteTemplate) (PoolRouteRecord, error) {
	var result PoolRouteRecord
	err := r.store.readTransaction(ctx, func(tx *sql.Tx) error {
		var pool string
		if err := tx.QueryRow("SELECT pool_id FROM pool_routes WHERE request_id=?", requestID).Scan(&pool); err != nil {
			return err
		}
		record, err := poolRouteRecord(tx, pool)
		if err != nil {
			return err
		}
		identity := record.Identity
		if identity.Controller != r.controller || identity.ManagementIdentity != r.managementIdentity || identity.JournalLineage != r.journalLineage || identity.RequestID != requestID || identity.TemplateDigest != routeTemplateDigest(template) {
			return ErrConflict
		}
		record.Replay = true
		result = record
		return nil
	})
	if err != nil {
		return PoolRouteRecord{}, err
	}
	return result, nil
}

// RegisterInactive is the ONLY write API in this foundation: no activation,
// deletion, arbitrary-proof input or native call. The source adapter is fixed by
// trusted server construction. Dynamic runtime resolution and HTTP/UI glue are
// not supplied by this storage slice.
func (r *PoolRegistrar) RegisterInactive(ctx context.Context, requestID, templateRef, expectedRevision string) (PoolRouteRecord, error) {
	if !routeUUID(requestID) || !bindingFingerprint(expectedRevision) {
		return PoolRouteRecord{}, ErrPoolRoute
	}
	template, ok := r.templates[templateRef]
	template = cloneRouteTemplate(template)
	if !ok {
		return PoolRouteRecord{}, ErrPoolRoute
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Historical exact replay grants no new state/capacity and never refreshes
	// source facts, changes identity, activates or resolves a remote unknown.
	if old, err := r.historicalReplay(bounded, requestID, template); err == nil {
		return old, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return PoolRouteRecord{}, err
	}
	facts, err := r.source.ConfirmedDisabledPool(bounded, requestID)
	if err != nil || facts.Protocol != "native-disabled-repository-pool-v1" || facts.ManagementIdentity != r.managementIdentity || facts.JournalLineage != r.journalLineage || facts.RequestID != requestID || !routeUUID(facts.PoolID) || !routeUUID(facts.RepositoryID) || !contains(template.RepositoryIDs, facts.RepositoryID) || facts.ProviderName != template.ProviderName || facts.ForgeEndpointName != template.ForgeEndpointName || facts.Generation > uint64(1<<63-1) || !bindingFingerprint(facts.SpecFingerprint) || !bindingFingerprint(facts.PoolFingerprint) || facts.Enabled || facts.MinIdleRunners != 0 || facts.TemplateID != 0 || facts.ShellEnabled || !facts.ExtraSpecsEmpty || facts.OSType != "linux" || facts.OSArch != template.Execution.Arch || facts.Image != template.Execution.Image || facts.Flavor != template.Execution.Flavor {
		return PoolRouteRecord{}, ErrPoolRoute
	}
	budget, ok := routeBudget(template, facts.MaxRunners)
	if !ok {
		return PoolRouteRecord{}, ErrPoolRoute
	}
	var result PoolRouteRecord
	err = r.store.transaction(bounded, func(tx *sql.Tx) error {
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		now := r.store.now()
		if facts.ObservedAt.IsZero() || facts.ObservedAt.After(now) || now.Sub(facts.ObservedAt) > config.MaxObservationAge {
			return ErrPoolRoute
		}
		if old, err := poolRouteRecord(tx, facts.PoolID); err == nil {
			identity := old.Identity
			if identity.RequestID != requestID || identity.Controller != r.controller || identity.ManagementIdentity != r.managementIdentity || identity.JournalLineage != r.journalLineage || identity.RepositoryID != facts.RepositoryID || identity.ProviderName != facts.ProviderName || identity.Generation != facts.Generation || identity.SpecFingerprint != facts.SpecFingerprint || identity.PoolFingerprint != facts.PoolFingerprint || identity.TemplateDigest != routeTemplateDigest(template) || identity.Budget != budget {
				return ErrConflict
			}
			old.Replay = true
			result = old
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if PolicyRevision(config) != expectedRevision {
			return ErrPolicyConflict
		}
		if _, exists := config.Pools[facts.PoolID]; exists {
			return ErrConflict
		}
		var previous bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pool_routes WHERE request_id=? UNION ALL SELECT 1 FROM reservations WHERE pool=? UNION ALL SELECT 1 FROM inventory WHERE pool=? UNION ALL SELECT 1 FROM execution_plans WHERE json_extract(payload,'$.request.pool')=? LIMIT 1)`, requestID, facts.PoolID, facts.PoolID, facts.PoolID).Scan(&previous); err != nil {
			return err
		}
		if previous {
			return ErrConflict
		}
		sequences := map[string]int64{}
		for _, host := range template.Hosts {
			if _, ok := config.Hosts[host]; !ok {
				return ErrHostBinding
			}
			binding, err := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", host).Scan)
			if err != nil || binding.Sequence <= 0 {
				return ErrHostBinding
			}
			var legacy bool
			if tx.QueryRow("SELECT EXISTS(SELECT 1 FROM host_binding_history_exclusions WHERE host=?)", host).Scan(&legacy) != nil || legacy {
				return ErrHostBinding
			}
			sequences[host] = binding.Sequence
		}
		config.Pools[facts.PoolID] = Pool{Budget: budget, Hosts: append([]string(nil), template.Hosts...)}
		if validateConfig(config) != nil {
			return ErrPoolRoute
		}
		if err := sqliteAudit(tx, "pool_route_registered_inactive", facts.PoolID, now); err != nil {
			return err
		}
		var sequence int64
		if tx.QueryRow("SELECT last_insert_rowid()").Scan(&sequence) != nil || sequence <= 0 {
			return ErrPoolRoute
		}
		identity := PoolRouteIdentity{Protocol: "inactive-pool-route/v1", Controller: r.controller, ManagementIdentity: r.managementIdentity, JournalLineage: r.journalLineage, RequestID: requestID, PoolID: facts.PoolID, RepositoryID: facts.RepositoryID, ProviderName: facts.ProviderName, Generation: facts.Generation, SpecFingerprint: facts.SpecFingerprint, PoolFingerprint: facts.PoolFingerprint, Template: template, TemplateDigest: routeTemplateDigest(template), Budget: budget, HostBindingSequences: sequences, IdentitySequence: sequence, RegisteredAt: now.UTC()}
		if !validRouteIdentity(identity) {
			return ErrPoolRoute
		}
		raw, err := json.Marshal(identity)
		if err != nil || len(raw) > 32768 {
			return ErrPoolRoute
		}
		sum := sha256.Sum256(raw)
		if _, err := tx.Exec("INSERT INTO pool_routes(pool_id,request_id,payload,payload_hash,identity_sequence,mode) VALUES(?,?,?,?,?,'inactive')", facts.PoolID, requestID, string(raw), hex.EncodeToString(sum[:]), sequence); err != nil {
			return err
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE metadata SET config=?,ready=0,nonce='',began_at='' WHERE id=1", encoded); err != nil {
			return err
		}
		result = PoolRouteRecord{Identity: identity, Mode: "inactive"}
		return nil
	})
	if err != nil {
		return PoolRouteRecord{}, err
	}
	return result, nil
}

// Equal intentionally excludes read-time freshness; it never implies live source
// approval or activation. Used only by trusted tests/coordinators for exact identity.
func (r PoolRouteRecord) Equal(other PoolRouteRecord) bool {
	return reflect.DeepEqual(r.Identity, other.Identity) && r.Mode == other.Mode
}

// sqliteCheckPoolRoutes validates inactive catalog history on a closed export.
// It does not contact Garm, resolve/activate private references or remove any
// unexpected allocation. Whole-ledger accounting checks remain mandatory.
func sqliteCheckPoolRoutes(ctx context.Context, s *SQLiteStore) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error { _, _, err := poolRouteCatalogRecords(tx); return err })
}
