package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Broker policy is advisory configuration only. It does not enroll a pool,
// grant capacity, change GARM or remove any existing resource charge.
type BrokerRepositoryPolicy struct {
	ID         string             `json:"id"`
	Weight     uint32             `json:"weight"`
	MaxRunners int64              `json:"max_runners"`
	Pools      []BrokerPoolPolicy `json:"pools"`
}
type BrokerPoolPolicy struct {
	ID         string `json:"id"`
	MaxRunners int64  `json:"max_runners"`
	WarmIdle   int64  `json:"warm_idle"`
}
type BrokerPolicy struct {
	Revision     int64                    `json:"revision"`
	Repositories []BrokerRepositoryPolicy `json:"repositories"`
}

var ErrBrokerConflict = errors.New("advisory policy revision changed")

const sqliteBrokerPolicySchema = `
CREATE TABLE IF NOT EXISTS broker_policy (
 id INTEGER PRIMARY KEY CHECK(id=1),
 revision INTEGER NOT NULL CHECK(revision>=0),
 document BLOB NOT NULL
) STRICT;
INSERT OR IGNORE INTO broker_policy VALUES(1,0,CAST('[]' AS BLOB));
PRAGMA user_version=12;
`

// ValidateBrokerPolicy bounds operator configuration independently of any
// live inventory. IDs must later be verified against authoritative ownership
// before a runtime adapter can consume this policy.
func ValidateBrokerPolicy(p BrokerPolicy) error {
	if p.Revision < 0 || p.Revision >= 9007199254740991 || p.Repositories == nil || len(p.Repositories) > 1000 {
		return ErrDenied
	}
	repos, pools := map[string]bool{}, map[string]bool{}
	for _, r := range p.Repositories {
		if !routeUUID(r.ID) || repos[r.ID] || r.Weight < 1 || r.MaxRunners < 0 || r.MaxRunners > 1000000 || r.Pools == nil {
			return ErrDenied
		}
		repos[r.ID] = true
		for _, pool := range r.Pools {
			if !routeUUID(pool.ID) || pools[pool.ID] || pool.MaxRunners < 0 || pool.MaxRunners > r.MaxRunners || pool.WarmIdle < 0 || pool.WarmIdle > pool.MaxRunners {
				return ErrDenied
			}
			pools[pool.ID] = true
			if len(pools) > 1000 {
				return ErrDenied
			}
		}
	}
	return nil
}
func (s *SQLiteStore) BrokerPolicy(ctx context.Context) (BrokerPolicy, error) {
	var p BrokerPolicy
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		var body []byte
		if err := tx.QueryRow("SELECT revision,document FROM broker_policy WHERE id=1").Scan(&p.Revision, &body); err != nil {
			return err
		}
		if err := json.Unmarshal(body, &p.Repositories); err != nil {
			return err
		}
		return ValidateBrokerPolicy(p)
	})
	return p, err
}

// ReplaceBrokerPolicy uses a monotonically increasing CAS revision. A lost
// response must be reconciled by reading; replay with the old revision cannot
// overwrite an intervening edit, including an edit that restores old values.
func (s *SQLiteStore) ReplaceBrokerPolicy(ctx context.Context, p BrokerPolicy) error {
	if err := ValidateBrokerPolicy(p); err != nil {
		return err
	}
	if p.Revision >= 9007199254740990 {
		return ErrDenied
	}
	body, err := json.Marshal(p.Repositories)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.Exec("UPDATE broker_policy SET revision=revision+1,document=? WHERE id=1 AND revision=?", body, p.Revision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrBrokerConflict
		}
		return sqliteAudit(tx, "advisory_broker_policy_replaced", "", s.now())
	})
}
