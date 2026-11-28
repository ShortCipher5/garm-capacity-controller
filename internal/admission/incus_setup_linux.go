package admission

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
)

type IncusSetupAttempt struct {
	Target string `json:"target_id"`
	Digest string `json:"review_digest"`
	Status string `json:"status"`
}

func (s *SQLiteStore) InitializeIncusSetup(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS incus_setup_v1(target TEXT NOT NULL,digest TEXT PRIMARY KEY,status TEXT NOT NULL)`)
		return e
	})
}
func (s *SQLiteStore) BeginIncusSetup(ctx context.Context, id, digest string) error {
	d, e := hex.DecodeString(digest)
	if !ValidOnboardingProfileID(id) || e != nil || len(d) != 32 || hex.EncodeToString(d) != digest {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var total int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM incus_setup_v1").Scan(&total); err != nil || total >= 2048 {
			return ErrDenied
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO incus_setup_v1(target,digest,status) VALUES(?,?,'unknown')", id, digest)
		if err != nil {
			return err
		}
		return sqliteAudit(tx, "incus_setup_started", fmt.Sprintf("target=%s digest=%s", id, digest), s.now())
	})
}
func (s *SQLiteStore) FinishIncusSetup(ctx context.Context, id, digest, status string) error {
	switch status {
	case "verified", "changed", "refused":
	default:
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, "UPDATE incus_setup_v1 SET status=? WHERE target=? AND digest=? AND status='unknown'", status, id, digest)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil || n != 1 {
			return ErrDenied
		}
		return sqliteAudit(tx, "incus_setup_completed", fmt.Sprintf("target=%s digest=%s outcome=%s", id, digest, status), s.now())
	})
}
func (s *SQLiteStore) LatestIncusSetup(ctx context.Context, id string) (IncusSetupAttempt, error) {
	var a IncusSetupAttempt
	e := s.readTransaction(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT target,digest,status FROM incus_setup_v1 WHERE target=? ORDER BY rowid DESC LIMIT 1", id).Scan(&a.Target, &a.Digest, &a.Status)
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	})
	return a, e
}
