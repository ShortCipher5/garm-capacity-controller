// Package onboarding performs authenticated read-only preflight on explicitly
// allowlisted transport references. It does not enroll or activate any backend.
package onboarding

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

var ErrConfiguration = errors.New("onboarding preflight configuration unavailable")
var ErrDenied = errors.New("invalid onboarding preflight request")
var ErrUnavailable = errors.New("onboarding preflight or its audit unavailable")
var ErrDisabled = errors.New("onboarding probes disabled")
var ErrBusy = errors.New("another onboarding preflight is in progress")

// Observer has no create, delete, pool edit, credential or binding operation.
type Observer interface {
	PhysicalIdentity(context.Context) (incusapi.PhysicalServerIdentity, error)
}
type Store interface {
	RecordOnboardingProbeAudit(context.Context, admission.OnboardingProbeAudit) error
	ReadOnboardingBindings(context.Context) (map[string]admission.HostBinding, error)
}
type Manager struct {
	allowProbe bool
	store      Store
	profiles   map[string]Observer
	gate       chan struct{}
}
type Profile struct {
	ID string `json:"id"`
}
type Discovery struct {
	Mode                string    `json:"mode"`
	Profiles            []Profile `json:"profiles"`
	ProbeEnabled        bool      `json:"probe_enabled"`
	EnrollmentSupported bool      `json:"enrollment_supported"`
	CapacityQualified   bool      `json:"capacity_qualified"`
}
type Request struct {
	RequestID string `json:"request_id"`
	ProfileID string `json:"profile_id"`
	Reason    string `json:"reason"`
}
type Result struct {
	Mode                 string    `json:"mode"`
	RequestID            string    `json:"request_id"`
	ProfileID            string    `json:"profile_id"`
	ObservedAt           time.Time `json:"observed_at"`
	ObservationAvailable bool      `json:"observation_available"`
	ServerVersion        string    `json:"server_version,omitempty"`
	BindingClass         string    `json:"stored_binding_class"`
	EnrollmentPerformed  bool      `json:"enrollment_performed"`
	ActivationPerformed  bool      `json:"activation_performed"`
	CapacityQualified    bool      `json:"capacity_qualified"`
	Blockers             []string  `json:"blockers"`
}

func New(store Store, profiles map[string]Observer, allowProbe bool) (*Manager, error) {
	if store == nil || len(profiles) == 0 || len(profiles) > 32 {
		return nil, ErrConfiguration
	}
	manager := &Manager{allowProbe: allowProbe, store: store, profiles: map[string]Observer{}, gate: make(chan struct{}, 1)}
	for id, observer := range profiles {
		if !admission.ValidOnboardingProfileID(id) || observer == nil {
			return nil, ErrConfiguration
		}
		manager.profiles[id] = observer
	}
	return manager, nil
}
func (m *Manager) ProbeEnabled() bool { return m.allowProbe }
func (m *Manager) Discovery() Discovery {
	result := Discovery{Mode: "read-only-onboarding-preflight", ProbeEnabled: m.allowProbe, Profiles: []Profile{}}
	for id := range m.profiles {
		result.Profiles = append(result.Profiles, Profile{ID: id})
	}
	sort.Slice(result.Profiles, func(i, j int) bool { return result.Profiles[i].ID < result.Profiles[j].ID })
	return result
}
func validReason(value string) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safeVersion(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("._+-", r)) {
			return false
		}
	}
	return true
}

// Probe deliberately cannot return an enrollment/activation/capacity permit.
// Every result remains blocked even if authenticated identity is observable.
// No observation is reused as reconciliation evidence or persisted as a binding.
func (m *Manager) Probe(ctx context.Context, request Request) (Result, error) {
	if !m.allowProbe {
		return Result{}, ErrDisabled
	}
	observer, exists := m.profiles[request.ProfileID]
	if !exists || !admission.ValidOnboardingRequestID(request.RequestID) || !validReason(request.Reason) {
		return Result{}, ErrDenied
	}
	select {
	case m.gate <- struct{}{}:
		defer func() { <-m.gate }()
	default:
		return Result{}, ErrBusy
	}
	record := admission.OnboardingProbeAudit{RequestID: request.RequestID, ProfileID: request.ProfileID, Reason: request.Reason, Phase: "requested"}
	if m.store.RecordOnboardingProbeAudit(ctx, record) != nil {
		return Result{}, ErrUnavailable
	}
	result := Result{Mode: "read-only-onboarding-preflight", RequestID: request.RequestID, ProfileID: request.ProfileID, ObservedAt: time.Now().UTC(), BindingClass: "not_checked", Blockers: []string{"enrollment_not_implemented", "physical_capacity_scope_unqualified"}}
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	identity, err := observer.PhysicalIdentity(read)
	cancel()
	if err == nil && identity.Valid() && safeVersion(identity.ServerVersion()) {
		result.ObservationAvailable = true
		result.ServerVersion = identity.ServerVersion()
		bindings, err := m.store.ReadOnboardingBindings(ctx)
		if err == nil {
			result.BindingClass = "no_match_observed"
			for _, binding := range bindings {
				if binding.ServerKeyFingerprint == identity.ServerKeyFingerprint() || binding.OriginFingerprint == identity.OriginFingerprint() || binding.ScopeFingerprint == identity.ScopeFingerprint() || strings.EqualFold(binding.ServerName, identity.ServerName()) {
					result.BindingClass = "match_observed"
					result.Blockers = append(result.Blockers, "stored_binding_match")
					break
				}
			}
		} else {
			result.Blockers = append(result.Blockers, "stored_binding_comparison_unavailable")
		}
	} else {
		result.Blockers = append(result.Blockers, "authenticated_identity_unavailable")
	}
	result.ObservedAt = time.Now().UTC()
	record.Phase = "completed"
	record.ObservationAvailable = result.ObservationAvailable
	record.ServerVersion = result.ServerVersion
	record.BindingClass = result.BindingClass
	// A canceled HTTP caller cannot erase a completed read's sanitized audit.
	audit, finish := context.WithTimeout(context.Background(), 2*time.Second)
	defer finish()
	if m.store.RecordOnboardingProbeAudit(audit, record) != nil {
		return Result{}, ErrUnavailable
	}
	return result, nil
}
