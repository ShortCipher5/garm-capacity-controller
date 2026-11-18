package admission

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"math"
	"time"
)

// BeginInitialNativeEnrollment opens only a first-setup collection barrier.
// All eligibility predicates and the inspected audit sequence are rechecked in
// the same write transaction. It cannot close a ledger that became live after
// an operator's earlier read, or adopt previously bound/charged work.
func (s *SQLiteStore) BeginInitialNativeEnrollment(ctx context.Context, expectedSequence int64, hosts []string) (Fence, error) {
	var result Fence
	if expectedSequence <= 0 || len(hosts) == 0 || len(hosts) > 1000 {
		return result, ErrFence
	}
	hosts = append([]string(nil), hosts...)
	var entropy [16]byte
	if _, e := rand.Read(entropy[:]); e != nil {
		return result, e
	}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		config, ready, epoch, _, e := sqliteMetadata(tx)
		if e != nil {
			return e
		}
		var sequence int64
		if e = tx.QueryRow("SELECT COALESCE(max(sequence),0) FROM audit").Scan(&sequence); e != nil {
			return e
		}
		if sequence != expectedSequence || ready || epoch == math.MaxInt64 || len(hosts) != len(config.Hosts) {
			return ErrFence
		}
		seen := map[string]bool{}
		for _, host := range hosts {
			if seen[host] {
				return ErrHostBinding
			}
			if _, ok := config.Hosts[host]; !ok {
				return ErrHostBinding
			}
			seen[host] = true
		}
		var prior bool
		if e = tx.QueryRow(`SELECT EXISTS(
SELECT 1 FROM host_bindings UNION ALL SELECT 1 FROM host_binding_history_exclusions
UNION ALL SELECT 1 FROM reservations UNION ALL SELECT 1 FROM inventory
UNION ALL SELECT 1 FROM broker_grants UNION ALL SELECT 1 FROM broker_grant_batches
UNION ALL SELECT 1 FROM broker_grant_archives UNION ALL SELECT 1 FROM broker_pool_profiles
UNION ALL SELECT 1 FROM native_domain_bindings UNION ALL SELECT 1 FROM native_domain_overheads
UNION ALL SELECT 1 FROM native_artifact_overheads LIMIT 1)`).Scan(&prior); e != nil {
			return e
		}
		if prior {
			return ErrFence
		}
		result = Fence{Epoch: epoch + 1, Nonce: hex.EncodeToString(entropy[:])}
		if _, e = tx.Exec("UPDATE metadata SET ready=0,epoch=?,nonce=?,began_at=? WHERE id=1", result.Epoch, result.Nonce, s.now().UTC().Format(time.RFC3339Nano)); e != nil {
			return e
		}
		return sqliteAudit(tx, "reconciliation_started", "", s.now())
	})
	if e != nil {
		return Fence{}, e
	}
	return result, nil
}
