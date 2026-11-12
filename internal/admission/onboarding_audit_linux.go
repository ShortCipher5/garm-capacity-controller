package admission

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// OnboardingProbeAudit contains only a read-only probe's operator correlation and
// sanitized outcome. No endpoint, path, certificate, fingerprint or daemon name.
// It never authorizes enrollment, binding, policy changes or resource release.
type OnboardingProbeAudit struct {
	RequestID            string `json:"request_id"`
	ProfileID            string `json:"profile_id"`
	Reason               string `json:"reason"`
	Phase                string `json:"phase"`
	ObservationAvailable bool   `json:"observation_available"`
	ServerVersion        string `json:"server_version,omitempty"`
	BindingClass         string `json:"stored_binding_class,omitempty"`
}

func ValidOnboardingProfileID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func ValidOnboardingRequestID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value != strings.ToLower(value) {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(raw) == 16
}

// RecordOnboardingProbeAudit appends audit only in BEGIN IMMEDIATE. Callers must
// commit requested before any external read. A completion is not a binding proof.
// Request IDs correlate observations, not an at-most-once mutation/replay permit.
func (s *SQLiteStore) RecordOnboardingProbeAudit(ctx context.Context, record OnboardingProbeAudit) error {
	if !ValidOnboardingRequestID(record.RequestID) || !ValidOnboardingProfileID(record.ProfileID) || !validAdminText(record.Reason, 256) || strings.TrimSpace(record.Reason) != record.Reason {
		return ErrDenied
	}
	switch record.Phase {
	case "requested":
		if record.ObservationAvailable || record.ServerVersion != "" || record.BindingClass != "" {
			return ErrDenied
		}
	case "completed":
		if record.ObservationAvailable {
			if !validAdminText(record.ServerVersion, 64) || (record.BindingClass != "match_observed" && record.BindingClass != "no_match_observed" && record.BindingClass != "not_checked") {
				return ErrDenied
			}
		} else if record.ServerVersion != "" || record.BindingClass != "not_checked" {
			return ErrDenied
		}
	default:
		return ErrDenied
	}
	encoded, err := json.Marshal(struct {
		Actor string `json:"actor"`
		OnboardingProbeAudit
	}{"management-token", record})
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		return sqliteAudit(tx, "onboarding_probe_"+record.Phase, string(encoded), s.now())
	})
}

// ReadOnboardingBindings reads only configured identity observations using the
// physically read-only connection. It is not reconciliation evidence or a fence,
// and intentionally does not load reservations, jobs, inventory or bootstrap.
func (s *SQLiteStore) ReadOnboardingBindings(ctx context.Context) (map[string]HostBinding, error) {
	var result map[string]HostBinding
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		result, err = sqliteHostBindings(tx, config)
		return err
	})
	return result, err
}
