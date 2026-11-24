package admission

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/reuse"
)

// ReuseStatusDisposition describes the durable reuse state of one charge.
const (
	ReuseStatusCharged           = "charged"
	ReuseStatusPendingCleanup    = "pending_cleanup"
	ReuseStatusPartiallyReusable = "partially_reusable"
	ReuseStatusVerifiedReusable  = "verified_reusable"
	// ReuseStatusReleasedHistoric marks charges excluded from accounting by a
	// pre-verification release path. They are NOT verified reusable capacity:
	// this controller never re-verifies or reverses them; they need explicit
	// accounting and physical inventory review.
	ReuseStatusReleasedHistoric = "released_historic"
	reuseStatusAwaitingEvidence = "awaiting_qualified_terminal_evidence"
	reuseStatusReleasedReason   = "released_verified_teardown"
	reuseStatusHistoricReason   = "historic_release_requires_review"
)

// ReuseStatusRow is one charge's operator-visible reuse state: what remains
// charged, what qualified evidence freed, and why the rest is held. It is a
// read-only projection: it never releases, deletes or retries anything.
type ReuseStatusRow struct {
	RequestKey  string           `json:"request_key"`
	Pool        string           `json:"pool"`
	Host        string           `json:"host"`
	State       string           `json:"state"`
	Disposition string           `json:"disposition"`
	Remaining   Resources        `json:"remaining"`
	Reusable    reuse.Dimensions `json:"reusable"`
	Reasons     []string         `json:"reasons"`
}

type ReuseStatusView struct {
	ObservedAt time.Time        `json:"observed_at"`
	Rows       []ReuseStatusRow `json:"rows"`
}

// heldReuseReasons maps still-charged dimensions to the deterministic hold
// reasons shown to operators. CPU/RAM/count share the workload/teardown
// gates; disk has its own exact-cleanup gate.
func heldReuseReasons(remaining Resources) []string {
	reasons := []string{}
	if remaining.CPU > 0 || remaining.RAM > 0 || remaining.Runners > 0 {
		reasons = append(reasons, reuse.HeldAwaitingCompletedWorkload, reuse.HeldAwaitingTerminalDelete, reuse.HeldAwaitingPostDeleteAbsence)
	}
	if remaining.Disk > 0 {
		reasons = append(reasons, reuse.HeldResidualVolumeUnverified)
	}
	return reasons
}

// ReadReuseStatus projects every reservation's reuse disposition in one
// read-only transaction. Unknown, failed and delayed charges stay visible as
// charged or pending cleanup; only durable decisions mark reusable capacity.
func (s *SQLiteStore) ReadReuseStatus(ctx context.Context) (ReuseStatusView, error) {
	view := ReuseStatusView{ObservedAt: s.now().UTC(), Rows: []ReuseStatusRow{}}
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		released, err := sqliteReleasedKeys(tx)
		if err != nil {
			return err
		}
		historic, err := sqliteHistoricReleaseKeys(tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query("SELECT request_key,cpu,ram,runners,disk FROM verified_reuse")
		if err != nil {
			return err
		}
		decisions := map[string]reuse.Dimensions{}
		for rows.Next() {
			var key string
			var cpu, ram, runners, disk int
			if err := rows.Scan(&key, &cpu, &ram, &runners, &disk); err != nil {
				rows.Close()
				return err
			}
			decisions[key] = reuse.Dimensions{CPU: cpu == 1, RAM: ram == 1, Runners: runners == 1, Disk: disk == 1}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for key, reservation := range reservations {
			row := ReuseStatusRow{
				RequestKey: key,
				Pool:       reservation.Request.Pool,
				Host:       reservation.Request.Host,
				State:      reservation.State,
				Remaining:  reservation.Request.Resources,
				Reasons:    []string{},
			}
			switch {
			case released[key] && historic[key]:
				row.Disposition = ReuseStatusReleasedHistoric
				row.Remaining = Resources{}
				row.Reasons = []string{reuseStatusHistoricReason}
			case released[key]:
				row.Disposition = ReuseStatusVerifiedReusable
				row.Remaining = Resources{}
				row.Reusable = reuse.Dimensions{CPU: true, RAM: true, Runners: true, Disk: true}
				row.Reasons = []string{reuseStatusReleasedReason}
			case decisions[key] != (reuse.Dimensions{}):
				freed := decisions[key]
				row.Disposition = ReuseStatusPartiallyReusable
				row.Reusable = freed
				row.Remaining = remainingCharge(reservation.Request.Resources, freed)
				row.Reasons = heldReuseReasons(row.Remaining)
			case reservation.State == "deleting":
				row.Disposition = ReuseStatusPendingCleanup
				row.Reasons = []string{reuseStatusAwaitingEvidence}
			default:
				row.Disposition = ReuseStatusCharged
			}
			view.Rows = append(view.Rows, row)
		}
		return nil
	})
	if err != nil {
		return ReuseStatusView{}, err
	}
	sort.Slice(view.Rows, func(i, j int) bool { return view.Rows[i].RequestKey < view.Rows[j].RequestKey })
	return view, nil
}
