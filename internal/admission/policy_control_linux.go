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

// PolicyChange only edits existing ledger policy. It never registers a host or
// pool, contacts Garm, drains runners, deletes resources or releases charges.
type PolicyChange struct {
	Revision string     `json:"revision"`
	Action   string     `json:"action"`
	Target   string     `json:"target"`
	Budget   *Resources `json:"budget,omitempty"`
	Mode     string     `json:"mode,omitempty"`
	Reason   string     `json:"reason"`
}

// PolicyRevision excludes health observations, so periodic reconciliation does
// not invalidate an operator's unchanged policy view. All managed policy fields
// participate; JSON sorts map keys. Revision comparison happens inside the writer
// transaction, not in the HTTP handler.
func PolicyRevision(config Config) string {
	type hostPolicy struct {
		Budget Resources `json:"budget"`
		Mode   string    `json:"mode"`
	}
	type policy struct {
		Global Resources
		Hosts  map[string]hostPolicy
		Pools  map[string]Pool
		MaxAge int64
	}
	value := policy{config.Global, map[string]hostPolicy{}, config.Pools, int64(config.MaxObservationAge)}
	for id, host := range config.Hosts {
		value.Hosts[id] = hostPolicy{host.Budget, host.Mode}
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
	if len(change.Revision) != 64 || !validAdminText(change.Reason, 256) || len(change.Target) > 256 || (change.Target != "" && !validAdminText(change.Target, 256)) {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		config, _, _, _, err := sqliteMetadata(tx)
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
			if change.Target != "" || change.Budget == nil || change.Mode != "" {
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
				old = host.Budget
				host.Budget = *change.Budget
			} else {
				if change.Budget != nil || (change.Mode != "enabled" && change.Mode != "disabled") {
					return ErrDenied
				}
				old = host.Mode
				host.Mode = change.Mode
			}
			config.Hosts[change.Target] = host
		case "pool_budget":
			pool, ok := config.Pools[change.Target]
			if !ok || change.Mode != "" || change.Budget == nil {
				return ErrDenied
			}
			old = pool.Budget
			pool.Budget = *change.Budget
			config.Pools[change.Target] = pool
		default:
			return ErrDenied
		}
		if change.Budget != nil && (change.Budget.CPU > 9007199254740991 || change.Budget.RAM > 9007199254740991 || change.Budget.Runners > 9007199254740991) {
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
