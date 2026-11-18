package admission

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func exerciseNativeRetirement(t *testing.T, f nativeFixture, permit string, journal nativeidentity.ProtectedJournalObservation, policy nativeapi.Policy) {
	t.Helper()
	ctx := context.Background()
	s := f.store
	if _, e := s.db.Exec("CREATE TRIGGER retire_audit_fail BEFORE INSERT ON audit WHEN NEW.kind='native_retirement_requested' BEGIN SELECT RAISE(ABORT,'fault'); END"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RequestNativeRetirement(ctx, permit, journal, policy); e == nil {
		t.Fatal("audit failure accepted intent")
	}
	if _, _, e := s.LookupNativeRetirement(ctx, permit); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("partial intent", e)
	}
	if _, e := s.db.Exec("DROP TRIGGER retire_audit_fail"); e != nil {
		t.Fatal(e)
	}
	intent, e := s.RequestNativeRetirement(ctx, permit, journal, policy)
	if e != nil {
		t.Fatal("request retirement", e)
	}
	again, e := s.RequestNativeRetirement(ctx, permit, journal, policy)
	if e != nil || again != intent {
		t.Fatal("intent replay", e)
	}
	_, digest, e := s.LookupNativeRetirement(ctx, permit)
	if e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(sqliteBackupDir(t), "retirement-before-claim.db")
	if e = s.BackupSQLite(ctx, backup); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(sqliteBackupDir(t), "retirement-restored.db")
	if e = RestoreSQLite(ctx, backup, restored); e != nil {
		t.Fatal(e)
	}
	restoredStore, e := OpenSQLite(restored, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer restoredStore.Close()
	if c, e := restoredStore.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireStop); e == nil || c.ID != "" {
		t.Fatal("unreconciled restore claimed", c, e)
	}
	barrier, e := restoredStore.BeginReconciliation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = restoredStore.CompleteReconciliation(ctx, barrier, InventorySnapshot{Hosts: observations(sqliteSnapshot(t, restoredStore).Config, restoredStore.now())}); e != nil {
		t.Fatal(e)
	}
	if c, e := restoredStore.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireStop); e == nil || c.ID != "" {
		t.Fatal("restored old intent claimed", c, e)
	}
	if sqliteSnapshot(t, restoredStore).Allocated.Global.Runners != 1 {
		t.Fatal("restore released charge")
	}
	if _, e = restoredStore.db.Exec("DROP TRIGGER native_retirement_intent_permanent; DELETE FROM native_retirement_intents"); e != nil {
		t.Fatal(e)
	}
	if e = restoredStore.checkNativeRetirements(ctx); e == nil {
		t.Fatal("missing audited intent accepted")
	}
	if _, e = s.ClaimNativeRetirement(ctx, f.controller, permit, digest, NativeRetireStop); e == nil {
		t.Fatal("wrong owner")
	}
	if _, e = s.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireDelete); e == nil {
		t.Fatal("delete before stopped")
	}
	previous := s.runtimeID
	s.runtimeID = "test-runtime-refusal"
	if _, e = s.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireStop); !errors.Is(e, ErrRetirementAuthority) {
		t.Fatal("production capability bypass", e)
	}
	s.runtimeID = previous
	if _, e := s.db.Exec("CREATE TRIGGER retire_claim_audit_fail BEFORE INSERT ON audit WHEN NEW.kind='native_retirement_claimed' BEGIN SELECT RAISE(ABORT,'fault'); END"); e != nil {
		t.Fatal(e)
	}
	failedClaim, failedErr := s.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireStop)
	if failedErr == nil || failedClaim != (NativeRetirementClaim{}) {
		t.Fatal("rolled-back claim escaped", failedClaim, failedErr)
	}
	if _, e := s.db.Exec("DROP TRIGGER retire_claim_audit_fail"); e != nil {
		t.Fatal(e)
	}
	var stop NativeRetirementClaim
	var wg sync.WaitGroup
	type claimResult struct {
		claim NativeRetirementClaim
		err   error
	}
	results := make(chan claimResult, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := s.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireStop)
			results <- claimResult{c, e}
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for r := range results {
		if r.err == nil {
			successes++
			stop = r.claim
		} else if !errors.Is(r.err, ErrNativeOperationClaimed) || r.claim != (NativeRetirementClaim{}) {
			t.Fatal("invalid concurrent claim result", r)
		}
	}
	if successes != 1 {
		t.Fatal("first-use claim success count", successes)
	}
	if _, e = s.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireStop); !errors.Is(e, ErrNativeOperationClaimed) {
		t.Fatal("reissued stop", e)
	}
	record := func(c NativeRetirementClaim, outcome, operation string) error {
		r := nativeidentity.RetirementOperationRecord{PermitID: permit, IntentDigest: digest, ClaimID: c.ID, RequestDigest: c.RequestDigest, DomainID: intent.DomainID, PolicyDigest: intent.PolicyDigest, NativeName: intent.NativeName, ResourceID: intent.ResourceID, Dataset: intent.Dataset, DatasetGUID: intent.DatasetGUID, Step: c.Step, OperationID: operation, Outcome: outcome}
		path := "/v1/native/retirement-operations/" + c.ID
		if outcome != "accepted" {
			path += "/terminal"
		}
		f.setEvidence(path, r)
		var o nativeidentity.RetirementOperationObservation
		var e error
		if outcome == "accepted" {
			o, e = nativeidentity.ObserveRetirementOperation(ctx, f.evidenceConfig, c.ID)
		} else {
			o, e = nativeidentity.ObserveRetirementTerminal(ctx, f.evidenceConfig, c.ID)
		}
		if e != nil {
			return e
		}
		return s.RecordNativeRetirementOperation(ctx, o)
	}
	stopID := "44444444-4444-4444-8444-444444444444"
	if e = record(stop, "succeeded", stopID); e == nil {
		t.Fatal("terminal without accepted")
	}
	if e = record(stop, "accepted", intent.StartOperationID); e == nil {
		t.Fatal("creation operation reused")
	}
	if e = record(stop, "accepted", stopID); e != nil {
		t.Fatal(e)
	}
	if e = record(stop, "succeeded", stopID); e != nil {
		t.Fatal(e)
	}
	deleted, e := s.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireDelete)
	if e != nil {
		t.Fatal(e)
	}
	if e = record(deleted, "accepted", stopID); e == nil {
		t.Fatal("stop operation reused for delete")
	}
	deleteID := "55555555-5555-4555-8555-555555555555"
	if e = record(deleted, "accepted", deleteID); e != nil {
		t.Fatal(e)
	}
	if e = record(deleted, "unknown", deleteID); e != nil {
		t.Fatal(e)
	}
	if e = record(deleted, "succeeded", deleteID); e == nil {
		t.Fatal("unknown upgraded")
	}
	if _, e = s.ClaimNativeRetirement(ctx, f.owner, permit, digest, NativeRetireDelete); !errors.Is(e, ErrNativeOperationClaimed) {
		t.Fatal("unknown replayed", e)
	}
	if e = s.checkNativeRetirements(ctx); e != nil {
		t.Fatal(e)
	}
	rollback := errors.New("rollback tamper fixture")
	if e := s.transaction(ctx, func(tx *sql.Tx) error {
		old, e := nativeRetirementReceipt(tx, deleted.ID, "unknown")
		if e != nil {
			return e
		}
		old.Record.OperationID = "66666666-6666-4666-8666-666666666666"
		raw, hash, e := nativeJSON(old)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("DROP TRIGGER native_retirement_receipt_immutable"); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE native_retirement_receipts SET operation_id=?,payload=?,payload_hash=? WHERE claim_id=? AND kind='unknown'", old.Record.OperationID, raw, hash, deleted.ID); e != nil {
			return e
		}
		if _, e = nativeRetirementReceipt(tx, deleted.ID, "unknown"); e == nil {
			return errors.New("mismatched stored unknown accepted")
		}
		return rollback
	}); !errors.Is(e, rollback) {
		t.Fatal("stored unknown validation", e)
	}
	if _, e = s.LookupRelease(ctx, intent.RequestKey); e == nil {
		t.Fatal("native delete is not physical release")
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("retirement released charge")
	}
	if strings.TrimSpace(intent.Dataset) == "" || intent.DatasetGUID == "" {
		t.Fatal("owned storage identity lost")
	}
}

func TestNativeRetirementMigration24PreservesCharge(t *testing.T) {
	s := placementFixture(t)
	stockPlan(t, s, "retire-migrate")
	if _, e := s.db.Exec(nativeRetirementDowngradeSQL + "PRAGMA user_version=24"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	v, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	snapshot := sqliteSnapshot(t, v)
	if snapshot.Ready || snapshot.Allocated.Global.Runners != 1 {
		t.Fatal("migration altered charge/readiness")
	}
}
