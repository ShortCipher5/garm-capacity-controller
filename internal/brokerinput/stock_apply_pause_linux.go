package brokerinput

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
			if _, err := tx.Exec("INSERT INTO stock_apply_mode(scope,paused,reason,updated_at) VALUES(?,1,?,?) ON CONFLICT(scope) DO UPDATE SET paused=1,reason=excluded.reason,updated_at=excluded.updated_at", key, reason, now.UTC().Format(time.RFC3339Nano)); err != nil {
				return ErrStockApplyJournal
			}
			if err := stockApplyAudit(tx, key, "", "pause_acknowledged", reason); err != nil {
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
	Scope        string               `json:"scope"`
	Paused       bool                 `json:"paused"`
	Inflight     string               `json:"inflight"`
	Operation    *StockApplyOperation `json:"operation,omitempty"`
	Withdrawable bool                 `json:"withdrawable"`
	Reason       string               `json:"reason"`
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
	recovery := StockApplyRecovery{Scope: key, Inflight: StockApplyInflightNone, Reason: stockApplyReasonNoOpenOperation}
	state, err := c.journal.scopeSnapshot(ctx, key, false)
	if err != nil {
		return StockApplyRecovery{}, err
	}
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
