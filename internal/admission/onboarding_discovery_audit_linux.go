package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// OnboardingDiscoveryAudit contains only a read-only discovery run's operator
// correlation and sanitized outcome. No endpoint, path, certificate,
// fingerprint, daemon name, guest name or resource detail is retained. It
// never authorizes enrollment, binding, policy changes or resource release.
type OnboardingDiscoveryAudit struct {
	RequestID            string `json:"request_id"`
	ProfileID            string `json:"profile_id"`
	Reason               string `json:"reason"`
	Phase                string `json:"phase"`
	ObservationAvailable bool   `json:"observation_available"`
	ServerVersion        string `json:"server_version,omitempty"`
	BindingClass         string `json:"stored_binding_class,omitempty"`
	TotalsComplete       bool   `json:"totals_complete,omitempty"`
	GuestCount           int    `json:"guest_count,omitempty"`
	UnmanagedGuests      int    `json:"unmanaged_guests,omitempty"`
	WarningCount         int    `json:"warning_count,omitempty"`
}

// RecordOnboardingDiscoveryAudit appends audit only in BEGIN IMMEDIATE.
// Callers must commit requested before any external read. The completed record
// summarizes counts and classes only; it is not a capacity qualification.
func (s *SQLiteStore) RecordOnboardingDiscoveryAudit(ctx context.Context, record OnboardingDiscoveryAudit) error {
	if !ValidOnboardingRequestID(record.RequestID) || !ValidOnboardingProfileID(record.ProfileID) || !validAdminText(record.Reason, 256) || strings.TrimSpace(record.Reason) != record.Reason {
		return ErrDenied
	}
	switch record.Phase {
	case "requested":
		if record.ObservationAvailable || record.ServerVersion != "" || record.BindingClass != "" || record.TotalsComplete || record.GuestCount != 0 || record.UnmanagedGuests != 0 || record.WarningCount != 0 {
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
		if record.GuestCount < 0 || record.UnmanagedGuests < 0 || record.UnmanagedGuests > record.GuestCount || record.WarningCount < 0 {
			return ErrDenied
		}
	default:
		return ErrDenied
	}
	encoded, err := json.Marshal(struct {
		Actor string `json:"actor"`
		OnboardingDiscoveryAudit
	}{"management-token", record})
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		return sqliteAudit(tx, "onboarding_discovery_"+record.Phase, string(encoded), s.now())
	})
}
