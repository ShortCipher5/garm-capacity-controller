package brokerinput

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

// StockApplyPauseAck confirms that local admission is paused for this scope.
// Unresolved describes work still held and charged at the pause transaction.
// SessionDrained and UpstreamSettled remain false because each requires
// independent evidence. Existing idle runners can still accept jobs.
type StockApplyPauseAck struct {
	Scope             string               `json:"scope"`
	Paused            bool                 `json:"paused"`
	AcknowledgedAt    time.Time            `json:"acknowledged_at"`
	LocalPauseApplied bool                 `json:"local_pause_applied"`
	SessionDrained    bool                 `json:"session_drained"`
	UpstreamSettled   bool                 `json:"upstream_settled"`
	Unresolved        *StockApplyOperation `json:"unresolved,omitempty"`
	Reason            string               `json:"reason"`
}

// AcknowledgePause sets the local pause and captures unresolved work in one
// transaction. Proposal admission and local dispatch check the durable pause
// in their own write transactions. Work committed earlier stays recorded.
// AcknowledgedAt is this call's supplied time in UTC. A retry while paused
// observes current work without changing the pause or adding an audit event.
// After Resume the same request sets a new pause. Use RecoverScope to inspect
// a lost acknowledgment without requesting another stop.
func (c *StockApplyController) AcknowledgePause(ctx context.Context, scope [sha256.Size]byte, reason string, now time.Time) (StockApplyPauseAck, error) {
	if scope == ([sha256.Size]byte{}) || now.IsZero() || !stockApplyReasonText(reason, 256) {
		return StockApplyPauseAck{}, ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return StockApplyPauseAck{}, err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	ack := StockApplyPauseAck{Scope: key, AcknowledgedAt: now.UTC()}
	err := c.journal.transaction(ctx, func(tx *sql.Tx) error {
		state, err := loadStockApplyScope(tx, key, false)
		if err != nil {
			return err
		}
		if !state.paused {
			if err := writeStockApplyMode(tx, key, true, "pause_acknowledged", reason, now); err != nil {
				return err
			}
		}
		ack.Unresolved = state.operation
		ack.Paused = true
		ack.LocalPauseApplied = true
		if state.paused {
			ack.Reason = stockApplyReasonAlreadyPaused
		} else {
			ack.Reason = stockApplyReasonPauseActive
		}
		return nil
	})
	if err != nil {
		return StockApplyPauseAck{}, err
	}
	return ack, nil
}

// AdmitScopeWork is a point-in-time preflight for planning. A favorable
// result expires as soon as another transaction changes the scope. It grants
// no reservation or write authority. RecordProposal repeats the checks in
// the transaction that actually admits the local proposal.
func (c *StockApplyController) AdmitScopeWork(ctx context.Context, scope [sha256.Size]byte, now time.Time) error {
	if scope == ([sha256.Size]byte{}) || now.IsZero() {
		return ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	state, err := c.journal.scopeSnapshot(ctx, key, false)
	if err != nil {
		return err
	}
	return state.admitProposal()
}

// StockApplyRecovery is the restart inspection for one scope. It separates
// three situations an operator must never see merged: no in-flight work, an
// unsubmitted proposal that retains its explicit withdrawal path, and a
// possibly accepted update that stays unresolved with the same operation
// identity and charges even on a healthy new process.
type StockApplyRecovery struct {
	ModeRevision string               `json:"mode_revision"`
	Scope        string               `json:"scope"`
	Paused       bool                 `json:"paused"`
	Inflight     string               `json:"inflight"`
	Operation    *StockApplyOperation `json:"operation,omitempty"`
	Withdrawable bool                 `json:"withdrawable"`
	Reason       string               `json:"reason"`
	// RecoverScope includes the most recently journaled comparison for the open operation.
	RecordedReadback *StockApplyRecordedReadback `json:"last_recorded_readback,omitempty"`
}

// StockApplyRecordedReadback describes historical evidence at local audit time.
// The journal does not attest to capture freshness or upstream causality.
type StockApplyRecordedReadback struct {
	Status     string     `json:"status"`
	Comparison string     `json:"comparison,omitempty"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

const (
	StockApplyInflightNone              = "none"
	StockApplyInflightProposalOpen      = "proposal_open"
	StockApplyInflightOutcomeUnresolved = "outcome_unresolved"
)

// RecoverScope inspects durable state after a restart without modifying it.
// A dispatched or unknown operation remains unknown with its original
// identity and charges; a pre-submit proposal keeps its withdrawal path; a
// proven operation is not in flight. Missing or foreign lineage already
// failed at journal open, so an available journal here is the genuine one.
func (c *StockApplyController) RecoverScope(ctx context.Context, scope [sha256.Size]byte, now time.Time) (StockApplyRecovery, error) {
	if scope == ([sha256.Size]byte{}) || now.IsZero() {
		return StockApplyRecovery{}, ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return StockApplyRecovery{}, err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	var recovery StockApplyRecovery
	err := c.journal.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		recovery, err = c.scopeRecovery(tx, key)
		if err != nil || recovery.Operation == nil {
			return err
		}
		recovery.RecordedReadback, err = lastStockApplyReadback(tx, recovery.Operation)
		return err
	})
	if err != nil {
		return StockApplyRecovery{}, err
	}
	return recovery, nil
}

// Bound the scan on the v1 audit table's integer primary key. A journal with
// many unrelated events can still report the unresolved operation and charge.
const stockApplyReadbackWindow = 256

func lastStockApplyReadback(tx *sql.Tx, op *StockApplyOperation) (*StockApplyRecordedReadback, error) {
	var kind, stamp, detail string
	var latest int64
	if err := tx.QueryRow("SELECT COALESCE(max(sequence),0) FROM stock_apply_audit").Scan(&latest); err != nil {
		return nil, ErrStockApplyJournal
	}
	floor := latest - stockApplyReadbackWindow
	err := tx.QueryRow(`SELECT kind, recorded_at, detail FROM stock_apply_audit
		WHERE sequence>? AND scope=? AND operation_id=? AND kind IN ('operation_observation','observation','proposal_recorded')
		ORDER BY sequence DESC LIMIT 1`, floor, op.Scope, op.OperationID).Scan(&kind, &stamp, &detail)
	if errors.Is(err, sql.ErrNoRows) && floor > 0 {
		return &StockApplyRecordedReadback{Status: "history_outside_inspection_window"}, nil
	}
	if err != nil {
		return nil, ErrStockApplyJournal
	}
	if kind == "proposal_recorded" {
		return nil, nil
	}
	maximum, err := strconv.ParseInt(detail, 10, 64)
	if err != nil || maximum < 0 || maximum > 1000000 || detail != strconv.FormatInt(maximum, 10) {
		return nil, ErrStockApplyJournal
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || at.IsZero() {
		return nil, ErrStockApplyJournal
	}
	at = at.UTC()
	if kind == "observation" {
		// Earlier versions attached captures to whichever operation was current
		// at record time, without checking the operation at collection.
		return &StockApplyRecordedReadback{Status: "legacy_scope_only", RecordedAt: &at}, nil
	}
	comparison := "differs_from_both"
	switch maximum {
	case op.Observed:
		comparison = "matches_proposal_baseline"
	case op.Suggested:
		comparison = "matches_proposal_target"
	}
	return &StockApplyRecordedReadback{Status: "operation_matched", Comparison: comparison, RecordedAt: &at}, nil
}

// ChangeScopePause changes local mode only if the reviewed mode revision
// is still current. A response can be lost after commit. Inspect again with
// RecoverScope; a retry carrying the same revision cannot change mode twice.
func (c *StockApplyController) ChangeScopePause(ctx context.Context, scope [sha256.Size]byte, expectedRevision string, paused bool, reason string, now time.Time) (StockApplyRecovery, error) {
	if scope == ([sha256.Size]byte{}) || !validStockApplyLineage(expectedRevision) || !stockApplyReasonText(reason, 256) || now.IsZero() {
		return StockApplyRecovery{}, ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return StockApplyRecovery{}, err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	var after StockApplyRecovery
	err := c.journal.transaction(ctx, func(tx *sql.Tx) error {
		before, err := c.scopeRecovery(tx, key)
		if err != nil {
			return err
		}
		if before.ModeRevision != expectedRevision || before.Paused == paused {
			return ErrStockApplyConflict
		}
		event := "pause_exited"
		if paused {
			event = "pause_entered"
		}
		if err := writeStockApplyMode(tx, key, paused, event, reason, now); err != nil {
			return err
		}
		after, err = c.scopeRecovery(tx, key)
		return err
	})
	if err != nil {
		return StockApplyRecovery{}, err
	}
	return after, nil
}

func writeStockApplyMode(tx *sql.Tx, key string, paused bool, event, reason string, now time.Time) error {
	if _, err := tx.Exec("INSERT INTO stock_apply_mode(scope,paused,reason,updated_at) VALUES(?,?,?,?) ON CONFLICT(scope) DO UPDATE SET paused=excluded.paused,reason=excluded.reason,updated_at=excluded.updated_at", key, paused, reason, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return ErrStockApplyJournal
	}
	return stockApplyAudit(tx, key, "", event, reason)
}

func (c *StockApplyController) scopeRecovery(tx *sql.Tx, key string) (StockApplyRecovery, error) {
	recovery := StockApplyRecovery{Scope: key, Inflight: StockApplyInflightNone, Reason: stockApplyReasonNoOpenOperation}
	state, err := loadStockApplyScope(tx, key, false)
	if err != nil {
		return StockApplyRecovery{}, err
	}
	// The audit sequence covers legacy Pause, Resume and AcknowledgePause
	// as well as guarded changes. Wall clocks can repeat or move backwards.
	var seq int64
	var kind string
	err = tx.QueryRow("SELECT sequence,kind FROM stock_apply_audit WHERE scope=? AND kind IN ('pause_entered','pause_acknowledged','pause_exited') ORDER BY sequence DESC LIMIT 1", key).Scan(&seq, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		if state.modeRecorded {
			return StockApplyRecovery{}, ErrStockApplyJournal
		}
	} else if err != nil || !state.modeRecorded || seq < 1 || state.paused != (kind != "pause_exited") {
		return StockApplyRecovery{}, ErrStockApplyJournal
	}
	digest := sha256.Sum256([]byte(c.journal.lineage + "/" + key + "/" + strconv.FormatInt(seq, 10)))
	recovery.ModeRevision = hex.EncodeToString(digest[:])
	recovery.Paused = state.paused
	recovery.Operation = state.operation
	if state.operation == nil {
		return recovery, nil
	}
	switch state.operation.State {
	case "proposed":
		recovery.Inflight = StockApplyInflightProposalOpen
		recovery.Withdrawable = !state.paused
		recovery.Reason = "proposal_open_withdrawal_available"
		if state.paused {
			recovery.Reason = stockApplyReasonPauseLiftFirst
		}
	case "dispatched":
		recovery.Inflight = StockApplyInflightOutcomeUnresolved
		recovery.Reason = stockApplyReasonAwaitingReceipt
	default:
		recovery.Inflight = StockApplyInflightOutcomeUnresolved
		recovery.Reason = stockApplyReasonOutcomeUncertain
	}
	return recovery, nil
}
