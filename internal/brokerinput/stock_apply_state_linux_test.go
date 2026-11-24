package brokerinput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var stockApplyTestIdentity = sha256.Sum256([]byte("fixture controller instance"))

func stockApplyPrivateDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

// stockApplyTestController initializes a journal, records its lineage the way
// trusted configuration would, and returns the bound controller.
func stockApplyTestController(t *testing.T) (*StockApplyController, [sha256.Size]byte, string, string) {
	t.Helper()
	path := filepath.Join(stockApplyPrivateDir(t), "stock-apply.sqlite3")
	journal, err := InitializeStockApplyJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	lineage := journal.Lineage()
	controller, err := NewStockApplyController(journal, hex.EncodeToString(stockApplyTestIdentity[:]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controller.Close() })
	scope := sha256.Sum256([]byte("same reviewed configuration, bindings and profiles"))
	return controller, scope, path, lineage
}

func reopenStockApplyController(t *testing.T, path, lineage string) *StockApplyController {
	t.Helper()
	if _, err := OpenStockApplyJournal(path, ""); err == nil {
		t.Fatal("empty lineage admitted")
	}
	journal, err := OpenStockApplyJournal(path, lineage)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := OpenStockApplyController(journal, hex.EncodeToString(stockApplyTestIdentity[:]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controller.Close() })
	return controller
}

func validatedStockApplyPlan(t *testing.T, delta int64) (StockChangePlan, time.Time) {
	t.Helper()
	prior, current, request, options, now := changePlanFixture(delta)
	plan, err := validateStockChangePlan(prior, current, request, options, now)
	if err != nil {
		t.Fatal(err)
	}
	return plan, now
}

func stockApplyEval(now time.Time, claim StockApplyCapabilityClaim, baseline int64, readback *int64, fresh bool) StockApplyEvaluation {
	return StockApplyEvaluation{Now: now, Claim: claim, BaselineCharged: baseline, Readback: readback, Fresh: fresh}
}

func actionReason(view StockApplyView, name string) (bool, string) {
	for _, action := range view.Actions {
		if action.Action == name {
			return action.Enabled, action.Reason
		}
	}
	return false, "action missing"
}

func viewOperationID(t *testing.T, controller *StockApplyController, scope [sha256.Size]byte) string {
	t.Helper()
	view, err := controller.View(context.Background(), scope, stockApplyEval(time.Unix(1700000300, 0), StockApplyCapabilityClaim{}, 0, nil, true))
	if err != nil || view.Operation == nil {
		t.Fatal(err, view.Operation)
	}
	return view.Operation.OperationID
}

func TestStockApplyControllerDefaultsUnavailableWithEverythingDisabled(t *testing.T) {
	controller, scope, _, _ := stockApplyTestController(t)
	now := time.Unix(1700000200, 0)
	view, err := controller.View(context.Background(), scope, stockApplyEval(now, StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, 7, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateUnavailable || view.Reason != "no_recorded_operation" {
		t.Fatal("default state", view.State, view.Reason)
	}
	if view.Capability.Supported {
		t.Fatal("stock capability supported by default")
	}
	// Every Garm-effecting command stays disabled; pause is only the
	// controller-local maintenance entry and resume has nothing to exit.
	if enabled, reason := actionReason(view, "apply"); enabled || reason != string(StockApplyRefusalStockObserveOnly) {
		t.Fatal("apply reason", enabled, reason)
	}
	if enabled, _ := actionReason(view, "resolve_outcome"); enabled {
		t.Fatal("resolve enabled by default")
	}
	if enabled, reason := actionReason(view, "rollback"); enabled || reason != "rollback_requires_original_outcome_and_current_state" {
		t.Fatal("rollback reason", enabled, reason)
	}
	if enabled, _ := actionReason(view, "pause"); !enabled {
		t.Fatal("maintenance entry unavailable")
	}
	if enabled, reason := actionReason(view, "resume"); enabled || reason != "not_paused" {
		t.Fatal("resume reason", enabled, reason)
	}
	if view.Charged.Runners != 7 || view.Charged.Reason == "" {
		t.Fatal("baseline charge", view.Charged)
	}
}

func TestStockApplyRecordProposalFromValidatedPlan(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	view, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now, stockApplyFixtureClaim(), 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateProposed || view.Operation == nil || view.Operation.Suggested != 4 || view.Operation.PoolID != "pool" {
		t.Fatal("proposed view", view.State, view.Operation)
	}
	if view.Charged.Runners != 1 || view.Charged.Reason != stockApplyChargedProposed {
		t.Fatal("proposed charge", view.Charged)
	}
	// Even a hypothetically supported capability admits no apply: no adapter ships.
	if enabled, reason := actionReason(view, "apply"); enabled || reason != "no_supported_adapter_implemented" {
		t.Fatal("apply with supported capability but no adapter", enabled, reason)
	}
	if enabled, reason := actionReason(view, "withdraw_proposal"); !enabled || reason != "" {
		t.Fatal("withdraw proposed", enabled, reason)
	}
	// The stock claim cannot dispatch anything and names the interface refusal.
	stockView, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now, StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if enabled, reason := actionReason(stockView, "apply"); enabled || reason != string(StockApplyRefusalStockObserveOnly) {
		t.Fatal("stock apply reason", enabled, reason)
	}
	// Stale evaluation evidence is visible even when the plan itself is fresh.
	staleView, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now, stockApplyFixtureClaim(), 1, nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if enabled, reason := actionReason(staleView, "apply"); enabled || reason != "stale_evidence" {
		t.Fatal("stale apply reason", enabled, reason)
	}
}

func TestStockApplyRecordProposalRefusesStaleActionableAndInvalid(t *testing.T) {
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	if err := controller.RecordProposal(context.Background(), plan, 1, now.Add(time.Minute)); !errors.Is(err, ErrStockApplyStale) {
		t.Fatal("stale plan admitted", err)
	}
	plan.Actionable = true
	if err := controller.RecordProposal(context.Background(), plan, 1, now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("actionable plan admitted", err)
	}
	plan.Actionable = false
	plan.Direction = "decrease"
	if err := controller.RecordProposal(context.Background(), plan, 1, now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("direction mismatch admitted", err)
	}
	plan.Direction = "increase"
	if err := controller.RecordProposal(context.Background(), plan, -1, now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("negative charge admitted", err)
	}
	if err := controller.RecordProposal(context.Background(), StockChangePlan{}, 1, now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("empty plan admitted", err)
	}
}

func TestStockApplyPauseBlocksNewWorkPersistsAcrossRestartAndPreservesProposals(t *testing.T) {
	controller, _, path, lineage := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.Pause(context.Background(), plan.Scope, "operator maintenance window", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordProposal(context.Background(), plan, 1, now.Add(2*time.Second)); !errors.Is(err, ErrStockApplyPaused) {
		t.Fatal("proposal during pause admitted", err)
	}
	view, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(2*time.Second), stockApplyFixtureClaim(), 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStatePausedMaintenance || view.Reason != "operator_pause_active" || view.Operation == nil {
		t.Fatal("paused view", view.State, view.Operation)
	}
	controller.Close()
	reopened := reopenStockApplyController(t, path, lineage)
	view, err = reopened.View(context.Background(), plan.Scope, stockApplyEval(now.Add(3*time.Second), stockApplyFixtureClaim(), 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStatePausedMaintenance || view.Operation == nil || view.Operation.State != "proposed" {
		t.Fatal("pause did not persist across restart", view.State, view.Operation)
	}
	if err := reopened.Resume(context.Background(), plan.Scope, "maintenance complete", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err = reopened.View(context.Background(), plan.Scope, stockApplyEval(now.Add(4*time.Second), stockApplyFixtureClaim(), 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateProposed || view.Paused {
		t.Fatal("resume did not restore the proposal", view.State, view.Paused)
	}
}

func TestStockApplyPauseNeverClearsUnknownState(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkDispatched(context.Background(), plan.Scope, claim, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkOutcomeUncertain(context.Background(), plan.Scope, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.Pause(context.Background(), plan.Scope, "investigate interrupted write", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.Resume(context.Background(), plan.Scope, "back from maintenance", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(4*time.Second), claim, 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateOutcomeUnknown || view.Paused || view.Operation == nil || view.Operation.State != "outcome_unknown" {
		t.Fatal("resume cleared unknown state", view.State, view.Paused, view.Operation)
	}
	if err := controller.RecordProposal(context.Background(), plan, 1, now.Add(5*time.Second)); !errors.Is(err, ErrStockApplyBlocked) {
		t.Fatal("new proposal during unknown admitted", err)
	}
	if err := controller.WithdrawProposal(context.Background(), plan.Scope, "try to clear it", now.Add(5*time.Second)); !errors.Is(err, ErrStockApplyBlocked) {
		t.Fatal("withdraw of unknown admitted", err)
	}
}

func TestStockApplyRestartPresentsDispatchedAsOutcomeUnknownAndHoldsCharges(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	controller, _, path, lineage := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	if err := controller.RecordProposal(context.Background(), plan, 2, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkDispatched(context.Background(), plan.Scope, claim, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	controller.Close()
	reopened := reopenStockApplyController(t, path, lineage)
	view, err := reopened.View(context.Background(), plan.Scope, stockApplyEval(now.Add(2*time.Second), claim, 2, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateOutcomeUnknown || view.Reason != "dispatched_awaiting_authoritative_receipt" {
		t.Fatal("restart did not conservatively present the dispatched write", view.State, view.Reason)
	}
	if view.Charged.Runners != 2 || view.Charged.Reason != stockApplyChargedUncertain {
		t.Fatal("restart released charges", view.Charged)
	}
	if err := reopened.MarkOutcomeUncertain(context.Background(), plan.Scope, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestStockApplyMatchingReadbackNeverAppliesOrReleases(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	if err := controller.RecordProposal(context.Background(), plan, 2, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkDispatched(context.Background(), plan.Scope, claim, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkOutcomeUncertain(context.Background(), plan.Scope, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	for i, readback := range []int64{plan.Suggested, plan.Observed, plan.Suggested} {
		// The ledger baseline moves while the operation is open; the held
		// capacity display must follow it without touching the outcome.
		if err := controller.RecordObservation(context.Background(), plan.Scope, readback, int64(5+i), now.Add(time.Duration(3+i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		view, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(time.Duration(3+i)*time.Second), claim, 5, &readback, true))
		if err != nil {
			t.Fatal(err)
		}
		if view.State != StockApplyStateOutcomeUnknown || view.Drift {
			t.Fatal("readback changed the durable outcome", readback, view.State, view.Drift)
		}
		if view.Charged.Runners != int64(5+i) || view.Charged.Reason != stockApplyChargedUncertain {
			t.Fatal("current charged baseline not reflected", view.Charged)
		}
	}
	proof := StockApplyOutcomeProof{OperationID: viewOperationID(t, controller, plan.Scope), ReceiptDigest: sha256.Sum256([]byte("authoritative receipt")), ChallengeDigest: sha256.Sum256([]byte("fence challenge"))}
	if err := controller.RecordProvenOutcome(context.Background(), plan.Scope, claim, proof, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(11*time.Second), claim, 2, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateAppliedWhenProven || view.Charged.Reason != stockApplyChargedProven || view.Charged.Runners != 7 {
		t.Fatal("proven outcome", view.State, view.Charged)
	}
	old := plan.Observed
	if err := controller.RecordObservation(context.Background(), plan.Scope, old, 9, now.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err = controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(12*time.Second), claim, 9, &old, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateAppliedWhenProven || !view.Drift {
		t.Fatal("late matching observation reverted or hid the proven outcome", view.State, view.Drift)
	}
	if view.Charged.Runners != 9 || view.Charged.Reason != stockApplyChargedProven {
		t.Fatal("proven charge does not reflect the current baseline", view.Charged)
	}
}

func TestStockApplyDriftBlocksDispatchWithVisibleReason(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordObservation(context.Background(), plan.Scope, 99, 1, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(2*time.Second), claim, 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if !view.Drift || view.State != StockApplyStateProposed {
		t.Fatal("drift view", view.Drift, view.State)
	}
	if enabled, reason := actionReason(view, "apply"); enabled || reason != "settings_drift_detected" {
		t.Fatal("drift apply reason", enabled, reason)
	}
	if err := controller.MarkDispatched(context.Background(), plan.Scope, claim, now.Add(3*time.Second)); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("drifted proposal dispatched", err)
	}
	// Drift is sticky: a matching readback afterwards does not silently clear it.
	if err := controller.RecordObservation(context.Background(), plan.Scope, plan.Observed, 1, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err = controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(4*time.Second), claim, 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if !view.Drift {
		t.Fatal("drift cleared by matching observation")
	}
}

func TestStockApplyProvenOutcomeRequiresSupportedCapabilityAndBoundProof(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkDispatched(context.Background(), plan.Scope, StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, now.Add(time.Second)); !errors.Is(err, ErrStockApplyUnavailable) {
		t.Fatal("stock capability dispatched", err)
	}
	if err := controller.MarkDispatched(context.Background(), plan.Scope, claim, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	operationID := viewOperationID(t, controller, plan.Scope)
	proof := StockApplyOutcomeProof{OperationID: operationID, ReceiptDigest: sha256.Sum256([]byte("receipt")), ChallengeDigest: sha256.Sum256([]byte("challenge"))}
	if err := controller.RecordProvenOutcome(context.Background(), plan.Scope, StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, proof, now.Add(2*time.Second)); !errors.Is(err, ErrStockApplyUnavailable) {
		t.Fatal("stock capability proved an outcome", err)
	}
	broken := proof
	broken.ReceiptDigest = [sha256.Size]byte{}
	if err := controller.RecordProvenOutcome(context.Background(), plan.Scope, claim, broken, now.Add(2*time.Second)); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("incomplete proof admitted", err)
	}
	broken = proof
	broken.OperationID = "ffffffffffffffffffffffffffffffff"
	if err := controller.RecordProvenOutcome(context.Background(), plan.Scope, claim, broken, now.Add(2*time.Second)); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("misbinding proof admitted", err)
	}
	if err := controller.RecordProvenOutcome(context.Background(), plan.Scope, claim, proof, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkDispatched(context.Background(), plan.Scope, claim, now.Add(3*time.Second)); !errors.Is(err, ErrStockApplyBlocked) {
		t.Fatal("dispatch after proven outcome admitted", err)
	}
}

func TestStockApplyWithdrawProposalRequiresReasonAndLeavesNoOperation(t *testing.T) {
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, -1)
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.WithdrawProposal(context.Background(), plan.Scope, "", now.Add(time.Second)); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("withdraw without reason admitted", err)
	}
	if err := controller.WithdrawProposal(context.Background(), plan.Scope, "operator reviewed current evidence", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(2*time.Second), StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateUnavailable || view.Operation != nil {
		t.Fatal("withdrawal left an operation", view.State, view.Operation)
	}
	if err := controller.WithdrawProposal(context.Background(), plan.Scope, "again", now.Add(3*time.Second)); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("withdraw without operation admitted", err)
	}
	if err := controller.RecordProposal(context.Background(), plan, 1, now.Add(4*time.Second)); err != nil {
		t.Fatal("fresh proposal after withdrawal refused", err)
	}
}

func TestStockApplyInverseAdviceRequiresObservedOutcomeAndFreshValidation(t *testing.T) {
	controller, _, _, _ := stockApplyTestController(t)
	now := time.Unix(1700000100, 0)
	scope := sha256.Sum256([]byte("same reviewed configuration, bindings and profiles"))
	makeSample := func(start time.Time) scalingPreviewSample {
		return scalingPreviewSample{scope: scope, StockScalingSample: StockScalingSample{source: "checked-source", sequence: 5, revision: 2, budget: 10, start: start, finish: start.Add(time.Second), expires: now.Add(20 * time.Second), pools: map[string]scalingPool{"pool": {repo: "repo", fingerprint: "checked-settings", enabled: true, current: 4, idle: 1, used: 1, cap: 8, target: 3}}, repoCaps: map[string]int64{"repo": 10}, order: []string{"pool"}, configuredMaximumFit: roomyMaximumFit("pool")}}
	}
	prior, current := makeSample(now.Add(-30*time.Second)), makeSample(now.Add(-10*time.Second))
	options := stockChangePlanOptions{EnableValidation: true, MaxStep: 1, Minimum: 1, Maximum: 8}
	old := StockChangePlan{PoolID: "pool", RepositoryID: "repo", Source: "checked-source", Fingerprint: "checked-settings", Scope: scope, Sequence: 4, Revision: 2, Observed: 3, Suggested: 4, Delta: 1, Direction: "increase"}
	if advice := assessStockInverseAdvice(old, stockInverseOutcomeUnknown, prior, current, options, now); advice.Plan != nil || advice.Reason != "outcome_uncertain" {
		t.Fatal("uncertain history advised a rollback", advice)
	}
	advice := assessStockInverseAdvice(old, stockInverseOutcomeObserved, prior, current, options, now)
	if advice.Plan == nil || advice.Reason != "fresh_inverse_advice_only" || advice.Plan.Observed != 4 || advice.Plan.Suggested != 3 || advice.Plan.Actionable {
		t.Fatal("inverse advice", advice.Reason, advice.Plan)
	}
	if err := controller.RecordProposal(context.Background(), *advice.Plan, 1, now); err != nil {
		t.Fatal(err)
	}
	view, err := controller.View(context.Background(), scope, stockApplyEval(now, StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, 1, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateProposed || view.Operation.Direction != "decrease" {
		t.Fatal("inverse proposal", view.State, view.Operation)
	}
	if enabled, _ := actionReason(view, "rollback"); enabled {
		t.Fatal("rollback enabled on the stock path")
	}
}

func TestStockApplyObservationAndViewInputBounds(t *testing.T) {
	controller, scope, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordObservation(context.Background(), scope, -1, 0, now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("negative readback admitted", err)
	}
	if err := controller.RecordObservation(context.Background(), scope, 1000001, 0, now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("unbounded readback admitted", err)
	}
	if err := controller.RecordObservation(context.Background(), scope, 1, -1, now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("negative charged baseline admitted", err)
	}
	if err := controller.RecordObservation(context.Background(), scope, 1, 100001, now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("unbounded charged baseline admitted", err)
	}
	if _, err := controller.View(context.Background(), scope, stockApplyEval(time.Time{}, StockApplyCapabilityClaim{}, 0, nil, true)); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("zero evaluation time admitted", err)
	}
	if _, err := controller.View(context.Background(), scope, stockApplyEval(now, StockApplyCapabilityClaim{}, 100001, nil, true)); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("unbounded baseline admitted", err)
	}
	bad := int64(-1)
	if _, err := controller.View(context.Background(), scope, stockApplyEval(now, StockApplyCapabilityClaim{}, 0, &bad, true)); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("unbounded readback view admitted", err)
	}
	if err := controller.Pause(context.Background(), scope, "bad\nreason", now); !errors.Is(err, ErrStockApplyConflict) {
		t.Fatal("control-character pause reason admitted", err)
	}
	if err := controller.Resume(context.Background(), scope, "ok", now); err != nil {
		t.Fatal(err)
	}
}

func TestStockApplyJournalRejectsSwapAndWrongIdentity(t *testing.T) {
	controller, _, path, lineage := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	otherLineage := sha256.Sum256([]byte("other lineage"))
	if _, err := OpenStockApplyJournal(path, hex.EncodeToString(otherLineage[:])); !errors.Is(err, ErrStockApplyJournal) {
		t.Fatal("wrong lineage admitted", err)
	}
	journal, err := OpenStockApplyJournal(path, lineage)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity := sha256.Sum256([]byte("another instance"))
	if _, err := OpenStockApplyController(journal, hex.EncodeToString(otherIdentity[:])); !errors.Is(err, ErrStockApplyJournal) {
		t.Fatal("wrong identity admitted", err)
	}
	journal.Close()
	worldReadable := t.TempDir()
	if err := os.Chmod(worldReadable, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeStockApplyJournal(filepath.Join(worldReadable, "stock-apply.sqlite3")); !errors.Is(err, ErrStockApplyJournal) {
		t.Fatal("world-readable journal directory admitted", err)
	}
	mismatch, err := OpenStockApplyJournal(path, lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStockApplyController(mismatch, "not-hex-identity"); !errors.Is(err, ErrStockApplyJournal) {
		t.Fatal("malformed identity admitted")
	}
	mismatch.Close()
}

func TestStockApplyHeldCapacityFollowsBaselineAcrossRestart(t *testing.T) {
	controller, _, path, lineage := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	if err := controller.RecordProposal(context.Background(), plan, 2, now); err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordObservation(context.Background(), plan.Scope, plan.Observed, 6, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	controller.Close()
	reopened := reopenStockApplyController(t, path, lineage)
	view, err := reopened.View(context.Background(), plan.Scope, stockApplyEval(now.Add(2*time.Second), StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, 2, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StockApplyStateProposed || view.Charged.Runners != 6 {
		t.Fatal("charged baseline did not persist across restart", view.State, view.Charged)
	}
}

func TestStockApplyDelayedObservationCannotReplaceRecordedCharge(t *testing.T) {
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	ctx := context.Background()
	if err := controller.RecordProposal(ctx, plan, 1, now); err != nil {
		t.Fatal(err)
	}
	// Even a fresh observation with the same count advances the timestamp.
	for _, n := range []time.Duration{2, 4} {
		if err := controller.RecordObservation(ctx, plan.Scope, plan.Observed, 3, now.Add(n*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []time.Duration{3, 4} {
		if err := controller.RecordObservation(ctx, plan.Scope, plan.Observed+90, 0, now.Add(n*time.Second)); !errors.Is(err, ErrStockApplyConflict) {
			t.Fatalf("accepted stale observation at +%ds: %v", n, err)
		}
	}
	view, err := controller.View(ctx, plan.Scope, stockApplyEval(now.Add(5*time.Second), StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, 0, nil, false))
	if err != nil || view.Charged.Runners != 3 || view.Drift || !view.Operation.UpdatedAt.Equal(now.Add(4*time.Second)) {
		t.Fatalf("stale observation changed recorded status: %+v, %v", view, err)
	}
}

func TestStockApplyResumeDistinguishesPauseLiftFromFreshCheckReadiness(t *testing.T) {
	enableStockApplyFixtureInterface(t)
	controller, _, _, _ := stockApplyTestController(t)
	plan, now := validatedStockApplyPlan(t, 1)
	claim := stockApplyFixtureClaim()
	assertResume := func(at time.Time, wantLift bool, wantLiftReason string, wantReady bool, wantReason string) StockApplyView {
		t.Helper()
		readback := plan.Observed
		view, err := controller.View(context.Background(), plan.Scope, stockApplyEval(at, claim, 1, &readback, true))
		if err != nil {
			t.Fatal(err)
		}
		if view.Resume.LiftPause != wantLift || view.Resume.LiftReason != wantLiftReason || view.Resume.Ready != wantReady || view.Resume.Reason != wantReason {
			t.Fatalf("resume view at %s = %+v; want lift %v/%q ready %v/%q", at, view.Resume, wantLift, wantLiftReason, wantReady, wantReason)
		}
		return view
	}
	assertResume(now, false, "not_paused", false, "no_open_operation")
	if err := controller.RecordProposal(context.Background(), plan, 1, now); err != nil {
		t.Fatal(err)
	}
	// Fresh evidence matches the journaled observed value: with a hypothetically
	// supported capability all drift checks pass, so readiness is true.
	assertResume(now.Add(time.Second), false, "not_paused", true, "")
	if enabled, reason := actionReason(assertResume(now.Add(time.Second), false, "not_paused", true, ""), "resume"); enabled || reason != "not_paused" {
		t.Fatal("resume action outside pause", enabled, reason)
	}
	if err := controller.Pause(context.Background(), plan.Scope, "operator maintenance window", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Lifting the local pause is available, but readiness is explicitly NOT
	// implied: fresh drift checks still have to run afterwards.
	paused := assertResume(now.Add(3*time.Second), true, "", false, "pause_must_be_lifted_first")
	if enabled, reason := actionReason(paused, "resume"); !enabled || reason != "lifts_local_pause_only" {
		t.Fatal("pause lift action", enabled, reason)
	}
	if err := controller.Resume(context.Background(), plan.Scope, "maintenance complete", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertResume(now.Add(5*time.Second), false, "not_paused", true, "")
	// Stale evidence blocks readiness even with the pause lifted.
	stale := plan.Observed
	staleView, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(6*time.Second), claim, 1, &stale, false))
	if err != nil {
		t.Fatal(err)
	}
	if staleView.Resume.Ready || staleView.Resume.Reason != "stale_evidence" {
		t.Fatal("stale readiness", staleView.Resume)
	}
	missing, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(6*time.Second), claim, 1, nil, true))
	if err != nil || missing.Resume.Ready || missing.Resume.Reason != "stale_evidence" {
		t.Fatal("missing readback readiness", missing.Resume, err)
	}
	// A fresh readback that drifts from the journaled operation blocks readiness.
	drifted := plan.Suggested + 50
	driftView, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(7*time.Second), claim, 1, &drifted, true))
	if err != nil {
		t.Fatal(err)
	}
	if driftView.Resume.Ready || driftView.Resume.Reason != "settings_drift_detected" {
		t.Fatal("drifted readiness", driftView.Resume)
	}
	// The stock capability can never be ready; it names the interface refusal.
	stockView, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(8*time.Second), StockApplyCapabilityClaim{Interface: StockApplyStockInterface}, 1, &stale, true))
	if err != nil {
		t.Fatal(err)
	}
	if stockView.Resume.Ready || stockView.Resume.Reason != string(StockApplyRefusalStockObserveOnly) {
		t.Fatal("stock readiness", stockView.Resume)
	}
	// An unknown outcome blocks readiness with the conservative reason.
	if err := controller.MarkDispatched(context.Background(), plan.Scope, claim, now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkOutcomeUncertain(context.Background(), plan.Scope, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	unknownView, err := controller.View(context.Background(), plan.Scope, stockApplyEval(now.Add(11*time.Second), claim, 1, &stale, true))
	if err != nil {
		t.Fatal(err)
	}
	if unknownView.Resume.Ready || unknownView.Resume.Reason != "outcome_uncertain_pending_resolution" {
		t.Fatal("unknown readiness", unknownView.Resume)
	}
}
