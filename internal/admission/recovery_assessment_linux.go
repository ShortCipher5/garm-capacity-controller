package admission

import (
	"context"
	"database/sql"
)

// RecoveryAssessment is advisory evidence from one read-only ledger snapshot.
// It never establishes physical absence, exclusive writer control or permission
// to resume. Entries include all reservations (not only visibly failed ones):
// a confirmed create is not proof that the resource is still present or idle.
type RecoveryAssessment struct {
	Entries          []RecoveryEntry `json:"entries"`
	NextAfter        string          `json:"next_after,omitempty"`
	ResumeAuthorized bool            `json:"resume_authorized"`
	Blockers         []string        `json:"blockers"`
}

type RecoveryEntry struct {
	RequestKey           string `json:"request_key"`
	Pool                 string `json:"pool"`
	Host                 string `json:"host"`
	State                string `json:"reservation_state"`
	DispatchState        string `json:"dispatch_state"`
	LinkedInventoryCount int    `json:"linked_inventory_count"`
	HistoricalRelease    bool   `json:"historical_release"`
	LedgerCharged        bool   `json:"ledger_charged"`
	RequiredAction       string `json:"required_action"`
}

// AssessRecoverySQLiteFile never opens a writer, migrates a ledger or creates a
// missing source. Pagination bounds response size; pages are separate snapshots
// and are NOT a consistent inventory across concurrent writes. This is local
// diagnostic data only, never authority to replay work, release or resume.
func AssessRecoverySQLiteFile(ctx context.Context, source, after string, limit int) (RecoveryAssessment, error) {
	if limit < 1 || limit > 100 || (after != "" && !bindingName(after)) {
		return RecoveryAssessment{}, ErrDenied
	}
	path, err := sqliteRuntimePrivatePath(source, true)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	store := &SQLiteStore{path: path}
	result := RecoveryAssessment{Entries: []RecoveryEntry{}, Blockers: []string{
		"external_writer_fence_unverified", "fresh_complete_physical_inventory_required",
		"job_assignment_and_completion_authority_unqualified", "retirement_finalizer_unimplemented",
		"backup_or_restore_artifacts_cannot_be_activated",
	}}
	err = store.readTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT r.request_key,r.pool,r.host,r.state,
   COALESCE(d.state,'not_dispatched'),
   (SELECT COUNT(*) FROM inventory i WHERE i.reservation_key=r.request_key),
   EXISTS(SELECT 1 FROM releases x WHERE x.request_key=r.request_key)
   FROM reservations r LEFT JOIN dispatches d ON d.reservation_key=r.request_key
   WHERE r.request_key>? ORDER BY r.request_key LIMIT ?`, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry RecoveryEntry
			if err := rows.Scan(&entry.RequestKey, &entry.Pool, &entry.Host, &entry.State, &entry.DispatchState, &entry.LinkedInventoryCount, &entry.HistoricalRelease); err != nil {
				return err
			}
			if len(result.Entries) == limit {
				result.NextAfter = result.Entries[limit-1].RequestKey
				break
			}
			entry.LedgerCharged = !entry.HistoricalRelease
			switch {
			case entry.HistoricalRelease:
				entry.RequiredAction = "review_historical_release_against_authoritative_inventory"
			case entry.State == "deleting":
				entry.RequiredAction = "retain_charge_do_not_repeat_delete"
			case entry.DispatchState == "intent" || entry.DispatchState == "submitted" || entry.DispatchState == "unknown":
				entry.RequiredAction = "retain_charge_resolve_original_dispatch_without_redispatch"
			default:
				entry.RequiredAction = "retain_charge_reconcile_exact_resource_identity"
			}
			result.Entries = append(result.Entries, entry)
		}
		return rows.Err()
	})
	if err != nil {
		return RecoveryAssessment{}, err
	}
	return result, nil
}
