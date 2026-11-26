package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

var ErrBrokerProfile = errors.New("immutable broker profile mapping unavailable or changed")

// These records describe validated configuration and observed stock ownership,
// NOT native exclusivity, current remote eligibility or activation permission.
type BrokerProfileDomain struct {
	Sequence, HostBindingSequence int64
	Host, DomainID, PolicyDigest  string
}
type BrokerPoolProfile struct {
	Sequence, Previous                                             int64
	Pool, Repository, SourceIdentity, PoolFingerprint, SizeProfile string
	ObservedAt                                                     time.Time
	Authority                                                      StockProviderAuthority
	Resources                                                      Resources
	Execution                                                      ExecutionImage
	Domains                                                        []BrokerProfileDomain
}
type BrokerPoolProfileRequest struct {
	// Optional compatibility guard; browser registration always supplies reviewed size.
	ExpectedResources           Resources
	ExpectedProfileSequence     int64
	SourceIdentity, SizeProfile string
	Authority                   StockProviderAuthority
	Execution                   ExecutionImage
}

const sqliteBrokerProfileSchema = `
CREATE TABLE broker_pool_profiles (
 record_sequence INTEGER PRIMARY KEY NOT NULL REFERENCES audit(sequence),
 pool TEXT NOT NULL,
 previous_sequence INTEGER NOT NULL CHECK(previous_sequence>=0),
 payload TEXT NOT NULL CHECK(length(payload)<=65536),
 payload_hash TEXT UNIQUE NOT NULL CHECK(length(payload_hash)=64),
 UNIQUE(pool,previous_sequence)
) STRICT;
CREATE TRIGGER broker_profile_no_update BEFORE UPDATE ON broker_pool_profiles BEGIN SELECT RAISE(ABORT,'broker profile revision immutable'); END;
CREATE TRIGGER broker_profile_no_delete BEFORE DELETE ON broker_pool_profiles BEGIN SELECT RAISE(ABORT,'broker profile history permanent'); END;
CREATE TRIGGER broker_provider_not_owner BEFORE INSERT ON native_domain_bindings
WHEN EXISTS(SELECT 1 FROM broker_pool_profiles WHERE json_extract(payload,'$.Authority.client_fingerprint') IN (json_extract(NEW.payload,'$.spec.owner_client_leaf'),json_extract(NEW.payload,'$.spec.owner_server_leaf')))
BEGIN SELECT RAISE(ABORT,'broker provider leaf cannot become native owner'); END;
CREATE TRIGGER broker_owner_not_provider BEFORE INSERT ON broker_pool_profiles
WHEN EXISTS(SELECT 1 FROM native_domain_bindings WHERE json_extract(NEW.payload,'$.Authority.client_fingerprint') IN (json_extract(payload,'$.spec.owner_client_leaf'),json_extract(payload,'$.spec.owner_server_leaf')))
BEGIN SELECT RAISE(ABORT,'native owner leaf cannot become broker provider'); END;
PRAGMA user_version=20;
`

func validBrokerProfile(v BrokerPoolProfile) bool {
	if v.Sequence <= 0 || v.Previous < 0 || v.Previous >= v.Sequence || !routeUUID(v.Pool) || !routeUUID(v.Repository) ||
		!bindingFingerprint(v.SourceIdentity) || !bindingFingerprint(v.PoolFingerprint) || v.ObservedAt.IsZero() ||
		!profileName(v.SizeProfile) || v.Execution.Name != "" || v.Authority.PoolID != v.Pool ||
		v.Resources.CPU <= 0 || v.Resources.CPU%1000 != 0 || v.Resources.RAM <= 0 || v.Resources.Disk <= 0 || v.Resources.Runners != 1 ||
		len(v.Domains) == 0 || len(v.Domains) > 1000 {
		return false
	}
	e := v.Execution
	e.Name = "profile-validation"
	r := PlacementRequest{Pool: v.Pool, Execution: e}
	if !validStockOrigin(stockProviderOrigin{Authority: v.Authority, CommandDigest: v.SourceIdentity}, r) {
		return false
	}
	req := Request{Key: executionKey(e, v.Pool), Pool: v.Pool, Host: v.Domains[0].Host, Resources: v.Resources}
	if !validExecution(e, req) {
		return false
	}
	last := ""
	for _, d := range v.Domains {
		if !bindingName(d.Host) || d.Host <= last || d.Sequence <= 0 || d.HostBindingSequence <= 0 || !bindingFingerprint(d.DomainID) || !bindingFingerprint(d.PolicyDigest) {
			return false
		}
		last = d.Host
	}
	return true
}

// Read and validate the append-only revision chains in sequence order. A record
// cannot be hidden by deleting its head: audit-to-record counts are checked too.
func sqliteBrokerPoolProfiles(tx *sql.Tx) (map[string]BrokerPoolProfile, error) {
	return readBrokerPoolProfiles(tx, nil)
}

func readBrokerPoolProfiles(tx *sql.Tx, ambiguousOwners map[string]bool) (map[string]BrokerPoolProfile, error) {
	out := map[string]BrokerPoolProfile{}
	rows, err := tx.Query("SELECT record_sequence,pool,previous_sequence,payload,payload_hash FROM broker_pool_profiles ORDER BY record_sequence")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq, prev int64
		var pool, raw, hash string
		if err = rows.Scan(&seq, &pool, &prev, &raw, &hash); err != nil {
			rows.Close()
			return nil, err
		}
		var v BrokerPoolProfile
		if len(raw) > 65536 || json.Unmarshal([]byte(raw), &v) != nil || !validBrokerProfile(v) || v.Sequence != seq || v.Pool != pool || v.Previous != prev || out[pool].Sequence != prev {
			rows.Close()
			return nil, ErrBrokerProfile
		}
		canonical, digest, e := grantHash(v)
		if e != nil || canonical != raw || digest != hash {
			rows.Close()
			return nil, ErrBrokerProfile
		}
		for _, d := range v.Domains {
			b, err := nativeDomainRecord(tx, d.Sequence)
			if err != nil || b.Spec.Controller != v.Authority.Controller || b.Spec.Host != d.Host || b.Spec.DomainID != d.DomainID || b.Spec.PolicyDigest != d.PolicyDigest || b.Spec.HostBindingSequence != d.HostBindingSequence {
				return nil, ErrBrokerProfile
			}
		}
		if old := out[pool]; ambiguousOwners != nil && old.Sequence != 0 && (old.Repository != v.Repository || old.SourceIdentity != v.SourceIdentity || old.Authority.Controller != v.Authority.Controller) {
			ambiguousOwners[pool] = true
		}
		out[pool] = v
		if len(out) > 1000 {
			rows.Close()
			return nil, ErrBrokerProfile
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var bad int
	if err = tx.QueryRow(`SELECT count(*) FROM broker_pool_profiles p LEFT JOIN audit a ON a.sequence=p.record_sequence
 WHERE a.sequence IS NULL OR a.kind!='broker_profile_recorded' OR a.request_key!=p.pool`).Scan(&bad); err != nil || bad != 0 {
		return nil, ErrBrokerProfile
	}
	if err = tx.QueryRow(`SELECT count(*) FROM audit a LEFT JOIN broker_pool_profiles p ON p.record_sequence=a.sequence
 WHERE a.kind='broker_profile_recorded' AND p.record_sequence IS NULL`).Scan(&bad); err != nil || bad != 0 {
		return nil, ErrBrokerProfile
	}
	return out, nil
}
func (s *SQLiteStore) CurrentBrokerPoolProfiles(ctx context.Context) (map[string]BrokerPoolProfile, error) {
	var out map[string]BrokerPoolProfile
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { var err error; out, err = sqliteBrokerPoolProfiles(tx); return err })
	return out, err
}
func (s *SQLiteStore) checkBrokerProfiles(ctx context.Context) error {
	_, err := s.CurrentBrokerPoolProfiles(ctx)
	return err
}

// RegisterBrokerPoolProfile captures one immutable mapping revision under CAS.
// Ownership is taken only from an opaque successful scoped stock read. Native
// policy hashes must equal real enrolled owner observations; sizes come from
// the saved profile in this same transaction, not caller-provided resource JSON.
// A source fingerprint is operator-configured endpoint identity, not an
// independently authenticated GARM controller identity or a remote write fence.
func (s *SQLiteStore) RegisterBrokerPoolProfile(ctx context.Context, r BrokerPoolProfileRequest, observed stockidentity.Observation, policies map[int64]nativeapi.Policy) (BrokerPoolProfile, error) {
	var out BrokerPoolProfile
	if (observed.ControllerID() != "" && observed.ControllerID() != r.Authority.Controller) || !observed.Valid() || r.ExpectedProfileSequence < 0 || !profileName(r.SizeProfile) || !bindingFingerprint(r.SourceIdentity) ||
		r.SourceIdentity != observed.SourceIdentity() || r.Execution.Name != "" || len(policies) == 0 || len(policies) > 1000 {
		return out, ErrBrokerProfile
	}
	frozenPolicies := make(map[int64]nativeapi.Policy, len(policies))
	for sequence, policy := range policies {
		frozenPolicies[sequence] = policy
	}
	policies = frozenPolicies
	pool := observed.Facts()
	if pool.Pool != r.Authority.PoolID || pool.Provider != r.Authority.ProviderName || pool.Flavor != r.Execution.Flavor || pool.Image != r.Execution.Image || pool.OSArch != r.Execution.Arch {
		return out, ErrBrokerProfile
	}
	// Snapshot caller slices before entering the transaction.
	r.Execution.Profiles = append([]string(nil), r.Execution.Profiles...)
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if observed.ObservedAt().After(now) || now.Sub(observed.ObservedAt()) > 30*time.Second {
			return ErrBrokerProfile
		}
		current, err := sqliteBrokerPoolProfiles(tx)
		if err != nil {
			return err
		}
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		var roleCollision int
		if err := tx.QueryRow("SELECT count(*) FROM native_domain_bindings WHERE json_extract(payload,'$.spec.owner_client_leaf')=? OR json_extract(payload,'$.spec.owner_server_leaf')=?", r.Authority.ClientFingerprint, r.Authority.ClientFingerprint).Scan(&roleCollision); err != nil || roleCollision != 0 {
			return ErrBrokerProfile
		}
		configPool, ok := config.Pools[pool.Pool]
		if !ok {
			return ErrBrokerProfile
		}
		bindings, err := sqliteHostBindings(tx, config)
		if err != nil {
			return err
		}
		v := BrokerPoolProfile{Previous: r.ExpectedProfileSequence, Pool: pool.Pool, Repository: pool.Repository, SourceIdentity: r.SourceIdentity,
			PoolFingerprint: pool.Fingerprint(), SizeProfile: r.SizeProfile, ObservedAt: observed.ObservedAt(), Authority: r.Authority, Execution: r.Execution, Resources: Resources{Runners: 1}}
		if err = tx.QueryRow("SELECT cpu,ram,disk FROM profiles WHERE name=?", r.SizeProfile).Scan(&v.Resources.CPU, &v.Resources.RAM, &v.Resources.Disk); err != nil {
			return ErrBrokerProfile
		}
		if r.ExpectedResources != (Resources{}) && r.ExpectedResources != v.Resources {
			return ErrBrokerProfile
		}
		for seq, policy := range policies {
			binding, err := nativeDomainRecord(tx, seq)
			if err != nil {
				return err
			}
			d := binding.Spec
			host, ok := bindings[d.Host]
			if !ok || d.HostBindingSequence != host.Sequence || !contains(configPool.Hosts, d.Host) ||
				d.Controller != r.Authority.Controller || policy.Controller() != d.Controller || policy.Host() != d.Host ||
				policy.DomainID() != d.DomainID || policy.Digest() != d.PolicyDigest ||
				r.Authority.ClientFingerprint == d.OwnerClientLeaf || r.Authority.ClientFingerprint == d.OwnerServerLeaf {
				return ErrBrokerProfile
			}
			e := r.Execution
			err = policy.ValidateAllocation(nativeapi.Allocation{Controller: d.Controller, Host: d.Host, Pool: pool.Pool, Name: "profile-validation",
				RequestKey: executionKey(ExecutionImage{Controller: d.Controller, Name: "profile-validation"}, pool.Pool),
				CPU:        v.Resources.CPU, RAM: v.Resources.RAM, Disk: v.Resources.Disk, Runners: 1,
				Flavor: e.Flavor, Image: e.Image, Fingerprint: e.Fingerprint, Type: e.Type, Arch: e.Arch, Profiles: append([]string(nil), e.Profiles...)})
			if err != nil {
				return ErrBrokerProfile
			}
			v.Domains = append(v.Domains, BrokerProfileDomain{Sequence: seq, HostBindingSequence: host.Sequence, Host: d.Host, DomainID: d.DomainID, PolicyDigest: d.PolicyDigest})
		}
		sort.Slice(v.Domains, func(i, j int) bool { return v.Domains[i].Host < v.Domains[j].Host })
		old := current[pool.Pool]
		// Exact retry of the same captured facts is read-only. It does not rebind
		// a profile, create a grant or renew any owner's authority.
		if old.Previous == r.ExpectedProfileSequence && old.Sequence != 0 {
			v.Sequence = old.Sequence
			if reflect.DeepEqual(old, v) {
				out = old
				return nil
			}
		}
		if old.Sequence != r.ExpectedProfileSequence {
			return ErrBrokerProfile
		}
		if err = sqliteAudit(tx, "broker_profile_recorded", pool.Pool, now); err != nil {
			return err
		}
		if err = tx.QueryRow("SELECT max(sequence) FROM audit").Scan(&v.Sequence); err != nil {
			return err
		}
		if !validBrokerProfile(v) {
			return ErrBrokerProfile
		}
		raw, digest, err := grantHash(v)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO broker_pool_profiles(record_sequence,pool,previous_sequence,payload,payload_hash)VALUES(?,?,?,?,?)", v.Sequence, v.Pool, v.Previous, raw, digest); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}
