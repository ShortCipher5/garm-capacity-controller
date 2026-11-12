package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"unicode"
	"unicode/utf8"
)

// SetHostMode is a trusted administrative ledger operation, not a provider/job
// API. Every changed mode closes global readiness and invalidates an active
// collector fence. Enabling therefore requires a NEW authoritative reconciliation.
// Draining/disabled stop NEW claims, not already committed in-flight permits,
// GitHub job assignment, runner registration, guest execution or resource charge.
func (s *SQLiteStore) SetHostMode(ctx context.Context, hostID, mode string) error {
	if hostID == "" || len(hostID) > 256 || !utf8.ValidString(hostID) || (mode != "enabled" && mode != "draining" && mode != "disabled") {
		return ErrDenied
	}
	for _, value := range hostID {
		if unicode.IsControl(value) {
			return ErrDenied
		}
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		host, ok := config.Hosts[hostID]
		if !ok {
			return ErrDenied
		}
		if host.Mode == mode {
			return nil
		} // Retry/no-op neither reopens nor re-closes a fence.
		host.Mode = mode
		config.Hosts[hostID] = host
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE metadata SET config=?,ready=0,nonce='',began_at='' WHERE id=1", encoded); err != nil {
			return err
		}
		// The existing audit target column is reused internally; Snapshot separates
		// host administration targets from reservation request keys for consumers.
		return sqliteAudit(tx, "host_mode_"+mode, hostID, s.now())
	})
}
