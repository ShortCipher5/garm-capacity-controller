package admission

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
)

func (s *SQLiteStore) InitializeFirstboot(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS host_firstboot_operations(target_id TEXT PRIMARY KEY,digest TEXT NOT NULL,request_id TEXT UNIQUE NOT NULL,state TEXT NOT NULL,attempts INTEGER NOT NULL,receipt TEXT NOT NULL DEFAULT '')`)
		return e
	})
}
func (s *SQLiteStore) FirstbootOperations(ctx context.Context) (map[string]firstboot.Operation, error) {
	out := map[string]firstboot.Operation{}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT target_id,digest,request_id,state,attempts,receipt FROM host_firstboot_operations ORDER BY target_id LIMIT 1025`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var o firstboot.Operation
			var receipt string
			if e = rows.Scan(&o.TargetID, &o.Digest, &o.RequestID, &o.State, &o.Attempts, &receipt); e != nil {
				return e
			}
			if (o.State != "unknown" && o.State != "installed") || o.Attempts < 1 || len(out) >= 1024 {
				return ErrDenied
			}
			if receipt != "" {
				var r firstboot.Receipt
				if json.Unmarshal([]byte(receipt), &r) != nil || r.Digest != o.Digest || r.State != "installed" {
					return ErrDenied
				}
				o.Receipt = &r
			}
			if (o.State == "installed") != (o.Receipt != nil) {
				return ErrDenied
			}
			out[o.TargetID] = o
		}
		return rows.Err()
	})
	return out, e
}
func (s *SQLiteStore) BeginFirstboot(ctx context.Context, id, digest, request string, retry bool) (firstboot.Operation, bool, error) {
	op := firstboot.Operation{}
	run := false
	raw, e := hex.DecodeString(digest)
	if !ValidOnboardingProfileID(id) || !ValidOnboardingRequestID(request) || e != nil || len(raw) != 32 || hex.EncodeToString(raw) != digest {
		return op, false, ErrDenied
	}
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		var receipt string
		e := tx.QueryRowContext(ctx, `SELECT target_id,digest,request_id,state,attempts,receipt FROM host_firstboot_operations WHERE target_id=?`, id).Scan(&op.TargetID, &op.Digest, &op.RequestID, &op.State, &op.Attempts, &receipt)
		if e == nil {
			if op.Digest != digest || op.RequestID != request || (op.State != "installed" && op.State != "unknown") || op.Attempts < 1 {
				return ErrDenied
			}
			if receipt != "" {
				var r firstboot.Receipt
				if json.Unmarshal([]byte(receipt), &r) != nil || r.Digest != op.Digest || r.State != "installed" {
					return ErrDenied
				}
				op.Receipt = &r
			}
			if (op.State == "installed") != (op.Receipt != nil) {
				return ErrDenied
			}
			if op.State == "installed" || !retry {
				return nil
			}
			if op.Attempts >= 100 {
				return ErrDenied
			}
			op.Attempts++
			if _, e = tx.ExecContext(ctx, `UPDATE host_firstboot_operations SET attempts=? WHERE target_id=?`, op.Attempts, id); e != nil {
				return e
			}
		} else if e == sql.ErrNoRows {
			if retry {
				return ErrDenied
			}
			var count int
			if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_firstboot_operations`).Scan(&count); e != nil {
				return e
			}
			if count >= 1024 {
				return ErrDenied
			}
			op = firstboot.Operation{TargetID: id, Digest: digest, RequestID: request, State: "unknown", Attempts: 1}
			if _, e = tx.ExecContext(ctx, `INSERT INTO host_firstboot_operations(target_id,digest,request_id,state,attempts) VALUES(?,?,?,'unknown',1)`, id, digest, request); e != nil {
				return e
			}
		} else {
			return e
		}
		b, _ := json.Marshal(op)
		if e = sqliteAudit(tx, "host_firstboot_requested", string(b), s.now()); e != nil {
			return e
		}
		run = true
		return nil
	})
	return op, run, e
}
func (s *SQLiteStore) CompleteFirstboot(ctx context.Context, id, digest, request string, receipt firstboot.Receipt) error {
	if receipt.State != "installed" || receipt.Digest != digest || receipt.NetworkReachabilityVerified || receipt.ListenPort != firstboot.ListenerPort {
		return ErrDenied
	}
	if _, e := hostbootstrap.PublicKey(receipt.HostKey); e != nil {
		return ErrDenied
	}
	encoded, e := json.Marshal(receipt)
	if e != nil {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var state, prior string
		if e := tx.QueryRowContext(ctx, `SELECT state,receipt FROM host_firstboot_operations WHERE target_id=? AND digest=? AND request_id=?`, id, digest, request).Scan(&state, &prior); e != nil {
			return ErrDenied
		}
		if state == "installed" {
			if prior != string(encoded) {
				return ErrDenied
			}
			return nil
		}
		if state != "unknown" {
			return ErrDenied
		}
		if _, e := tx.ExecContext(ctx, `UPDATE host_firstboot_operations SET state='installed',receipt=? WHERE target_id=?`, string(encoded), id); e != nil {
			return e
		}
		b, _ := json.Marshal(map[string]string{"target_id": id, "review_digest": digest, "request_id": request})
		return sqliteAudit(tx, "host_firstboot_installed", string(b), s.now())
	})
}
