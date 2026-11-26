package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
	"unicode"
	"unicode/utf8"
)

// validHostID accepts the same host references SetHostMode accepts: a bounded,
// control-free UTF-8 name. It never derives from a URL, path or credential.
func validHostID(hostID string) bool {
	if hostID == "" || len(hostID) > 256 || !utf8.ValidString(hostID) {
		return false
	}
	for _, value := range hostID {
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}

func validStoredMagnitude(r Resources) bool {
	const maxJSONSafe = int64(9007199254740991)
	return r.CPU <= maxJSONSafe && r.RAM <= maxJSONSafe && r.Disk <= maxJSONSafe && r.Runners <= maxJSONSafe
}

// EnrollHost registers a NEW host budget explicitly supplied by an operator.
// It fails with ErrConflict when the host already exists: enrollment never
// silently adopts or rewrites an existing host. The initial mode must be
// "disabled" or "draining"; "enabled" is rejected because activation is a
// separate explicit action after qualification and reconciliation. The host
// starts unhealthy with no detected totals; it is in no pool yet. Enrollment
// closes global readiness and invalidates an active collector fence, exactly
// like any other policy shape change. Audit is durable in the same
// BEGIN IMMEDIATE transaction.
func (s *SQLiteStore) EnrollHost(ctx context.Context, hostID string, budget Resources, mode string) error {
	if !validHostID(hostID) || !nonnegative(budget) || !validStoredMagnitude(budget) || (mode != "disabled" && mode != "draining") {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		return s.enrollHostTx(tx, hostID, budget, mode, nil, time.Time{})
	})
}

func (s *SQLiteStore) enrollHostTx(tx *sql.Tx, hostID string, budget Resources, mode string, detected *Resources, observedAt time.Time) error {
	config, _, _, _, err := sqliteMetadata(tx)
	if err != nil {
		return err
	}
	if _, exists := config.Hosts[hostID]; exists {
		return ErrConflict
	}
	var withdrawn bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='host_draft_removed' AND request_key=?)", hostID).Scan(&withdrawn); err != nil {
		return err
	}
	if withdrawn {
		return ErrConflict
	}
	config.Hosts[hostID] = Host{Budget: budget, Mode: mode, Healthy: false, ObservedAt: observedAt, Detected: detected}
	if observedAt.IsZero() {
		host := config.Hosts[hostID]
		host.ObservedAt = s.now()
		config.Hosts[hostID] = host
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE metadata SET config=?,ready=0,nonce='',began_at='' WHERE id=1", encoded); err != nil {
		return err
	}
	return sqliteAudit(tx, "host_enrolled", hostID, s.now())
}

// SetHostDetected records the last discovered physical totals for an existing
// host in one BEGIN IMMEDIATE transaction. It refreshes the host observation
// (ObservedAt, Healthy=true) so a fresh discovery view is also a fresh health
// observation. A nil detected clears the stored totals; a non-nil value must
// be non-negative. The observation must be fresh: not zero, not in the future
// and not older than the configured maximum observation age, so this can never
// mint health from a stale or fabricated observation. Detected is display
// data: readiness and fences are left untouched because admission semantics
// do not change.
func (s *SQLiteStore) SetHostDetected(ctx context.Context, hostID string, detected *Resources, observedAt time.Time) error {
	if !validHostID(hostID) || observedAt.IsZero() {
		return ErrDenied
	}
	if detected != nil && (!nonnegative(*detected) || !validStoredMagnitude(*detected)) {
		return ErrDenied
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
		now := s.now()
		age := now.Sub(observedAt)
		if age < 0 || age > config.MaxObservationAge {
			return ErrDenied
		}
		if detected == nil {
			host.Detected = nil
		} else {
			value := *detected
			host.Detected = &value
		}
		host.ObservedAt = observedAt.UTC()
		host.Healthy = true
		config.Hosts[hostID] = host
		if err := validateConfig(config); err != nil {
			return err
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE metadata SET config=? WHERE id=1", encoded); err != nil {
			return err
		}
		return sqliteAudit(tx, "host_detected", hostID, now)
	})
}
