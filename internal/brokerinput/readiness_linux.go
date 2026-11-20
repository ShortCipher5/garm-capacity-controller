package brokerinput

import "errors"

const stockInventoryAccountingUnavailable = "stock_inventory_accounting_unavailable"

// PlanFailureReason returns only a fixed public category, never underlying data.
func PlanFailureReason(err error) string {
	var failure *planFailure
	if errors.As(err, &failure) {
		switch failure.reason {
		case "observation_unavailable", "ledger_unavailable", "pool_mapping_missing", "host_binding_unavailable", "enrollment_or_evidence_unavailable", stockInventoryAccountingUnavailable, "planning_unavailable", "snapshot_changed_or_expired", "awaiting_observation", "preview_stopped":
			return failure.reason
		}
	}
	return "preview_unavailable"
}

type planFailure struct {
	reason string
	cause  error
}

func (e *planFailure) Error() string             { return e.reason }
func (e *planFailure) Unwrap() error             { return e.cause }
func unavailable(reason string, err error) error { return &planFailure{reason, err} }
