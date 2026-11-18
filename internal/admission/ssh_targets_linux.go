package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
)

// InitializeSSHTargets is an explicit opt-in schema addition. These records never
// participate in host bindings, admission budgets, or enrollment.
func (s *SQLiteStore) InitializeSSHTargets(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ssh_inventory_targets (id TEXT PRIMARY KEY, config TEXT NOT NULL, digest TEXT NOT NULL)`)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ssh_inventory_revocations (id TEXT PRIMARY KEY, config TEXT NOT NULL, digest TEXT NOT NULL)`)
		if e != nil {
			return e
		}
		// Database-enforced tombstones also protect older readers/writers that know
		// only the active target table and would otherwise resurrect revoked IDs.
		_, e = tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS ssh_target_no_resurrection_insert BEFORE INSERT ON ssh_inventory_targets WHEN EXISTS (SELECT 1 FROM ssh_inventory_revocations WHERE id=NEW.id) BEGIN SELECT RAISE(ABORT,'SSH target ID revoked'); END`)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS ssh_target_no_resurrection_update BEFORE UPDATE ON ssh_inventory_targets WHEN EXISTS (SELECT 1 FROM ssh_inventory_revocations WHERE id=NEW.id) BEGIN SELECT RAISE(ABORT,'SSH target ID revoked'); END`)
		return e
	})
}
func (s *SQLiteStore) ListSSHTargets(ctx context.Context) (map[string]sshinventory.Config, error) {
	out := map[string]sshinventory.Config{}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT t.id,t.config,t.digest FROM ssh_inventory_targets t WHERE NOT EXISTS (SELECT 1 FROM ssh_inventory_revocations r WHERE r.id=t.id) ORDER BY t.id LIMIT 33`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, raw, digest string
			if e = rows.Scan(&id, &raw, &digest); e != nil {
				return e
			}
			var c sshinventory.Config
			if len(raw) > 20000 || json.Unmarshal([]byte(raw), &c) != nil || digest != sshinventory.ReviewDigest(id, c) {
				return ErrDenied
			}
			out[id] = c
		}
		return rows.Err()
	})
	return out, e
}

// SaveSSHTarget is append-only and idempotent for an identical target. A changed
// address, credential or host key requires a new reviewed ID. Audit and record
// commit atomically, so failed audit cannot authorize future SSH contact.
func (s *SQLiteStore) SaveSSHTarget(ctx context.Context, request, id string, c sshinventory.Config, digest string, limit int) error {
	if !ValidOnboardingRequestID(request) || !ValidOnboardingProfileID(id) || limit < 1 || limit > 32 || digest != sshinventory.ReviewDigest(id, c) {
		return ErrDenied
	}
	raw, e := json.Marshal(c)
	if e != nil || len(raw) > 20000 {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var old, oldDigest string
		e := tx.QueryRowContext(ctx, `SELECT config,digest FROM ssh_inventory_targets WHERE id=?`, id).Scan(&old, &oldDigest)
		if e == nil {
			if old == string(raw) && oldDigest == digest {
				var revoked int
				if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ssh_inventory_revocations WHERE id=?`, id).Scan(&revoked); e != nil {
					return e
				}
				if revoked != 0 {
					return ErrDenied
				}
				return nil
			}
			return ErrDenied
		}
		if e != sql.ErrNoRows {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ssh_inventory_targets t WHERE NOT EXISTS (SELECT 1 FROM ssh_inventory_revocations r WHERE r.id=t.id)`).Scan(&count); e != nil {
			return e
		}
		var total int
		if e = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM ssh_inventory_targets)+(SELECT COUNT(*) FROM ssh_inventory_revocations)`).Scan(&total); e != nil {
			return e
		}
		if total >= 1024 {
			return ErrDenied
		}
		if count >= limit {
			return ErrDenied
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO ssh_inventory_targets(id,config,digest) VALUES(?,?,?)`, id, string(raw), digest); e != nil {
			return e
		}
		audit, _ := json.Marshal(map[string]string{"actor": "ssh-target-management-token", "request_id": request, "target_id": id, "review_digest": digest})
		return sqliteAudit(tx, "ssh_target_approved", string(audit), s.now())
	})
}

// RevokeSSHTarget only withdraws local inventory authorization. It does not
// delete target data, revoke remote keys, stop runners or release any capacity.
func (s *SQLiteStore) RevokeSSHTarget(ctx context.Context, request, id, digest string) error {
	if !ValidOnboardingRequestID(request) || !ValidOnboardingProfileID(id) {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var raw, saved string
		e := tx.QueryRowContext(ctx, `SELECT config,digest FROM ssh_inventory_revocations WHERE id=?`, id).Scan(&raw, &saved)
		if e == nil {
			var c sshinventory.Config
			if len(raw) > 20000 || json.Unmarshal([]byte(raw), &c) != nil || saved != digest || saved != sshinventory.ReviewDigest(id, c) {
				return ErrDenied
			}
			return nil
		}
		if e != sql.ErrNoRows {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT config,digest FROM ssh_inventory_targets WHERE id=?`, id).Scan(&raw, &saved); e != nil {
			return ErrDenied
		}
		var c sshinventory.Config
		if len(raw) > 20000 || json.Unmarshal([]byte(raw), &c) != nil || saved != digest || saved != sshinventory.ReviewDigest(id, c) {
			return ErrDenied
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO ssh_inventory_revocations(id,config,digest) VALUES(?,?,?)`, id, raw, digest); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM ssh_inventory_targets WHERE id=?`, id); e != nil {
			return e
		}

		audit, _ := json.Marshal(map[string]string{"actor": "ssh-target-management-token", "request_id": request, "target_id": id, "review_digest": digest})
		return sqliteAudit(tx, "ssh_target_revoked", string(audit), s.now())
	})
}
