package brokerinput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func TestStockApplyPauseAckSerializesAgainstNewWork(t *testing.T) {
	controller, scope, path, lineage := stockApplyTestController(t)
	operator := reopenStockApplyController(t, path, lineage)
	now := time.Unix(1700000200, 0)
	ctx := context.Background()
	if err := controller.AdmitScopeWork(ctx, scope, now); err != nil {
		t.Fatalf("preflight before pause: %v", err)
	}
	ack, err := operator.AcknowledgePause(ctx, scope, "operator stop", now)
	if err != nil {
		t.Fatal(err)
	}
	// The acknowledgment establishes exactly one fact: the local pause. It
	// never claims a drained session or a settled upstream outcome.
	if !ack.Paused || !ack.LocalPauseApplied || ack.SessionDrained || ack.UpstreamSettled || ack.Unresolved != nil {
		t.Fatalf("pause ack overclaimed: %+v", ack)
	}
	if err := controller.AdmitScopeWork(ctx, scope, now.Add(time.Second)); !errors.Is(err, ErrStockApplyPaused) {
		t.Fatal("acknowledged pause admitted new work", err)
	}
	plan, planNow := validatedStockApplyPlan(t, 1)
	if err := controller.RecordProposal(ctx, plan, 0, planNow); !errors.Is(err, ErrStockApplyPaused) {
		t.Fatal("acknowledged pause accepted a new proposal", err)
	}
	// A repeated acknowledgment after a lost response is idempotent: same
	// durable state, no new unresolved work invented, no error.
	retryTime := now.Add(2 * time.Second)
	again, err := controller.AcknowledgePause(ctx, scope, "retry after lost response", retryTime)
	if err != nil || !again.Paused || !again.LocalPauseApplied || again.Reason != stockApplyReasonAlreadyPaused ||
		!again.AcknowledgedAt.Equal(retryTime) || again.Unresolved != nil || again.UpstreamSettled || again.SessionDrained {
		t.Fatalf("lost-response retry: %+v %v", again, err)
	}
	var reason, stamp string
	var events int
	key := hex.EncodeToString(scope[:])
	if err := operator.journal.db.QueryRow("SELECT reason,updated_at FROM stock_apply_mode WHERE scope=?", key).Scan(&reason, &stamp); err != nil {
		t.Fatal(err)
	}
	if err := operator.journal.db.QueryRow("SELECT count(*) FROM stock_apply_audit WHERE scope=?", key).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if reason != "operator stop" || stamp != now.UTC().Format(time.RFC3339Nano) || events != 1 {
		t.Fatalf("retry modified durable pause: reason=%q at=%q audit rows=%d", reason, stamp, events)
	}
	if err := operator.Resume(ctx, scope, "maintenance complete", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.AdmitScopeWork(ctx, scope, now.Add(4*time.Second)); err != nil {
		t.Fatalf("clean scope after resume: %v", err)
	}
	again, err = controller.AcknowledgePause(ctx, scope, "operator stop", now.Add(5*time.Second))
	if err != nil || again.Reason != stockApplyReasonPauseActive || again.UpstreamSettled || again.SessionDrained {
		t.Fatalf("reissued stop after resume: %+v %v", again, err)
	}
	if err := operator.RecordProposal(ctx, plan, 0, planNow); !errors.Is(err, ErrStockApplyPaused) {
		t.Fatalf("reissued stop allowed actual admission: %v", err)
	}
	// An unrelated scope neither inherits the pause nor loses its own state.
	other := scope
	other[0] ^= 0xff
	if err := controller.AdmitScopeWork(ctx, other, now.Add(3*time.Second)); err != nil {
		t.Fatal("unrelated scope inherited the pause", err)
	}
}

func TestStockApplyConcurrentPauseAndAdmission(t *testing.T) {
	controller, scope, path, lineage := stockApplyTestController(t)
	operator := reopenStockApplyController(t, path, lineage)
	now := time.Unix(1700000200, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	plan, planNow := validatedStockApplyPlan(t, 1)
	const workers = 8
	start := make(chan struct{})
	admitted := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			err := controller.AdmitScopeWork(ctx, scope, now)
			if err == nil {
				err = controller.RecordProposal(ctx, plan, 2, planNow)
			}
			admitted <- err
		}()
	}
	type pauseResult struct {
		ack StockApplyPauseAck
		err error
	}
	paused := make(chan pauseResult, 1)
	go func() {
		<-start
		ack, err := operator.AcknowledgePause(ctx, scope, "operator stop", now)
		paused <- pauseResult{ack, err}
	}()
	close(start)
	var ack StockApplyPauseAck
	select {
	case result := <-paused:
		if result.err != nil {
			t.Fatal(result.err)
		}
		ack = result.ack
	case <-ctx.Done():
		t.Fatal("pause acknowledgment timed out")
	}
	succeeded := 0
	for i := 0; i < workers; i++ {
		select {
		case err := <-admitted:
			if err == nil {
				succeeded++
			} else if !errors.Is(err, ErrStockApplyPaused) && !errors.Is(err, ErrStockApplyBlocked) {
				t.Fatalf("unexpected admission failure: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("proposal admission timed out")
		}
	}
	recovery, err := controller.RecoverScope(ctx, scope, now.Add(time.Second))
	if err != nil || !recovery.Paused || !ack.LocalPauseApplied || ack.UpstreamSettled || ack.SessionDrained {
		t.Fatalf("pause and recovery: %+v %+v %v", ack, recovery, err)
	}
	if succeeded > 1 || (succeeded == 1) != (ack.Unresolved != nil) || (succeeded == 1) != (recovery.Operation != nil) {
		t.Fatalf("commits=%d, pause=%+v, recovery=%+v", succeeded, ack, recovery)
	}
	if succeeded == 1 && (*recovery.Operation != *ack.Unresolved || ack.Unresolved.Charged != 2 ||
		recovery.Inflight != StockApplyInflightProposalOpen || recovery.Withdrawable) {
		t.Fatalf("paused proposal: pause=%+v recovery=%+v", ack.Unresolved, recovery)
	}
}

// During each transition the scope always has a dispatched operation, a
// committed pause, or both. Reading the pre-pause mode with the post-proof
// operation would incorrectly report a clear scope.
func TestStockApplySnapshotAcrossHandlesDuringSettlement(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	reader, scope, path, lineage := stockApplyTestController(t)
	writer := reopenStockApplyController(t, path, lineage)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	plan, now := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	for round := 0; round < 16; round++ {
		if err := writer.Resume(ctx, scope, "prepare fixture", now); err != nil {
			t.Fatal(err)
		}
		if err := writer.RecordProposal(ctx, plan, 2, now); err != nil {
			t.Fatal(err)
		}
		if err := writer.MarkDispatched(ctx, scope, claim, now); err != nil {
			t.Fatal(err)
		}
		proof := StockApplyOutcomeProof{OperationID: viewOperationID(t, writer, scope), ReceiptDigest: [32]byte{1}, ChallengeDigest: [32]byte{2}}
		done := make(chan error, 1)
		go func() {
			_, err := writer.AcknowledgePause(ctx, scope, "operator stop", now)
			if err == nil {
				err = writer.RecordProvenOutcome(ctx, scope, claim, proof, now)
			}
			done <- err
		}()
		for i := 0; i < 8; i++ {
			recovery, err := reader.RecoverScope(ctx, scope, now)
			if err != nil || (!recovery.Paused && recovery.Operation == nil) {
				t.Fatalf("incoherent recovery: %+v %v", recovery, err)
			}
			if recovery.Operation != nil && (recovery.Operation.OperationID != proof.OperationID || recovery.Operation.Charged != 2) {
				t.Fatalf("recovery lost held work: %+v", recovery.Operation)
			}
			err = reader.AdmitScopeWork(ctx, scope, now)
			if !errors.Is(err, ErrStockApplyBlocked) && !errors.Is(err, ErrStockApplyPaused) {
				t.Fatalf("preflight missed both the operation and pause: %v", err)
			}
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("pause and settlement timed out")
		}
	}
}

func TestStockApplyPauseAckHoldsUnresolvedAndResumeStaysBlocked(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	ctx := context.Background()
	if err := controller.RecordProposal(ctx, plan, 2, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkDispatched(ctx, plan.Scope, claim, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkOutcomeUncertain(ctx, plan.Scope, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	operationID := viewOperationID(t, controller, plan.Scope)
	ack, err := controller.AcknowledgePause(ctx, plan.Scope, "operator stop", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// The possibly accepted update stays unresolved and charged in the ack;
	// the pause is not a settlement.
	if ack.Unresolved == nil || ack.Unresolved.OperationID != operationID || ack.Unresolved.State != "outcome_unknown" ||
		ack.Unresolved.Charged != 2 || ack.UpstreamSettled || ack.SessionDrained {
		t.Fatalf("pause ack lost or settled unresolved work: %+v", ack)
	}
	retry, err := controller.AcknowledgePause(ctx, plan.Scope, "retry after lost response", now.Add(4*time.Second))
	if err != nil || retry.Unresolved == nil || *retry.Unresolved != *ack.Unresolved ||
		retry.Reason != stockApplyReasonAlreadyPaused || retry.SessionDrained || retry.UpstreamSettled {
		t.Fatalf("lost-ack retry changed held work or claimed settlement: %+v %v", retry, err)
	}
	// Lifting the pause changes only the local pause state: new work stays
	// blocked and no retry, resolution or premature resume is permitted.
	if err := controller.Resume(ctx, plan.Scope, "maintenance done", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.AdmitScopeWork(ctx, plan.Scope, now.Add(5*time.Second)); !errors.Is(err, ErrStockApplyBlocked) {
		t.Fatal("lifted pause admitted new work over an unresolved update", err)
	}
	if err := controller.MarkDispatched(ctx, plan.Scope, claim, now.Add(6*time.Second)); !errors.Is(err, ErrStockApplyBlocked) {
		t.Fatal("unresolved update was retried", err)
	}
	if err := controller.RecordProvenOutcome(ctx, plan.Scope, StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, StockApplyOutcomeProof{OperationID: operationID, ReceiptDigest: [32]byte{1}, ChallengeDigest: [32]byte{2}}, now.Add(7*time.Second)); !errors.Is(err, ErrStockApplyUnavailable) {
		t.Fatal("stock claim resolved an uncertain outcome", err)
	}
	readback := plan.Suggested
	if err := controller.RecordObservation(ctx, plan.Scope, operationID, readback, 2, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err := controller.View(ctx, plan.Scope, stockApplyEval(now.Add(9*time.Second), claim, 2, &readback, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateOutcomeUnknown || view.Resume.Ready {
		t.Fatalf("a matching readback resolved or readied an uncertain update: %+v", view)
	}
	if enabled, _ := actionReason(view, "apply"); enabled {
		t.Fatal("apply enabled over an unresolved update")
	}
}

func TestStockApplyRecoveryAfterRestartKeepsInflightDistinct(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	ctx := context.Background()
	now := time.Unix(1700000200, 0)

	// An unsubmitted proposal keeps its explicit withdrawal path across a
	// restart.
	controller, scope, path, lineage := stockApplyTestController(t)
	plan, planNow := validatedStockApplyPlan(t, 1)
	if err := controller.RecordProposal(ctx, plan, 1, planNow); err != nil {
		t.Fatal(err)
	}
	controller.Close()
	reopened := reopenStockApplyController(t, path, lineage)
	recovery, err := reopened.RecoverScope(ctx, scope, now)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Inflight != StockApplyInflightProposalOpen || !recovery.Withdrawable || recovery.Operation == nil {
		t.Fatalf("restart lost the withdrawal path: %+v", recovery)
	}
	if err := reopened.WithdrawProposal(ctx, scope, "operator reviewed after restart", now.Add(time.Second)); err != nil {
		t.Fatal("withdrawal after restart refused", err)
	}
	recovery, err = reopened.RecoverScope(ctx, scope, now.Add(2*time.Second))
	if err != nil || recovery.Inflight != StockApplyInflightNone || recovery.Operation != nil {
		t.Fatalf("withdrawn proposal still in flight: %+v %v", recovery, err)
	}
	if err := reopened.AdmitScopeWork(ctx, scope, now.Add(3*time.Second)); err != nil {
		t.Fatal("clean scope blocked after withdrawal", err)
	}

	// A possibly accepted update stays unresolved across restart with the
	// same operation identity and charges, on a healthy new process.
	controller2, scope2, path2, lineage2 := stockApplyTestController(t)
	plan2, now2 := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	if err := controller2.RecordProposal(ctx, plan2, 3, now2); err != nil {
		t.Fatal(err)
	}
	if err := controller2.MarkDispatched(ctx, plan2.Scope, claim, now2.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := controller2.AcknowledgePause(ctx, plan2.Scope, "operator stop", now2.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	operationID := viewOperationID(t, controller2, plan2.Scope)
	controller2.Close()
	reopened2 := reopenStockApplyController(t, path2, lineage2)
	recovery, err = reopened2.RecoverScope(ctx, scope2, now2.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Inflight != StockApplyInflightOutcomeUnresolved || recovery.Withdrawable ||
		recovery.Operation == nil || recovery.Operation.OperationID != operationID || recovery.Operation.Charged != 3 ||
		!recovery.Paused {
		t.Fatalf("restart resolved or rewrote an uncertain update: %+v", recovery)
	}
	if err := reopened2.WithdrawProposal(ctx, scope2, "attempted withdrawal of accepted work", now2.Add(4*time.Second)); err == nil {
		t.Fatal("possibly accepted work was discarded", err)
	}
	if err := reopened2.AdmitScopeWork(ctx, scope2, now2.Add(5*time.Second)); !errors.Is(err, ErrStockApplyPaused) {
		t.Fatal("paused scope admitted work after restart", err)
	}
	// Pause blocks new work only; recording uncertainty about already
	// accepted work is still permitted and changes nothing else.
	if err := reopened2.MarkOutcomeUncertain(ctx, scope2, now2.Add(6*time.Second)); err != nil {
		t.Fatal("uncertainty recording under pause refused", err)
	}
	// Only an independently authoritative outcome bound to the exact
	// operation resolves it, even after restart.
	proof := StockApplyOutcomeProof{OperationID: operationID, ReceiptDigest: [32]byte{9}, ChallengeDigest: [32]byte{8}}
	if err := reopened2.RecordProvenOutcome(ctx, scope2, claim, proof, now2.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	recovery, err = reopened2.RecoverScope(ctx, scope2, now2.Add(8*time.Second))
	if err != nil || recovery.Inflight != StockApplyInflightNone {
		t.Fatalf("proven outcome still in flight: %+v %v", recovery, err)
	}
	// Lifting the pause after settlement admits new work again.
	if err := reopened2.Resume(ctx, scope2, "maintenance done", now2.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := reopened2.AdmitScopeWork(ctx, scope2, now2.Add(10*time.Second)); err != nil {
		t.Fatal("settled scope stayed blocked", err)
	}
}

func TestStockApplyPauseAckNeverFabricatesSettlement(t *testing.T) {
	controller, scope, _, _ := stockApplyTestController(t)
	now := time.Unix(1700000200, 0)
	// Even with no operation at all, the acknowledgment reports only the
	// local pause; session drain and upstream settlement stay unproven.
	ack, err := controller.AcknowledgePause(context.Background(), scope, "operator stop", now)
	if err != nil {
		t.Fatal(err)
	}
	if ack.SessionDrained || ack.UpstreamSettled {
		t.Fatalf("local pause fabricated upstream facts: %+v", ack)
	}
	// Invalid input never pauses.
	if _, err := controller.AcknowledgePause(context.Background(), scope, "", now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("empty reason accepted", err)
	}
	if _, err := controller.AcknowledgePause(context.Background(), scope, "operator stop", time.Time{}); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("zero clock accepted", err)
	}
	if err := controller.AdmitScopeWork(context.Background(), scope, time.Time{}); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("zero clock admitted", err)
	}
}

func TestStockApplyZeroScopeRejected(t *testing.T) {
	controller, _, _, _ := stockApplyTestController(t)
	ctx := context.Background()
	plan, now := validatedStockApplyPlan(t, 1)
	var scope [sha256.Size]byte
	plan.Scope = scope
	claim := StockApplyCapabilityClaim{Interface: StockApplyStockInterface}
	_, ackErr := controller.AcknowledgePause(ctx, scope, "operator stop", now)
	_, recoverErr := controller.RecoverScope(ctx, scope, now)
	_, viewErr := controller.View(ctx, scope, stockApplyEval(now, claim, 0, nil, true))
	for name, err := range map[string]error{
		"acknowledge": ackErr,
		"recover":     recoverErr,
		"view":        viewErr,
		"preflight":   controller.AdmitScopeWork(ctx, scope, now),
		"propose":     controller.RecordProposal(ctx, plan, 0, now),
		"pause":       controller.Pause(ctx, scope, "operator stop", now),
		"resume":      controller.Resume(ctx, scope, "resume", now),
		"withdraw":    controller.WithdrawProposal(ctx, scope, "withdraw", now),
		"dispatch":    controller.MarkDispatched(ctx, scope, claim, now),
		"uncertain":   controller.MarkOutcomeUncertain(ctx, scope, now),
		"prove":       controller.RecordProvenOutcome(ctx, scope, claim, StockApplyOutcomeProof{}, now),
		"observe":     controller.RecordObservation(ctx, scope, "00000000000000000000000000000000", 1, 1, now),
	} {
		if !errors.Is(err, ErrStockApplyConflict) {
			t.Errorf("%s accepted zero scope or reported a different error: %v", name, err)
		}
	}
}

func TestStockApplyGuardedModeAcrossHandles(t *testing.T) {
	c, scope, path, lineage := stockApplyTestController(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	other := reopenStockApplyController(t, path, lineage)
	plan, now := validatedStockApplyPlan(t, 1)
	initial, err := c.RecoverScope(ctx, scope, now)
	if err != nil {
		t.Fatal(err)
	}

	// Two requests based on the same review can commit exactly one decision.
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, handle := range []*StockApplyController{c, other} {
		go func() {
			<-start
			_, err := handle.ChangeScopePause(ctx, scope, initial.ModeRevision, true, "reviewed stop", now)
			results <- err
		}()
	}
	close(start)
	var commits, conflicts int
	for range 2 {
		select {
		case err := <-results:
			switch {
			case err == nil:
				commits++
			case errors.Is(err, ErrStockApplyConflict):
				conflicts++
			default:
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("guarded mode change timed out")
		}
	}
	if commits != 1 || conflicts != 1 {
		t.Fatalf("commits=%d conflicts=%d", commits, conflicts)
	}
	paused, err := c.RecoverScope(ctx, scope, now)
	if err != nil || !paused.Paused || paused.ModeRevision == initial.ModeRevision {
		t.Fatalf("after stop: %+v %v", paused, err)
	}
	if err := other.RecordProposal(ctx, plan, 3, now); !errors.Is(err, ErrStockApplyPaused) {
		t.Fatalf("paused scope admitted proposal: %v", err)
	}
	// Older controller methods use the same journal. Even with the same
	// timestamp and identical final mode, an old resume must fail.
	if err := other.Resume(ctx, scope, "previous work complete", now); err != nil {
		t.Fatal(err)
	}
	if _, err := other.AcknowledgePause(ctx, scope, "separate operator hold", now); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChangeScopePause(ctx, scope, paused.ModeRevision, false, "delayed resume", now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatalf("stale resume: %v", err)
	}
	fresh, err := c.RecoverScope(ctx, scope, now)
	if err != nil || !fresh.Paused || fresh.ModeRevision == paused.ModeRevision {
		t.Fatalf("lost newer pause: %+v %v", fresh, err)
	}
	if _, err := c.ChangeScopePause(ctx, scope, fresh.ModeRevision, false, "new review", now); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ChangeScopePause(ctx, scope, initial.ModeRevision, true, "delayed original stop", now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatalf("stale stop: %v", err)
	}
	if err := other.RecordProposal(ctx, plan, 3, now); err != nil {
		t.Fatal(err)
	}
}

func TestStockApplyModeRevisionBindingsAndRollback(t *testing.T) {
	c, scope, _, _ := stockApplyTestController(t)
	foreign, _, _, _ := stockApplyTestController(t)
	ctx := context.Background()
	now := time.Unix(1700000200, 0)
	review, err := c.RecoverScope(ctx, scope, now)
	if err != nil {
		t.Fatal(err)
	}
	other := sha256.Sum256([]byte("another scope"))
	if _, err := c.ChangeScopePause(ctx, other, review.ModeRevision, true, "wrong scope", now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatalf("cross-scope review: %v", err)
	}
	if _, err := foreign.ChangeScopePause(ctx, scope, review.ModeRevision, true, "wrong journal", now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatalf("cross-lineage review: %v", err)
	}
	if err := c.Pause(ctx, other, "independent stop", now); err != nil {
		t.Fatal(err)
	}
	// Failure while appending the audit must roll back the mode write.
	_, err = c.journal.db.Exec("CREATE TRIGGER reject_mode_audit BEFORE INSERT ON stock_apply_audit BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := c.ChangeScopePause(ctx, scope, review.ModeRevision, true, "reviewed stop", now); err == nil || result.ModeRevision != "" {
		t.Fatalf("returned an acknowledgment after audit failure: %+v %v", result, err)
	}
	got, err := c.RecoverScope(ctx, scope, now)
	if err != nil || got != review {
		t.Fatalf("audit failure changed scope: %+v %v", got, err)
	}
	if _, err = c.journal.db.Exec("DROP TRIGGER reject_mode_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChangeScopePause(ctx, scope, review.ModeRevision, true, "after audit repair", now); err != nil {
		t.Fatal("unrelated scope or failed audit invalidated original review", err)
	}
}

func TestStockApplyGuardedResumeKeepsUnknownHeld(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	c, scope, path, lineage := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	ctx := context.Background()
	if err := c.RecordProposal(ctx, plan, 4, now); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkDispatched(ctx, scope, stockApplyFixtureClaim(), now); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkOutcomeUncertain(ctx, scope, now); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordObservation(ctx, scope, viewOperationID(t, c, scope), plan.Suggested, 4, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := c.Pause(ctx, scope, "local stop", now); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := reopenStockApplyController(t, path, lineage)
	review, err := reopened.RecoverScope(ctx, scope, now)
	if err != nil || review.Operation == nil || review.Inflight != StockApplyInflightOutcomeUnresolved {
		t.Fatalf("restart: %+v %v", review, err)
	}
	after, err := reopened.ChangeScopePause(ctx, scope, review.ModeRevision, false, "lift local hold", now)
	if err != nil || after.Paused || after.Withdrawable || after.Operation == nil || *after.Operation != *review.Operation || after.Inflight != review.Inflight || after.Operation.Charged != 4 {
		t.Fatalf("resume changed the uncertain operation: %+v %v", after, err)
	}
	if err := reopened.RecordProposal(ctx, plan, 4, now); !errors.Is(err, ErrStockApplyBlocked) {
		t.Fatalf("resume admitted another proposal: %v", err)
	}
}
