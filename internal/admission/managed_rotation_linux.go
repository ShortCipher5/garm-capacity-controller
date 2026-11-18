package admission

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
)

func (s *SQLiteStore) InitializeManagedRotation(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS host_inventory_rotation_operations(target_id TEXT PRIMARY KEY,digest TEXT NOT NULL,request_id TEXT UNIQUE NOT NULL,state TEXT NOT NULL,phase TEXT NOT NULL,attempts INTEGER NOT NULL,receipt TEXT NOT NULL DEFAULT '')`)
		return e
	})
}
func (s *SQLiteStore) ManagedRotations(ctx context.Context) (map[string]firstboot.ManagedRotationOperation, error) {
	out := map[string]firstboot.ManagedRotationOperation{}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT target_id,digest,request_id,state,phase,attempts,receipt FROM host_inventory_rotation_operations ORDER BY target_id LIMIT 1025`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var o firstboot.ManagedRotationOperation
			var receipt string
			if e = rows.Scan(&o.TargetID, &o.Digest, &o.RequestID, &o.State, &o.Phase, &o.Attempts, &receipt); e != nil {
				return e
			}
			if (o.State != "unknown" && o.State != "complete") || !validRotationPhase(o.State, o.Phase) || o.Attempts < 1 || o.Attempts > 100 || len(out) >= 1024 {
				return ErrDenied
			}
			if receipt != "" {
				var r firstboot.RotationReceipt
				if json.Unmarshal([]byte(receipt), &r) != nil || r.State != "revoked" || !r.OldKeyRemoved || !r.SessionsTerminated {
					return ErrDenied
				}
				o.Receipt = &r
			}
			if (o.State == "complete") != (o.Receipt != nil) {
				return ErrDenied
			}
			out[o.TargetID] = o
		}
		return rows.Err()
	})
	return out, e
}
func (s *SQLiteStore) BeginManagedRotation(ctx context.Context, id, digest, request string, retry bool) (firstboot.ManagedRotationOperation, bool, error) {
	op := firstboot.ManagedRotationOperation{}
	run := false
	raw, e := hex.DecodeString(digest)
	if !ValidOnboardingProfileID(id) || !ValidOnboardingRequestID(request) || e != nil || len(raw) != 32 || hex.EncodeToString(raw) != digest {
		return op, false, ErrDenied
	}
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		var receipt string
		e := tx.QueryRowContext(ctx, `SELECT target_id,digest,request_id,state,phase,attempts,receipt FROM host_inventory_rotation_operations WHERE target_id=?`, id).Scan(&op.TargetID, &op.Digest, &op.RequestID, &op.State, &op.Phase, &op.Attempts, &receipt)
		if e == nil {
			if op.Digest != digest || op.RequestID != request || (op.State != "complete" && op.State != "unknown") || !validRotationPhase(op.State, op.Phase) || op.Attempts < 1 || op.Attempts > 100 {
				return ErrDenied
			}
			if receipt != "" {
				var r firstboot.RotationReceipt
				if json.Unmarshal([]byte(receipt), &r) != nil || r.State != "revoked" || !r.OldKeyRemoved || !r.SessionsTerminated {
					return ErrDenied
				}
				op.Receipt = &r
			}
			if (op.State == "complete") != (op.Receipt != nil) {
				return ErrDenied
			}
			if op.State == "complete" || !retry {
				return nil
			}
			if op.Attempts >= 100 {
				return ErrDenied
			}
			op.Attempts++
			if _, e = tx.ExecContext(ctx, `UPDATE host_inventory_rotation_operations SET attempts=? WHERE target_id=?`, op.Attempts, id); e != nil {
				return e
			}
		} else if e == sql.ErrNoRows {
			if retry {
				return ErrDenied
			}
			var count int
			if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_inventory_rotation_operations`).Scan(&count); e != nil {
				return e
			}
			if count >= 1024 {
				return ErrDenied
			}
			op = firstboot.ManagedRotationOperation{TargetID: id, Digest: digest, RequestID: request, State: "unknown", Phase: "approved", Attempts: 1}
			if _, e = tx.ExecContext(ctx, `INSERT INTO host_inventory_rotation_operations(target_id,digest,request_id,state,phase,attempts) VALUES(?,?,?,'unknown','approved',1)`, id, digest, request); e != nil {
				return e
			}
		} else {
			return e
		}
		b, _ := json.Marshal(op)
		if e = sqliteAudit(tx, "host_inventory_rotation_requested", string(b), s.now()); e != nil {
			return e
		}
		run = true
		return nil
	})
	return op, run, e
}
func (s *SQLiteStore) CompleteManagedRotation(ctx context.Context, id, digest, request string, receipt firstboot.RotationReceipt) error {
	if receipt.State != "revoked" || !receipt.OldKeyRemoved || !receipt.SessionsTerminated {
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
		var state, phase, prior string
		if e := tx.QueryRowContext(ctx, `SELECT state,phase,receipt FROM host_inventory_rotation_operations WHERE target_id=? AND digest=? AND request_id=?`, id, digest, request).Scan(&state, &phase, &prior); e != nil {
			return ErrDenied
		}
		if state == "complete" {
			if prior != string(encoded) {
				return ErrDenied
			}
			return nil
		}
		if state != "unknown" || phase != "committed" {
			return ErrDenied
		}
		if _, e := tx.ExecContext(ctx, `UPDATE host_inventory_rotation_operations SET state='complete',phase='verified',receipt=? WHERE target_id=?`, string(encoded), id); e != nil {
			return e
		}
		b, _ := json.Marshal(map[string]string{"target_id": id, "review_digest": digest, "request_id": request})
		return sqliteAudit(tx, "host_inventory_rotation_completed", string(b), s.now())
	})
}

func (s *SQLiteStore) MarkManagedRotation(ctx context.Context, id, digest, request, phase string) error {
	ranks := map[string]int{"approved": 0, "staged": 1, "proved": 2, "registry_switched": 3, "committed": 4}
	want, ok := ranks[phase]
	if !ok || want == 0 {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var state, prior string
		if tx.QueryRowContext(ctx, `SELECT state,phase FROM host_inventory_rotation_operations WHERE target_id=? AND digest=? AND request_id=?`, id, digest, request).Scan(&state, &prior) != nil {
			return ErrDenied
		}
		have, ok := ranks[prior]
		if state != "unknown" || !ok {
			return ErrDenied
		}
		if have >= want {
			return nil
		}
		if want != have+1 {
			return ErrDenied
		}
		if _, e := tx.ExecContext(ctx, `UPDATE host_inventory_rotation_operations SET phase=? WHERE target_id=?`, phase, id); e != nil {
			return e
		}
		b, _ := json.Marshal(map[string]string{"target_id": id, "review_digest": digest, "request_id": request, "phase": phase})
		return sqliteAudit(tx, "host_inventory_rotation_phase", string(b), s.now())
	})
}

func validRotationPhase(state, phase string) bool {
	if state == "complete" {
		return phase == "verified"
	}
	if state != "unknown" {
		return false
	}
	switch phase {
	case "approved", "staged", "proved", "registry_switched", "committed":
		return true
	}
	return false
}
