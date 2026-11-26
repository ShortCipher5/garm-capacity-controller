package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrPolicyConflict = errors.New("policy changed; refresh before retrying")
var ErrHostActivation = errors.New("host placement prerequisites are unavailable")
var ErrHostDraftRemoval = errors.New("disabled server draft still has a binding or retained state")

// PolicyChange edits existing ledger policy and can discard an unused disabled
// discovery enrollment. It never contacts Garm or deletes resources.
type PolicyChange struct {
	Revision     string     `json:"revision"`
	Action       string     `json:"action"`
	Target       string     `json:"target"`
	Budget       *Resources `json:"budget,omitempty"`
	CPUCapMillis *int64     `json:"cpu_cap_millis,omitempty"`
	Mode         string     `json:"mode,omitempty"`
	Reason       string     `json:"reason"`
}

// PolicyRevision excludes health observations, so periodic reconciliation does
// not invalidate an operator's unchanged policy view. All managed policy fields
// participate; JSON sorts map keys. Revision comparison happens inside the writer
// transaction, not in the HTTP handler.
func PolicyRevision(config Config) string {
	type hostPolicy struct {
		Budget       Resources `json:"budget"`
		Mode         string    `json:"mode"`
		CPUCapMillis int64     `json:"cpu_cap_millis,omitempty"`
	}
	type policy struct {
		Global Resources
		Hosts  map[string]hostPolicy
		Pools  map[string]Pool
		MaxAge int64
	}
	value := policy{config.Global, map[string]hostPolicy{}, config.Pools, int64(config.MaxObservationAge)}
	for id, host := range config.Hosts {
		value.Hosts[id] = hostPolicy{host.Budget, host.Mode, host.CPUCapMillis}
	}
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validAdminText(value string, max int) bool {
	if strings.TrimSpace(value) == "" || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ApplyPolicyChange serializes configuration, readiness and the durable audit in
// one BEGIN IMMEDIATE transaction shared with admission. Lowering a budget below
// charged allocation is allowed, but blocks further claims, never frees charges.
// Every effective edit closes readiness and invalidates any collector fence.
func (s *SQLiteStore) ApplyPolicyChange(ctx context.Context, change PolicyChange) error {
	return s.applyPolicyChange(ctx, change, false, false)
}

// ApplyAdminPolicyChange restricts browser activation to configured placement
// candidates with a verified daemon binding and a fresh reconciled inventory.
// A successful change still requires a new reconciliation before admission.
func (s *SQLiteStore) ApplyAdminPolicyChange(ctx context.Context, change PolicyChange, provisioning bool) error {
	return s.applyPolicyChange(ctx, change, true, provisioning)
}

func (s *SQLiteStore) applyPolicyChange(ctx context.Context, change PolicyChange, managed, provisioning bool) error {
	if len(change.Revision) != 64 || !validAdminText(change.Reason, 256) || len(change.Target) > 256 || (change.Target != "" && !validAdminText(change.Target, 256)) {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		config, ready, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		before := PolicyRevision(config)
		if change.Revision != before {
			return ErrPolicyConflict
		}
		var old any
		switch change.Action {
		case "global_budget":
			if change.Target != "" || change.Budget == nil || change.Mode != "" || change.CPUCapMillis != nil {
				return ErrDenied
			}
			old = config.Global
			config.Global = *change.Budget
		case "host_budget", "host_mode":
			host, ok := config.Hosts[change.Target]
			if !ok {
				return ErrDenied
			}
			if change.Action == "host_budget" {
				if change.Budget == nil || change.Mode != "" {
					return ErrDenied
				}
				if change.CPUCapMillis != nil {
					// The cap is display-only today, but it must never exceed the
					// host CPU budget it annotates. Zero clears the cap.
					if *change.CPUCapMillis < 0 || (*change.CPUCapMillis > 0 && *change.CPUCapMillis > change.Budget.CPU) {
						return ErrDenied
					}
					host.CPUCapMillis = *change.CPUCapMillis
				}
				old = host.Budget
				host.Budget = *change.Budget
			} else {
				if change.Budget != nil || change.CPUCapMillis != nil || (change.Mode != "enabled" && change.Mode != "disabled" && change.Mode != "draining") {
					return ErrDenied
				}
				if managed && change.Mode == "enabled" && host.Mode != "enabled" {
					if err := s.checkHostActivation(tx, config, change.Target, ready && provisioning); err != nil {
						return err
					}
				}
				old = host.Mode
				host.Mode = change.Mode
			}
			config.Hosts[change.Target] = host
		case "host_draft_remove":
			if change.Budget != nil || change.CPUCapMillis != nil || change.Mode != "" {
				return ErrDenied
			}
			host, exists := config.Hosts[change.Target]
			if !exists || provisioning || host.Mode != "disabled" || host.Healthy ||
				len(config.Hosts) <= 1 || hostHasPool(config, change.Target) {
				return ErrHostDraftRemoval
			}
			if err := checkHostDraftRemoval(tx, change.Target); err != nil {
				return err
			}
			old = host
			delete(config.Hosts, change.Target)
			if err := sqliteAudit(tx, "host_draft_removed", change.Target, s.now()); err != nil {
				return err
			}
		case "pool_budget":
			pool, ok := config.Pools[change.Target]
			if !ok || change.Mode != "" || change.Budget == nil || change.CPUCapMillis != nil {
				return ErrDenied
			}
			old = pool.Budget
			pool.Budget = *change.Budget
			config.Pools[change.Target] = pool
		default:
			return ErrDenied
		}
		if change.Budget != nil && (change.Budget.CPU > 9007199254740991 || change.Budget.RAM > 9007199254740991 || change.Budget.Disk > 9007199254740991 || change.Budget.Runners > 9007199254740991) {
			return ErrDenied
		}
		if validateConfig(config) != nil {
			return ErrDenied
		}
		if PolicyRevision(config) == before {
			return nil
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE metadata SET config=?,ready=0,nonce='',began_at='' WHERE id=1", encoded); err != nil {
			return err
		}
		audit, err := json.Marshal(struct {
			Actor  string       `json:"actor"`
			Before any          `json:"before"`
			Change PolicyChange `json:"change"`
		}{"management-token", old, change})
		if err != nil {
			return err
		}
		return sqliteAudit(tx, "admin_policy_change", string(audit), s.now())
	})
}

func (s *SQLiteStore) checkHostActivation(tx *sql.Tx, config Config, id string, ready bool) error {
	host := config.Hosts[id]
	age := s.now().Sub(host.ObservedAt)
	if !ready || !host.Healthy || age < 0 || age > config.MaxObservationAge ||
		host.Budget.CPU <= 0 || host.Budget.RAM <= 0 || host.Budget.Runners <= 0 {
		return ErrHostActivation
	}
	bindings, err := sqliteHostBindings(tx, config)
	if err != nil {
		return err
	}
	if _, ok := bindings[id]; !ok {
		return ErrHostActivation
	}
	var excluded bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM host_binding_history_exclusions WHERE host=?)", id).Scan(&excluded); err != nil {
		return err
	}
	if excluded {
		return ErrHostActivation
	}
	if !hostHasPool(config, id) {
		return ErrHostActivation
	}
	return nil
}

func hostHasPool(config Config, id string) bool {
	for _, pool := range config.Pools {
		if contains(pool.Hosts, id) {
			return true
		}
	}
	return false
}

// Require a consumed discovery review and no retained state associated with
// the draft. Bindings and immutable enrollment history survive lifecycle edits.
func checkHostDraftRemoval(tx *sql.Tx, id string) error {
	const query = `SELECT EXISTS(SELECT 1 FROM onboarding_reviews WHERE consumed_by=:host),
		EXISTS(
			SELECT 1 FROM host_bindings WHERE host=:host
			UNION ALL SELECT 1 FROM host_binding_history_exclusions WHERE host=:host
			UNION ALL SELECT 1 FROM reservations WHERE host=:host
			UNION ALL SELECT 1 FROM inventory WHERE host=:host
			UNION ALL SELECT 1 FROM native_domain_bindings WHERE host=:host
			UNION ALL SELECT 1 FROM native_domain_overheads WHERE host=:host
			UNION ALL SELECT 1 FROM native_artifact_overheads WHERE host=:host
			UNION ALL SELECT 1 FROM runner_mixes WHERE host=:host
			UNION ALL SELECT 1 FROM usage_samples WHERE host=:host
			UNION ALL SELECT 1 FROM audit WHERE kind IN ('host_detected','host_draft_removed') AND request_key=:host
		)`
	var discovery, retained bool
	if err := tx.QueryRow(query, sql.Named("host", id)).Scan(&discovery, &retained); err != nil {
		return err
	}
	if !discovery || retained {
		return ErrHostDraftRemoval
	}
	return nil
}
