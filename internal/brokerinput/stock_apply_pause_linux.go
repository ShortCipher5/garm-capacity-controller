// Durable operator stop and interrupted-update recovery seam. A stop
// acknowledgment means no new controller work under the paused scope; it
// never means upstream work completed, a runner stopped, or capacity became
// available. Local pause applied, local session drained and upstream outcome
// settled are three distinct facts: this seam can only ever establish the
// first. A possibly accepted update stays unresolved across restart until an
// independently authoritative outcome is proven; a matching GET or a resumed
// listener is not proof.
package brokerinput

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// StockApplyPauseAck is the durable stop acknowledgment for one scope. It
// reports the local pause and the exact unresolved work that stays held and
// charged. SessionDrained and UpstreamSettled are always false here: a local
// pause never drains an upstream session and never settles an accepted
// change, and rendering them as anything but distinct unproven facts would
// be a false completion claim.
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

// AcknowledgePause durably pauses the scope and, in the same serialized
// transaction, reads back the exact work that remains unresolved. The
// acknowledgment is serialized against new admission: after it returns, no
// new proposal or dispatch can start under the scope. Earlier accepted or
// uncertain work is held, never replayed or discarded. A repeated
// acknowledgment after a lost response is idempotent: it changes nothing and
// reports the same durable state.
func (c *StockApplyController) AcknowledgePause(ctx context.Context, scope [sha256.Size]byte, reason string, now time.Time) (StockApplyPauseAck, error) {
	if now.IsZero() || !stockApplyReasonText(reason, 256) {
		return StockApplyPauseAck{}, ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return StockApplyPauseAck{}, err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	ack := StockApplyPauseAck{Scope: key, AcknowledgedAt: now.UTC()}
	err := c.journal.transaction(ctx, func(tx *sql.Tx) error {
		var paused int
		err := tx.QueryRow("SELECT paused FROM stock_apply_mode WHERE scope=?", key).Scan(&paused)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyJournal
		}
		already := err == nil && paused == 1
		if !already {
			if _, err := tx.Exec("INSERT INTO stock_apply_mode(scope,paused,reason,updated_at) VALUES(?,1,?,?) ON CONFLICT(scope) DO UPDATE SET paused=1,reason=excluded.reason,updated_at=excluded.updated_at", key, reason, now.UTC().Format(time.RFC3339Nano)); err != nil {
				return ErrStockApplyJournal
			}
			if err := stockApplyAudit(tx, key, "", "pause_acknowledged", reason); err != nil {
				return err
			}
		}
		var op StockApplyOperation
		row := tx.QueryRow("SELECT "+stockApplyOperationColumns+" FROM stock_apply_operation WHERE scope=? AND state IN ('proposed','dispatched','outcome_unknown') ORDER BY sequence DESC LIMIT 1", key)
		op, err = scanStockApplyOperation(row)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyJournal
		}
		if err == nil {
			held := op
			ack.Unresolved = &held
		}
		ack.Paused = true
		ack.LocalPauseApplied = true
		if already {
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

// AdmitScopeWork is the scheduler/control seam every controller loop must
// pass before starting new work under a scope. It fails closed: a paused
// scope admits nothing new, an unresolved operation blocks new work even
// after the pause is lifted, and journal failures never silently become a
// writable scope. Admission here is permission to plan local work only; it
// grants no upstream write authority.
func (c *StockApplyController) AdmitScopeWork(ctx context.Context, scope [sha256.Size]byte, now time.Time) error {
	if now.IsZero() {
		return ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	var paused int
	err := c.journal.db.QueryRowContext(ctx, "SELECT paused FROM stock_apply_mode WHERE scope=?", key).Scan(&paused)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ErrStockApplyJournal
	}
	if err == nil && paused == 1 {
		return ErrStockApplyPaused
	}
	var unresolved int
	if err := c.journal.db.QueryRowContext(ctx, "SELECT count(*) FROM stock_apply_operation WHERE scope=? AND state IN ('proposed','dispatched','outcome_unknown')", key).Scan(&unresolved); err != nil {
		return ErrStockApplyJournal
	}
	if unresolved != 0 {
		return ErrStockApplyBlocked
	}
	return nil
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
	if now.IsZero() {
		return StockApplyRecovery{}, ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return StockApplyRecovery{}, err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	recovery := StockApplyRecovery{Scope: key, Inflight: StockApplyInflightNone, Reason: stockApplyReasonNoOpenOperation}
	var paused int
	err := c.journal.db.QueryRowContext(ctx, "SELECT paused FROM stock_apply_mode WHERE scope=?", key).Scan(&paused)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StockApplyRecovery{}, ErrStockApplyJournal
	}
	recovery.Paused = err == nil && paused == 1
	var op StockApplyOperation
	row := c.journal.db.QueryRowContext(ctx, "SELECT "+stockApplyOperationColumns+" FROM stock_apply_operation WHERE scope=? AND state IN ('proposed','dispatched','outcome_unknown') ORDER BY sequence DESC LIMIT 1", key)
	op, err = scanStockApplyOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return recovery, nil
	}
	if err != nil {
		return StockApplyRecovery{}, ErrStockApplyJournal
	}
	held := op
	recovery.Operation = &held
	switch op.State {
	case "proposed":
		recovery.Inflight = StockApplyInflightProposalOpen
		recovery.Withdrawable = true
		recovery.Reason = "proposal_open_withdrawal_available"
	case "dispatched":
		recovery.Inflight = StockApplyInflightOutcomeUnresolved
		recovery.Reason = stockApplyReasonAwaitingReceipt
	default:
		recovery.Inflight = StockApplyInflightOutcomeUnresolved
		recovery.Reason = stockApplyReasonOutcomeUncertain
	}
	return recovery, nil
}
