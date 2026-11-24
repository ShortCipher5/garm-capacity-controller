package brokerinput

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStockApplyPauseAckSerializesAgainstNewWork(t *testing.T) {
	controller, scope, _, _ := stockApplyTestController(t)
	now := time.Unix(1700000200, 0)
	ctx := context.Background()
	ack, err := controller.AcknowledgePause(ctx, scope, "operator stop", now)
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
	again, err := controller.AcknowledgePause(ctx, scope, "operator stop", now.Add(2*time.Second))
	if err != nil || !again.Paused || !again.LocalPauseApplied || again.Reason != stockApplyReasonAlreadyPaused {
		t.Fatalf("lost-response retry must be idempotent: %+v %v", again, err)
	}
	// An unrelated scope neither inherits the pause nor loses its own state.
	other := scope
	other[0] ^= 0xff
	if err := controller.AdmitScopeWork(ctx, other, now.Add(3*time.Second)); err != nil {
		t.Fatal("unrelated scope inherited the pause", err)
	}
}

func TestStockApplyConcurrentPauseAndAdmission(t *testing.T) {
	controller, scope, _, _ := stockApplyTestController(t)
	now := time.Unix(1700000200, 0)
	ctx := context.Background()
	plan, planNow := validatedStockApplyPlan(t, 1)
	const workers = 8
	var wg sync.WaitGroup
	admitted := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := controller.AdmitScopeWork(ctx, scope, now); err != nil {
				admitted[i] = err
				return
			}
			admitted[i] = controller.RecordProposal(ctx, plan, 0, planNow)
		}(i)
	}
	ackDone := make(chan StockApplyPauseAck, 1)
	go func() {
		ack, err := controller.AcknowledgePause(ctx, scope, "operator stop", now)
		if err != nil {
			t.Error(err)
			return
		}
		ackDone <- ack
	}()
	wg.Wait()
	<-ackDone
	// However the race resolved, after the acknowledged pause no new work
	// starts, and earlier admitted work is held rather than discarded.
	if err := controller.AdmitScopeWork(ctx, scope, now.Add(time.Second)); !errors.Is(err, ErrStockApplyPaused) {
		t.Fatal("work admitted after acknowledged pause", err)
	}
	recovery, err := controller.RecoverScope(ctx, scope, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !recovery.Paused {
		t.Fatal("recovery lost the acknowledged pause")
	}
	succeeded := 0
	for _, err := range admitted {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrStockApplyPaused) && !errors.Is(err, ErrStockApplyBlocked) {
			t.Fatalf("unexpected admission failure: %v", err)
		}
	}
	if succeeded > 1 {
		t.Fatalf("more than one proposal admitted: %d", succeeded)
	}
	if recovery.Inflight == StockApplyInflightProposalOpen && succeeded != 1 {
		t.Fatalf("held proposal and admission count disagree: %+v %d", recovery, succeeded)
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
	if err := controller.RecordObservation(ctx, plan.Scope, readback, 2, now.Add(8*time.Second)); err != nil {
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
