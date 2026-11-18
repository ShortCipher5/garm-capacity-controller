package admission

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const nativeOperationDowngradeSQL = nativeBootstrapDowngradeSQL + `
DROP TRIGGER native_steps_outcome;
DROP TABLE native_operation_quotas;
DROP TABLE native_operation_receipts;
DROP TABLE native_operation_intents;
DELETE FROM audit WHERE kind IN ('native_operation_claimed','native_operation_recorded','native_quota_recorded','schema_migrated_v21');
`

func nativeStepPlan(t *testing.T, f nativeFixture, name string) (ExecutionPlan, HostBinding) {
	t.Helper()
	ctx := context.Background()
	request := placementRequest(name)
	request.Resources.Disk = 4096
	state, e := f.store.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	binding, e := f.store.LookupHostBinding(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	execution := request.Execution
	execution.Name = ""
	batch := BrokerGrantBatch{ID: nativeidentity.Fingerprint([]byte(name)), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: []BrokerGrantSpec{{Authority: stockAuthority(), Host: "a", HostBindingSequence: binding.Sequence, Resources: request.Resources, Execution: execution, ExpiresAt: f.store.now().Add(time.Hour)}}}
	if _, e := f.store.IssueBrokerGrants(ctx, batch); e != nil {
		t.Fatal(e)
	}
	p, e := f.store.AdmitStockProviderPlacement(ctx, request, stockAuthority(), stockDigest())
	if e != nil {
		t.Fatal(e)
	}
	return p, binding
}
func nativeStepReceipt(t *testing.T, f nativeFixture, c NativeOperationClaim, envelope NativeExecutionEnvelope, kind, op, resource string) nativeidentity.OperationObservation {
	t.Helper()
	r := nativeidentity.OperationRecord{PermitID: c.PermitID, EnvelopeDigest: c.EnvelopeDigest, ParentClaimID: c.ParentClaimID, Step: c.Step, StepClaimID: c.ID, DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest, NativeName: envelope.NativeName, OperationID: op, Outcome: kind, ResourceID: resource}
	path := "/v1/native/operations/" + c.ID
	terminal := kind != "accepted" && kind != "unknown"
	if terminal {
		path += "/terminal"
	}
	f.setEvidence(path, r)
	var observation nativeidentity.OperationObservation
	var e error
	if terminal {
		observation, e = nativeidentity.ObserveOperationTerminal(context.Background(), f.evidenceConfig, c.ID)
	} else {
		observation, e = nativeidentity.ObserveOperation(context.Background(), f.evidenceConfig, c.ID)
	}
	if e != nil {
		t.Fatal(e)
	}
	return observation
}
func nativeStepQuota(t *testing.T, f nativeFixture, c NativeOperationClaim, envelope NativeExecutionEnvelope, op, resource string) nativeidentity.QuotaObservation {
	t.Helper()
	r := nativeidentity.QuotaRecord{PermitID: c.PermitID, EnvelopeDigest: c.EnvelopeDigest, ParentClaimID: c.ParentClaimID, CreateStepClaimID: c.ID, DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest, NativeName: envelope.NativeName, ResourceID: resource, CreateOperationID: op, Backend: "zfs", Dataset: "owned/containers/" + envelope.NativeName, DatasetGUID: "12345", QuotaKind: "quota", RequestedBytes: envelope.Resources.Disk * (1 << 20), EnforcedBytes: envelope.Resources.Disk * (1 << 20), InstanceState: "stopped"}
	f.setEvidence("/v1/native/quotas/"+c.ID, r)
	o, e := nativeidentity.ObserveQuota(context.Background(), f.evidenceConfig, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	return o
}

const nativeCreateOp = "11111111-1111-4111-8111-111111111111"
const nativeStartOp = "22222222-2222-4222-8222-222222222222"
const nativeResource = "33333333-3333-4333-8333-333333333333"

func TestNativeSuboperationsRequireCreateQuotaStartAndNeverReturnSecondClaim(t *testing.T) {
	f, protectedPolicy, protectedData := protectedNativeFixture(t, "step-sequence")
	s := f.store
	ctx := context.Background()
	p, b := nativeStepPlan(t, f, "step-sequence")
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		envelope, digest := protectedEnvelope(t, f, d, protectedPolicy, protectedData)
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := s.ClaimNativeStart(ctx, f.owner, d.ID(), digest, parent.ClaimID); e == nil {
			t.Fatal("started without create")
		}
		create, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		if create.RequestDigest != envelope.BodyDigest {
			t.Fatal("create digest mismatch")
		}
		if _, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID); !errors.Is(e, ErrNativeOperationClaimed) {
			t.Fatal("replayed create", e)
		}
		if e := s.RecordNativeOperation(ctx, nativeidentity.OperationObservation{}); e == nil {
			t.Fatal("caller receipt accepted")
		}
		success := nativeStepReceipt(t, f, create, envelope, "succeeded", nativeCreateOp, nativeResource)
		if e := s.RecordNativeOperation(ctx, success); e == nil {
			t.Fatal("terminal without accepted op")
		}
		accepted := nativeStepReceipt(t, f, create, envelope, "accepted", nativeCreateOp, "")
		if e := s.RecordNativeOperation(ctx, accepted); e != nil {
			t.Fatal(e)
		}
		success = nativeStepReceipt(t, f, create, envelope, "succeeded", nativeCreateOp, nativeResource)
		if e := s.RecordNativeOperation(ctx, success); e != nil {
			t.Fatal(e)
		}
		if _, e := s.ClaimNativeStart(ctx, f.owner, d.ID(), digest, parent.ClaimID); e == nil {
			t.Fatal("started without measured quota")
		}
		if e := s.RecordNativeQuota(ctx, nativeidentity.QuotaObservation{}); e == nil {
			t.Fatal("caller quota accepted")
		}
		if e := s.RecordNativeQuota(ctx, nativeStepQuota(t, f, create, envelope, nativeCreateOp, nativeResource)); e != nil {
			t.Fatal(e)
		}
		if e := s.RecordDispatchOutcome(ctx, d, DispatchReceipt{State: "confirmed", OperationID: nativeCreateOp, ResourceID: nativeResource}); e == nil {
			t.Fatal("stopped create was mistaken for started runner")
		}
		bootstrapProof(t, f, envelope, protectedPolicy, protectedData)
		start, e := s.ClaimNativeStart(ctx, f.owner, d.ID(), digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		if start.RequestDigest != nativeidentity.NativeStartDigest() {
			t.Fatal("start body not fixed")
		}
		if _, e := s.ClaimNativeStart(ctx, f.owner, d.ID(), digest, parent.ClaimID); !errors.Is(e, ErrNativeOperationClaimed) {
			t.Fatal("replayed start", e)
		}
		if e := s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, start, envelope, "accepted", nativeStartOp, "")); e != nil {
			t.Fatal(e)
		}
		if e := s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, start, envelope, "succeeded", nativeStartOp, nativeResource)); e != nil {
			t.Fatal(e)
		}
		if e := s.RecordDispatchOutcome(ctx, d, DispatchReceipt{State: "confirmed", OperationID: nativeCreateOp, ResourceID: nativeResource}); e == nil {
			t.Fatal("overall outcome changed native operation ID")
		}
		if e := s.RecordDispatchOutcome(ctx, d, DispatchReceipt{State: "confirmed", OperationID: nativeStartOp, ResourceID: nativeCreateOp}); e == nil {
			t.Fatal("overall outcome changed native incarnation")
		}
		return DispatchReceipt{State: "confirmed", OperationID: nativeStartOp, ResourceID: nativeResource}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e := s.checkNativeOperations(ctx); e != nil {
		t.Fatal(e)
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("creation released charge")
	}
	backup := filepath.Join(sqliteBackupDir(t), "native-steps.db")
	if e := s.BackupSQLite(ctx, backup); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(sqliteBackupDir(t), "native-steps-restored.db")
	if e := RestoreSQLite(ctx, backup, restored); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restored, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	if e := copy.checkNativeOperations(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestNativeSuboperationUnknownIsTerminalAndRetainsCharge(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := nativeStepPlan(t, f, "step-unknown")
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		envelope, digest := nativeEnvelope(t, f, d)
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e != nil {
			t.Fatal(e)
		}
		create, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		if e := s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, envelope, "unknown", "", "")); e != nil {
			t.Fatal(e)
		}
		if e := s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, envelope, "accepted", nativeCreateOp, "")); e == nil {
			t.Fatal("unknown regained accepted state")
		}
		if _, e := s.ClaimNativeStart(ctx, f.owner, d.ID(), digest, parent.ClaimID); e == nil {
			t.Fatal("unknown advanced to start")
		}
		if _, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID); !errors.Is(e, ErrNativeOperationClaimed) {
			t.Fatal(e)
		}
		return DispatchReceipt{}, errors.New("uncertain native create")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	if e := s.checkNativeOperations(ctx); e != nil {
		t.Fatal(e)
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("unknown lost charge")
	}
}
func TestNativeSuboperationRefusesWrongParentAndOwner(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := nativeStepPlan(t, f, "step-authority")
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		_, digest := nativeEnvelope(t, f, d)
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := s.ClaimNativeCreate(ctx, f.controller, d.ID(), digest, parent.ClaimID); e == nil {
			t.Fatal("controller role accepted as owner")
		}
		if _, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, strings.Repeat("0", 64)); e == nil {
			t.Fatal("wrong parent claim accepted")
		}
		return DispatchReceipt{}, errors.New("abort")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
}

func TestNativeSuboperationConcurrentClaimAndLostAcknowledgement(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := nativeStepPlan(t, f, "step-race")
	other, e := OpenSQLite(s.path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var permit, digest, parentID string
	_, e = s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		permit = d.ID()
		_, digest = nativeEnvelope(t, f, d)
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e != nil {
			t.Fatal(e)
		}
		parentID = parent.ClaimID
		var fresh atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				store := s
				if i%2 == 1 {
					store = other
				}
				claim, e := store.ClaimNativeCreate(ctx, f.owner, permit, digest, parentID)
				if e == nil {
					if claim.ID == "" {
						t.Error("empty fresh receipt")
					}
					fresh.Add(1)
				} else if !errors.Is(e, ErrNativeOperationClaimed) {
					t.Error(e)
				}
			}(i)
		}
		wg.Wait()
		if fresh.Load() != 1 {
			t.Fatal("fresh step count", fresh.Load())
		}
		return DispatchReceipt{}, errors.New("lost create-step claim acknowledgement")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	reopened, e := OpenSQLite(s.path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	claim, e := reopened.ClaimNativeCreate(ctx, f.owner, permit, digest, parentID)
	if e == nil || claim.ID != "" {
		t.Fatal("restart renewed create permission", e)
	}
	if sqliteSnapshot(t, reopened).Allocated.Global.Runners != 1 {
		t.Fatal("lost charge")
	}
	if e := reopened.checkNativeOperations(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestNativeSuboperationMigrationFrom20PreservesCharge(t *testing.T) {
	s := placementFixture(t)
	stockPlan(t, s, "migration20")
	if _, e := s.db.Exec(nativeOperationDowngradeSQL + "PRAGMA user_version=20"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	migrated, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer migrated.Close()
	snap := sqliteSnapshot(t, migrated)
	if snap.Ready || snap.Allocated.Global.Runners != 1 {
		t.Fatal("migration altered charge/readiness")
	}
	for _, table := range []string{"native_operation_intents", "native_operation_receipts", "native_operation_quotas"} {
		var n int
		if e := migrated.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatal("migration invented physical authority", table, n, e)
		}
	}
}
func TestNativeSuboperationMissingIntentHistoryFailsStartupAndBackup(t *testing.T) {
	f := nativeExecutionFixture(t)
	s := f.store
	ctx := context.Background()
	p, b := nativeStepPlan(t, f, "step-history")
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		_, digest := nativeEnvelope(t, f, d)
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID); e != nil {
			t.Fatal(e)
		}
		if _, e := s.db.Exec("DROP TRIGGER native_operation_intent_no_delete; DELETE FROM native_operation_intents"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID); !errors.Is(e, ErrNativeOperationClaimed) {
			t.Fatal("missing intent repeated", e)
		}
		return DispatchReceipt{}, errors.New("unknown")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	if e := s.checkNativeOperations(ctx); e == nil {
		t.Fatal("history omission accepted")
	}
	if copy, e := OpenSQLite(s.path, Config{}); e == nil {
		copy.Close()
		t.Fatal("startup accepted missing intent")
	}
	if e := s.BackupSQLite(ctx, filepath.Join(sqliteBackupDir(t), "missing-step.db")); e == nil {
		t.Fatal("backup accepted missing intent")
	}
}

func TestNativeSuboperationConsumedParentCannotUseLegacyOutcomeBeforeFirstStep(t *testing.T) {
	for _, state := range []string{"submitted", "confirmed"} {
		t.Run(state, func(t *testing.T) {
			f := nativeExecutionFixture(t)
			s := f.store
			ctx := context.Background()
			p, b := nativeStepPlan(t, f, "no-steps-"+state)
			var permit DispatchPermit
			receipt := DispatchReceipt{State: state, OperationID: nativeStartOp, ResourceID: nativeResource}
			_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
				permit = d
				_, digest := nativeEnvelope(t, f, d)
				if _, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest); e != nil {
					t.Fatal(e)
				}
				var n int
				if e := s.db.QueryRow("SELECT count(*) FROM native_operation_intents WHERE permit_id=?", d.ID()).Scan(&n); e != nil || n != 0 {
					t.Fatal(n, e)
				}
				if e := s.RecordDispatchOutcome(ctx, d, receipt); e == nil {
					t.Fatal("legacy outcome bypassed mandatory native steps")
				}
				if _, e := s.db.Exec("UPDATE dispatches SET state=?,operation_id=?,resource_id=? WHERE permit_id=?", state, nativeStartOp, nativeResource, d.ID()); e == nil {
					t.Fatal("raw legacy update bypassed native steps")
				}
				record, e := s.LookupDispatch(ctx, p.Request.Key)
				if e != nil || record.State != "intent" {
					t.Fatal("failed legacy update changed state", record, e)
				}
				return receipt, nil
			})
			if !errors.Is(e, ErrDispatchUnknown) {
				t.Fatal("canonical callback promoted unqualified outcome", e)
			}
			if e := s.RecordDispatchOutcome(ctx, permit, receipt); e == nil {
				t.Fatal("late legacy outcome bypassed mandatory steps")
			}
			record, e := s.LookupDispatch(ctx, p.Request.Key)
			if e != nil || record.State == "submitted" || record.State == "confirmed" {
				t.Fatal("parent was promoted", record, e)
			}
			if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
				t.Fatal("charge released")
			}
		})
	}
}
