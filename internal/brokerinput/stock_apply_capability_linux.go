package brokerinput

import "crypto/sha256"

// StockApplyRefusalCode names one explicit, stable reason a capability or
// adapter claim is refused. Codes are operator-visible diagnostics, never
// permission states: a refusal cannot be bypassed by retrying or restarting.
type StockApplyRefusalCode string

const (
	// StockApplyRefusalInterfaceUnsupported reports an empty or unknown
	// upstream interface kind. Unknown kinds fail closed.
	StockApplyRefusalInterfaceUnsupported StockApplyRefusalCode = "interface_unsupported"
	// StockApplyRefusalStockObserveOnly reports that stock GARM v0.2.1 cannot
	// satisfy the write contract: it has no controller-specific pool-write role
	// and no atomic conditional update. An ordinary stock PUT plus GET must
	// never appear conditional or authoritative, so this refusal is returned
	// even when other evidence fields are populated.
	StockApplyRefusalStockObserveOnly StockApplyRefusalCode = "stock_garm_v0_2_1_observe_only"
	// StockApplyRefusalExternalOwnershipUnproven reports missing verified
	// evidence that the controller exclusively owns the managed settings
	// fields on the exact enrolled scope.
	StockApplyRefusalExternalOwnershipUnproven StockApplyRefusalCode = "external_ownership_unproven"
	// StockApplyRefusalCompletionProofUnavailable reports missing verified
	// evidence of an independently authoritative completion result that
	// survives interruption, timeout and restart.
	StockApplyRefusalCompletionProofUnavailable StockApplyRefusalCode = "completion_proof_unavailable"
	// StockApplyRefusalLateWriterFenceUnproven reports missing verified
	// evidence that delayed or concurrent external writers cannot settle
	// after the controller concludes an outcome.
	StockApplyRefusalLateWriterFenceUnproven StockApplyRefusalCode = "late_writer_fence_unproven"
	// StockApplyRefusalRequirementUnestablished reports one adapter contract
	// requirement the candidate did not establish; Detail carries the
	// requirement name.
	StockApplyRefusalRequirementUnestablished StockApplyRefusalCode = "requirement_unestablished"
	// StockApplyRefusalPhysicalScope reports that the candidate adapter
	// reaches beyond verified settings into physical allocation or teardown.
	// A compatible adapter manages bounded capacity settings only.
	StockApplyRefusalPhysicalScope StockApplyRefusalCode = "physical_allocation_teardown_out_of_scope"
)

// StockApplyRefusal is one explicit refusal reason. Detail is a short fixed
// phrase or requirement name, never caller-provided free text.
type StockApplyRefusal struct {
	Code   StockApplyRefusalCode `json:"code"`
	Detail string                `json:"detail,omitempty"`
}

// StockApplyStockInterface identifies the pinned stock GARM v0.2.1 interface.
// It is registered for naming only: assessment always refuses it with
// StockApplyRefusalStockObserveOnly.
const StockApplyStockInterface = "stock_garm_v0_2_1"

// supportedStockApplyInterfaces lists upstream interface kinds whose verified
// evidence contract has been independently qualified for unattended writes.
// It is deliberately empty: no pinned stock GARM v0.2.1 interface and no
// current upstream interface qualifies, so every production claim fails
// closed and the controller stays inactive by default. Tests register
// fixture kinds to exercise the state machine offline.
var supportedStockApplyInterfaces = map[string]bool{}

// StockApplyCapabilityClaim presents the verified evidence inputs an upstream
// interface must supply before any stock pool settings change can be
// dispatched. Zero digests mean the evidence was not supplied; the assessment
// never trusts caller-supplied booleans or assertions.
type StockApplyCapabilityClaim struct {
	Interface         string            `json:"interface"`
	ExternalOwnership [sha256.Size]byte `json:"external_ownership_digest,omitempty"`
	CompletionProof   [sha256.Size]byte `json:"completion_proof_digest,omitempty"`
	LateWriterFence   [sha256.Size]byte `json:"late_writer_fence_digest,omitempty"`
}

// StockApplyCapabilityVerdict is the fail-closed answer to a capability
// claim. Supported is true only when the interface kind is qualified and
// every required evidence digest is present.
type StockApplyCapabilityVerdict struct {
	Supported bool                `json:"supported"`
	Refusals  []StockApplyRefusal `json:"refusals"`
}

func assessStockApplyCapability(claim StockApplyCapabilityClaim) StockApplyCapabilityVerdict {
	var out StockApplyCapabilityVerdict
	refuse := func(code StockApplyRefusalCode, detail string) {
		out.Refusals = append(out.Refusals, StockApplyRefusal{Code: code, Detail: detail})
	}
	switch {
	case claim.Interface == StockApplyStockInterface:
		refuse(StockApplyRefusalStockObserveOnly, "stock_garm_v0_2_1")
	case claim.Interface == "" || !supportedStockApplyInterfaces[claim.Interface]:
		refuse(StockApplyRefusalInterfaceUnsupported, claim.Interface)
	}
	if claim.ExternalOwnership == ([sha256.Size]byte{}) {
		refuse(StockApplyRefusalExternalOwnershipUnproven, "external_ownership")
	}
	if claim.CompletionProof == ([sha256.Size]byte{}) {
		refuse(StockApplyRefusalCompletionProofUnavailable, "completion_proof")
	}
	if claim.LateWriterFence == ([sha256.Size]byte{}) {
		refuse(StockApplyRefusalLateWriterFenceUnproven, "late_writer_fence")
	}
	out.Supported = len(out.Refusals) == 0
	return out
}

// AssessStockApplyCapability evaluates a capability claim against the pinned
// interface registry and the evidence requirements. With the production
// registry empty this always refuses; refusals name each missing property.
func AssessStockApplyCapability(claim StockApplyCapabilityClaim) StockApplyCapabilityVerdict {
	return assessStockApplyCapability(claim)
}

// StockApplyAdapterRequirement names one property a future compatible write
// adapter must establish and keep established for every managed change.
type StockApplyAdapterRequirement string

const (
	// StockApplyExclusiveChangeBoundary: the adapter must prove and hold
	// exclusive, authorized write access to the managed pool settings for the
	// exact enrolled scope, fencing every other writer including native GARM
	// UI/CLI and direct database access.
	StockApplyExclusiveChangeBoundary StockApplyAdapterRequirement = "exclusive_authorized_change_boundary"
	// StockApplyAuthoritativeOutcome: after any interruption, timeout or
	// restart, the adapter must return an independently authoritative result
	// for the exact original request, not a readback comparison.
	StockApplyAuthoritativeOutcome StockApplyAdapterRequirement = "independently_authoritative_result_after_interruption"
	// StockApplyLateWriterRacePrevention: the adapter must prove no delayed or
	// concurrent writer can still settle after an outcome is recorded.
	StockApplyLateWriterRacePrevention StockApplyAdapterRequirement = "late_writer_race_prevention"
	// StockApplyDriftHandling: the adapter must detect external settings drift
	// and pause rather than apply or resume over changed state.
	StockApplyDriftHandling StockApplyAdapterRequirement = "drift_handling"
	// StockApplySafeRollback: rollback is permitted only after the original
	// outcome and the current state are both independently known; a previously
	// observed maximum is history, not a safe rollback target.
	StockApplySafeRollback StockApplyAdapterRequirement = "safe_rollback_after_known_original_outcome_and_current_state"
)

var stockApplyAdapterRequirements = []StockApplyAdapterRequirement{
	StockApplyExclusiveChangeBoundary,
	StockApplyAuthoritativeOutcome,
	StockApplyLateWriterRacePrevention,
	StockApplyDriftHandling,
	StockApplySafeRollback,
}

// StockApplyAdapterClaim presents the evidence a candidate write adapter
// establishes for each contract requirement, keyed by requirement. A missing
// key or a zero digest means the requirement is not established. SettingsOnly
// must be true: verified settings stay separated from physical allocation and
// teardown, which this plan never manages.
type StockApplyAdapterClaim struct {
	Name         string                                             `json:"name"`
	Interface    string                                             `json:"interface"`
	SettingsOnly bool                                               `json:"settings_only"`
	Establishes  map[StockApplyAdapterRequirement][sha256.Size]byte `json:"establishes,omitempty"`
}

// StockApplyAdapterVerdict reports whether a candidate adapter may be used.
// An unsupported adapter returns Availability "unavailable" and one refusal
// per unestablished requirement; it never approximates the contract.
type StockApplyAdapterVerdict struct {
	Availability string              `json:"availability"`
	Refusals     []StockApplyRefusal `json:"refusals"`
}

// StockGarmAdapterClaim describes the pinned stock GARM v0.2.1 path as an
// adapter candidate: it establishes none of the contract requirements and
// reaches into physical runner lifecycle it cannot verify.
func StockGarmAdapterClaim() StockApplyAdapterClaim {
	return StockApplyAdapterClaim{
		Name:         "stock-garm-v0.2.1",
		Interface:    StockApplyStockInterface,
		SettingsOnly: false,
		Establishes:  map[StockApplyAdapterRequirement][sha256.Size]byte{},
	}
}

// AssessStockApplyAdapter evaluates a candidate adapter against the
// compatibility contract. The built-in stock claim always returns
// "unavailable".
func AssessStockApplyAdapter(claim StockApplyAdapterClaim) StockApplyAdapterVerdict {
	out := StockApplyAdapterVerdict{Availability: "available"}
	refuse := func(code StockApplyRefusalCode, detail string) {
		out.Refusals = append(out.Refusals, StockApplyRefusal{Code: code, Detail: detail})
	}
	switch {
	case claim.Interface == StockApplyStockInterface:
		refuse(StockApplyRefusalStockObserveOnly, "stock_garm_v0_2_1")
	case claim.Interface == "" || !supportedStockApplyInterfaces[claim.Interface]:
		refuse(StockApplyRefusalInterfaceUnsupported, claim.Interface)
	}
	for _, requirement := range stockApplyAdapterRequirements {
		if digest, ok := claim.Establishes[requirement]; !ok || digest == ([sha256.Size]byte{}) {
			refuse(StockApplyRefusalRequirementUnestablished, string(requirement))
		}
	}
	if !claim.SettingsOnly {
		refuse(StockApplyRefusalPhysicalScope, "verified_settings_only")
	}
	if len(out.Refusals) != 0 {
		out.Availability = "unavailable"
	}
	return out
}
