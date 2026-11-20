package adminview

import "github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"

// retirementReuseExplanation describes why a last-cycle diagnostic cannot
// establish capacity reuse. It is not a fresh ledger/host read, an allocation
// input, or release evidence. Absence means no supported cycle to explain.
type retirementReuseExplanation struct {
	Availability    string   `json:"availability"`
	Reasons         []string `json:"reasons"`
	ReuseAuthorized bool     `json:"reuse_authorized"`
}

func explainRetirementReuse(status nativecontroller.RetirementStatus, freshness string) *retirementReuseExplanation {
	if status.At.IsZero() || freshness == "no_cycle" {
		return nil
	}
	r := status.Result
	for _, n := range []int{r.Examined, r.AwaitingEvidence, r.OutsideScope, r.OperationsCompleted, r.Unresolved, r.RetainedDiagnosticsAvailable, r.RetainedDiagnosticsUnavailable} {
		if n < 0 || n > 64 {
			return nil
		}
	}
	if r.ReleaseAuthorized || r.AwaitingEvidence+r.OutsideScope+r.OperationsCompleted+r.Unresolved > r.Examined || r.RetainedDiagnosticsAvailable+r.RetainedDiagnosticsUnavailable > r.OperationsCompleted {
		return nil
	}
	out := &retirementReuseExplanation{Availability: "historical_only", Reasons: []string{}}
	switch freshness {
	case "stale":
		out.Reasons = append(out.Reasons, "cycle_stale")
		return out
	case "clock_invalid":
		out.Reasons = append(out.Reasons, "cycle_clock_unverified")
		return out
	case "within_cycle_window":
		out.Availability = "last_cycle_diagnostics"
	default:
		return nil
	}
	if status.Incomplete {
		out.Reasons = append(out.Reasons, "cycle_incomplete")
	}
	if r.AwaitingEvidence > 0 {
		out.Reasons = append(out.Reasons, "canonical_root_fence_missing")
	}
	if r.OutsideScope > 0 {
		out.Reasons = append(out.Reasons, "outside_configured_owner_scope")
	}
	if r.Unresolved > 0 {
		out.Reasons = append(out.Reasons, "operation_outcome_unresolved")
	}
	if r.RetainedDiagnosticsUnavailable > 0 {
		out.Reasons = append(out.Reasons, "retained_diagnostics_unavailable")
	}
	if r.RetainedDiagnosticsAvailable > 0 {
		out.Reasons = append(out.Reasons, "retained_diagnostics_are_not_release_proof")
	}
	if r.OperationsCompleted > r.RetainedDiagnosticsAvailable+r.RetainedDiagnosticsUnavailable {
		out.Reasons = append(out.Reasons, "retained_diagnostics_not_recorded")
	}
	// Even exact immutable successful STOP/DELETE receipts and a valid pinned
	// retained observation intentionally grant no refund. Never infer absence,
	// reclaimed bytes or freed runner slots from these aggregate counters.
	out.Reasons = append(out.Reasons, "physical_capacity_reuse_not_established")
	return out
}
