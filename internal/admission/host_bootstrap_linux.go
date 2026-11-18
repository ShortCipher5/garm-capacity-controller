package admission

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
)

func (s *SQLiteStore) InitializeBootstrap(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS host_bootstrap_operations(target_id TEXT PRIMARY KEY,digest TEXT NOT NULL,request_id TEXT UNIQUE NOT NULL,state TEXT NOT NULL,attempts INTEGER NOT NULL)`)
		return e
	})
}
func (s *SQLiteStore) BootstrapOperations(ctx context.Context) (map[string]hostbootstrap.Operation, error) {
	out := map[string]hostbootstrap.Operation{}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT target_id,digest,request_id,state,attempts FROM host_bootstrap_operations ORDER BY target_id LIMIT 1025`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var o hostbootstrap.Operation
			if e = rows.Scan(&o.TargetID, &o.Digest, &o.RequestID, &o.State, &o.Attempts); e != nil {
				return e
			}
			if (o.State != "unknown" && o.State != "installed") || o.Attempts < 1 || len(out) >= 1024 {
				return ErrDenied
			}
			out[o.TargetID] = o
		}
		return rows.Err()
	})
	return out, e
}
func (s *SQLiteStore) BeginBootstrap(ctx context.Context, id, digest, request string, retry bool) (hostbootstrap.Operation, bool, error) {
	op := hostbootstrap.Operation{}
	run := false
	raw, e := hex.DecodeString(digest)
	if !ValidOnboardingProfileID(id) || !ValidOnboardingRequestID(request) || e != nil || len(raw) != 32 || hex.EncodeToString(raw) != digest {
		return op, false, ErrDenied
	}
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		e := tx.QueryRowContext(ctx, `SELECT target_id,digest,request_id,state,attempts FROM host_bootstrap_operations WHERE target_id=?`, id).Scan(&op.TargetID, &op.Digest, &op.RequestID, &op.State, &op.Attempts)
		if e == nil {
			if op.Digest != digest || op.RequestID != request || (op.State != "installed" && op.State != "unknown") || op.Attempts < 1 {
				return ErrDenied
			}
			if op.State == "installed" || !retry {
				return nil
			}
			if op.Attempts >= 100 {
				return ErrDenied
			}
			op.Attempts++
			if _, e = tx.ExecContext(ctx, `UPDATE host_bootstrap_operations SET attempts=? WHERE target_id=?`, op.Attempts, id); e != nil {
				return e
			}
		} else if e == sql.ErrNoRows {
			if retry {
				return ErrDenied
			}
			var count int
			if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_bootstrap_operations`).Scan(&count); e != nil {
				return e
			}
			if count >= 1024 {
				return ErrDenied
			}
			op = hostbootstrap.Operation{TargetID: id, Digest: digest, RequestID: request, State: "unknown", Attempts: 1}
			if _, e = tx.ExecContext(ctx, `INSERT INTO host_bootstrap_operations(target_id,digest,request_id,state,attempts) VALUES(?,?,?,'unknown',1)`, id, digest, request); e != nil {
				return e
			}
		} else {
			return e
		}
		b, _ := json.Marshal(op)
		if e = sqliteAudit(tx, "host_bootstrap_requested", string(b), s.now()); e != nil {
			return e
		}
		run = true
		return nil
	})
	return op, run, e
}
func (s *SQLiteStore) CompleteBootstrap(ctx context.Context, id, digest, request string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var state string
		if e := tx.QueryRowContext(ctx, `SELECT state FROM host_bootstrap_operations WHERE target_id=? AND digest=? AND request_id=?`, id, digest, request).Scan(&state); e != nil {
			return ErrDenied
		}
		if state == "installed" {
			return nil
		}
		if state != "unknown" {
			return ErrDenied
		}
		if _, e := tx.ExecContext(ctx, `UPDATE host_bootstrap_operations SET state='installed' WHERE target_id=?`, id); e != nil {
			return e
		}
		b, _ := json.Marshal(map[string]string{"target_id": id, "review_digest": digest, "request_id": request})
		return sqliteAudit(tx, "host_bootstrap_installed", string(b), s.now())
	})
}
