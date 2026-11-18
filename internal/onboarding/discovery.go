package onboarding

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// DiscoverySource is the full read-only discovery surface. A profile observer
// implements it to enable Discover; *incusapi.Client does. It has no create,
// delete, pool edit, credential or binding operation.
type DiscoverySource interface {
	Observer
	HostTotals(context.Context) (incusapi.HostTotals, error)
	Guests(context.Context) ([]incusapi.Guest, error)
	SampleUtilization(context.Context) (incusapi.UtilizationSample, error)
}

// DiscoveryIdentity is the sanitized authenticated-identity section. It never
// exposes endpoints, certificates, key/origin/scope fingerprints or raw server
// names, exactly like Result.
type DiscoveryIdentity struct {
	ObservationAvailable bool   `json:"observation_available"`
	ServerVersion        string `json:"server_version,omitempty"`
	BindingClass         string `json:"stored_binding_class"`
}

// DiscoveryTotals is the physical host capacity snapshot. Unknown dimensions
// are omitted (nil), never rendered as confirmed zeros: a zero from the
// transport means unparseable or not reported, i.e. unknown.
type DiscoveryTotals struct {
	Complete     bool   `json:"complete"`
	CPUMillis    *int64 `json:"cpu_millis,omitempty"`
	RAMMiB       *int64 `json:"ram_mib,omitempty"`
	DiskMiB      *int64 `json:"disk_mib,omitempty"`
	DiskMiBUsed  *int64 `json:"disk_mib_used,omitempty"`
	StoragePool  string `json:"storage_pool,omitempty"`
	PoolDiskMiB  *int64 `json:"storage_pool_disk_mib,omitempty"`
	PoolDiskUsed *int64 `json:"storage_pool_disk_mib_used,omitempty"`
}

// GuestView is one instance from the bounded project inventory. Resource
// limits are omitted when the guest could not be bounded from view. Managed
// means the guest carries a runner-controller attribution; unmanaged guests
// may consume capacity the shared limit does not account for.
type GuestView struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Managed     bool   `json:"managed"`
	Controller  string `json:"controller,omitempty"`
	Pool        string `json:"pool,omitempty"`
	Reservation string `json:"reservation,omitempty"`
	CPUMillis   *int64 `json:"cpu_millis,omitempty"`
	RAMMiB      *int64 `json:"ram_mib,omitempty"`
	DiskMiBRoot *int64 `json:"root_disk_mib,omitempty"`
}

// DiscoveryUtilization is MEASURED guest usage aggregated over one sweep,
// reported as-is with source=guest-state. Instance-state CPU is cumulative
// time since guest start, not a rate: deriving usage rates requires two
// consecutive samples; a single sweep is a snapshot of cumulative time.
type DiscoveryUtilization struct {
	Source string `json:"source"`
	// CPUUsedMillis is the SUM of per-guest instance-state CPU usage over the
	// sweep, rendered in milliseconds. Every guest contribution is CUMULATIVE
	// CPU time since that guest started (Incus instance state reports
	// nanoseconds). This field is NOT a rate, NOT a percentage and NOT
	// per-second: only the DELTA between two consecutive discoveries divided
	// by the wall clock between them yields a usage rate. The JSON key is
	// cpu_cumulative_millis so a single sweep cannot be misread as a rate.
	CPUUsedMillis int64    `json:"cpu_cumulative_millis"`
	RAMUsedMiB    int64    `json:"ram_used_mib"`
	DiskUsedMiB   int64    `json:"disk_used_mib"`
	Guests        int      `json:"guests"`
	Errors        []string `json:"errors,omitempty"`
}

// DiscoveryResult is one complete read-only discovery run for one allowlisted
// transport profile. It suggests but never applies a budget: enrollment and
// activation remain separate explicit operator actions. Blockers always
// include physical_capacity_scope_unqualified: no discovery observation is
// physical-hardware, whole-project, parent-quota or capacity qualification.
type DiscoveryResult struct {
	Proposal        *BudgetProposal            `json:"proposal,omitempty"`
	Review          *admission.DiscoveryReview `json:"review,omitempty"`
	Profile         string                     `json:"profile_id"`
	ObservedAt      time.Time                  `json:"observed_at"`
	Identity        DiscoveryIdentity          `json:"identity"`
	Totals          *DiscoveryTotals           `json:"totals,omitempty"`
	Guests          []GuestView                `json:"guests"`
	Utilization     *DiscoveryUtilization      `json:"utilization,omitempty"`
	SuggestedBudget admission.Resources        `json:"suggested_budget"`
	Warnings        []string                   `json:"warnings"`
	Blockers        []string                   `json:"blockers"`
}

// Headroom reserved from detected physical totals before any runner workload:
// 2 vCPU and 2 GiB RAM for the OS, Incus and other services, plus 10% of disk.
const (
	suggestionHeadroomCPUMillis = int64(2000)
	suggestionHeadroomRAMMiB    = int64(2048)
	suggestionDiskReserveDiv    = int64(10)
)

// SuggestedBudgetFromTotals derives an advisory admission budget from detected
// physical totals. The documented formula subtracts the reserved OS/Incus
// headroom (2 vCPU, 2 GiB RAM) and 10% of disk from the totals and floors
// every dimension at zero, so the suggestion is never negative. Unknown (zero)
// totals yield a zero suggestion, which by itself constrains nothing and must
// be read together with the detection-incomplete warnings. Runner count is not
// derivable from host totals and is left zero.
func SuggestedBudgetFromTotals(totals incusapi.HostTotals) admission.Resources {
	cpu := totals.CPUMillisTotal - suggestionHeadroomCPUMillis
	if cpu < 0 {
		cpu = 0
	}
	ram := totals.RAMMiBTotal - suggestionHeadroomRAMMiB
	if ram < 0 {
		ram = 0
	}
	var disk int64
	if totals.DiskMiBTotal > 0 {
		disk = totals.DiskMiBTotal - totals.DiskMiBTotal/suggestionDiskReserveDiv
	}
	return admission.Resources{CPU: cpu, RAM: ram, Disk: disk}
}

// Discover performs one audited, read-only discovery run against one
// allowlisted transport profile. Every backend call gets a fresh 5 second
// budget; a section that fails or times out degrades to a blocker for that
// section instead of failing the whole run. No observation is reused as
// reconciliation evidence or persisted as a binding; the only durable effects
// are the requested/completed audit pair.
func (m *Manager) Discover(ctx context.Context, request Request) (DiscoveryResult, error) {
	if !m.allowProbe {
		return DiscoveryResult{}, ErrDisabled
	}
	observer, exists := m.profiles[request.ProfileID]
	if !exists || !admission.ValidOnboardingRequestID(request.RequestID) || !validReason(request.Reason) {
		return DiscoveryResult{}, ErrDenied
	}
	source, ok := observer.(DiscoverySource)
	if !ok {
		return DiscoveryResult{}, ErrDenied
	}
	select {
	case m.gate <- struct{}{}:
		defer func() { <-m.gate }()
	default:
		return DiscoveryResult{}, ErrBusy
	}
	record := admission.OnboardingDiscoveryAudit{RequestID: request.RequestID, ProfileID: request.ProfileID, Reason: request.Reason, Phase: "requested"}
	if m.store.RecordOnboardingDiscoveryAudit(ctx, record) != nil {
		return DiscoveryResult{}, ErrUnavailable
	}
	result := DiscoveryResult{Profile: request.ProfileID, ObservedAt: time.Now().UTC(), Identity: DiscoveryIdentity{BindingClass: "not_checked"}, Guests: []GuestView{}, Warnings: []string{}, Blockers: []string{"physical_capacity_scope_unqualified"}}

	result.Identity, record.ObservationAvailable, record.ServerVersion, record.BindingClass = m.discoverIdentity(ctx, source, &result)
	result.Totals, record.TotalsComplete = m.discoverTotals(ctx, source, &result)
	guests, unmanaged := m.discoverGuests(ctx, source, &result)
	result.Guests = guests
	record.GuestCount, record.UnmanagedGuests = len(guests), unmanaged
	result.Utilization = m.discoverUtilization(ctx, source, &result)

	result.Warnings = append(result.Warnings, "point-in-time snapshot: observations go stale immediately and are not reconciliation evidence")
	record.Phase = "completed"
	record.WarningCount = len(result.Warnings)
	// A canceled HTTP caller cannot erase a completed run's sanitized audit.
	audit, finish := context.WithTimeout(context.Background(), 2*time.Second)
	defer finish()
	if m.store.RecordOnboardingDiscoveryAudit(audit, record) != nil {
		return DiscoveryResult{}, ErrUnavailable
	}
	return result, nil
}

func (m *Manager) discoverIdentity(ctx context.Context, source DiscoverySource, result *DiscoveryResult) (DiscoveryIdentity, bool, string, string) {
	identity := DiscoveryIdentity{BindingClass: "not_checked"}
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	observed, err := source.PhysicalIdentity(read)
	// An observer returning nil error after cancellation cannot turn an expired
	// read into an available observation. Check before our cleanup cancel.
	readErr := read.Err()
	cancel()
	if err != nil || readErr != nil || !observed.Valid() || !safeVersion(observed.ServerVersion()) {
		result.Blockers = append(result.Blockers, "authenticated_identity_unavailable")
		result.Warnings = append(result.Warnings, "stale identity: authenticated identity could not be re-validated this run; previously recorded identity and detection data may be stale")
		return identity, false, "", "not_checked"
	}
	identity.ObservationAvailable, identity.ServerVersion = true, observed.ServerVersion()
	bindings, err := m.store.ReadOnboardingBindings(ctx)
	if err != nil {
		result.Blockers = append(result.Blockers, "stored_binding_comparison_unavailable")
		return identity, true, identity.ServerVersion, "not_checked"
	}
	identity.BindingClass = "no_match_observed"
	for _, binding := range bindings {
		if binding.ServerKeyFingerprint == observed.ServerKeyFingerprint() || binding.OriginFingerprint == observed.OriginFingerprint() || binding.ScopeFingerprint == observed.ScopeFingerprint() || strings.EqualFold(binding.ServerName, observed.ServerName()) {
			identity.BindingClass = "match_observed"
			result.Warnings = append(result.Warnings, "stored binding match observed: overlapping identity is a warning, not separate capacity")
			break
		}
	}
	return identity, true, identity.ServerVersion, identity.BindingClass
}

func (m *Manager) discoverTotals(ctx context.Context, source DiscoverySource, result *DiscoveryResult) (*DiscoveryTotals, bool) {
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	totals, err := source.HostTotals(read)
	readErr := read.Err()
	cancel()
	if err != nil || readErr != nil {
		result.Blockers = append(result.Blockers, "host_totals_unavailable")
		return nil, false
	}
	view := &DiscoveryTotals{}
	unknown := []string{}
	if totals.CPUMillisTotal > 0 {
		value := totals.CPUMillisTotal
		view.CPUMillis = &value
	} else {
		unknown = append(unknown, "cpu")
	}
	if totals.RAMMiBTotal > 0 {
		value := totals.RAMMiBTotal
		view.RAMMiB = &value
	} else {
		unknown = append(unknown, "memory")
	}
	if totals.DiskMiBTotal > 0 {
		value := totals.DiskMiBTotal
		view.DiskMiB = &value
		if totals.DiskMiBUsed > 0 {
			used := totals.DiskMiBUsed
			view.DiskMiBUsed = &used
		}
	} else {
		unknown = append(unknown, "disk")
	}
	if totals.StoragePool != "" {
		view.StoragePool = totals.StoragePool
		if totals.StoragePoolDiskMiBTotal > 0 {
			value := totals.StoragePoolDiskMiBTotal
			view.PoolDiskMiB = &value
			if totals.StoragePoolDiskMiBUsed > 0 {
				used := totals.StoragePoolDiskMiBUsed
				view.PoolDiskUsed = &used
			}
		} else {
			unknown = append(unknown, "storage pool disk")
		}
	}
	view.Complete = len(unknown) == 0
	if !view.Complete {
		sort.Strings(unknown)
		result.Warnings = append(result.Warnings, fmt.Sprintf("physical totals incomplete: %s unknown (detection incomplete)", strings.Join(unknown, ", ")))
	}
	result.SuggestedBudget = SuggestedBudgetFromTotals(totals)
	return view, view.Complete
}

func (m *Manager) discoverGuests(ctx context.Context, source DiscoverySource, result *DiscoveryResult) ([]GuestView, int) {
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	guests, err := source.Guests(read)
	readErr := read.Err()
	cancel()
	if err != nil || readErr != nil {
		result.Blockers = append(result.Blockers, "guest_inventory_unavailable")
		return []GuestView{}, 0
	}
	views := make([]GuestView, 0, len(guests))
	unmanaged := 0
	for _, guest := range guests {
		view := GuestView{Name: guest.Name, Status: guest.Status, Managed: guest.Controller != "", Controller: guest.Controller, Pool: guest.Pool, Reservation: guest.Reservation}
		if guest.CPUMillis > 0 {
			value := guest.CPUMillis
			view.CPUMillis = &value
		}
		if guest.RAMMiB > 0 {
			value := guest.RAMMiB
			view.RAMMiB = &value
		}
		if guest.DiskMiBRoot > 0 {
			value := guest.DiskMiBRoot
			view.DiskMiBRoot = &value
		}
		if !view.Managed {
			unmanaged++
		}
		views = append(views, view)
	}
	if unmanaged > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d unmanaged guests: the shared limit may not cover them", unmanaged))
	}
	return views, unmanaged
}

func (m *Manager) discoverUtilization(ctx context.Context, source DiscoverySource, result *DiscoveryResult) *DiscoveryUtilization {
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	sample, err := source.SampleUtilization(read)
	readErr := read.Err()
	cancel()
	if err != nil || readErr != nil {
		result.Blockers = append(result.Blockers, "utilization_unavailable")
		return nil
	}
	view := &DiscoveryUtilization{Source: "guest-state", CPUUsedMillis: sample.CPUUsedMillis, RAMUsedMiB: sample.RAMUsedMiB, DiskUsedMiB: sample.DiskUsedMiB, Guests: sample.Guests}
	for _, failure := range sample.Errors {
		view.Errors = append(view.Errors, failure.Guest)
	}
	if len(sample.Errors) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("utilization incomplete: %d guest(s) state unavailable; their usage is unknown, not zero", len(sample.Errors)))
	}
	result.Warnings = append(result.Warnings, "guest-state cpu usage is cumulative since guest start; a rate needs consecutive samples")
	return view
}
