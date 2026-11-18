package admission

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const nativeBootstrapDowngradeSQL = nativeJobDowngradeSQL + `
DROP TRIGGER native_start_requires_bootstrap;
DROP TABLE native_bootstrap_receipts;
DROP TABLE native_bootstrap_intents;
DROP TABLE native_bootstrap_bindings;
DELETE FROM audit WHERE kind IN ('native_bootstrap_bound','native_bootstrap_claimed','native_bootstrap_recorded','schema_migrated_v22');
`

func protectedNativeFixture(t *testing.T, name string) (nativeFixture, nativeapi.Policy, string) {
	t.Helper()
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("4", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	settings, _ := json.Marshal(map[string]any{"agentName": name, "workFolder": "_work", "ephemeral": true, "disableUpdate": true})
	jit, _ := json.Marshal(map[string]string{".runner": base64.StdEncoding.EncodeToString(settings), ".credentials": base64.StdEncoding.EncodeToString([]byte(`{"scheme":"OAuth"}`)), ".credentials_rsaparams": base64.StdEncoding.EncodeToString([]byte(`{"fixture":true}`))})
	payload, _ := json.Marshal(protectedboot.Payload{Version: protectedboot.Version, RunnerName: name, Image: image, JIT: base64.StdEncoding.EncodeToString(jit)})
	policy, e := nativeapi.NewPolicy(nativeapi.PolicySpec{Version: nativeapi.ProtectedPolicyVersion, Controller: "controller", Host: "a", DomainID: strings.Repeat("a", 64), NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: nativeapi.NativeVersion, Pools: map[string]nativeapi.Pool{"app": {Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("a", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge", ZFSDataset: "owned", ProtectedImage: &image}}})
	if e != nil {
		t.Fatal(e)
	}
	return nativeExecutionFixture(t, policy.Digest()), policy, string(payload)
}
func protectedEnvelope(t *testing.T, f nativeFixture, d DispatchPermit, policy nativeapi.Policy, data string) (NativeExecutionEnvelope, string) {
	t.Helper()
	p, e := f.store.LookupExecutionPlan(context.Background(), d.key)
	if e != nil {
		t.Fatal(e)
	}
	r := p.Request.Resources
	v := p.Execution
	compiled, e := policy.Compile(nativeapi.Allocation{Controller: v.Controller, Host: p.Request.Host, Pool: p.Request.Pool, Name: v.Name, RequestKey: p.Request.Key, CPU: r.CPU, RAM: r.RAM, Disk: r.Disk, Runners: r.Runners, Flavor: v.Flavor, Image: v.Image, Fingerprint: v.Fingerprint, Type: v.Type, Arch: v.Arch, Profiles: v.Profiles}, data)
	if e != nil {
		t.Fatal(e)
	}
	envelope, e := f.store.BindNativeExecution(context.Background(), d, f.binding.Sequence, compiled.Digest())
	if e != nil {
		t.Fatal(e)
	}
	digest, e := NativeEnvelopeDigest(envelope)
	if e != nil {
		t.Fatal(e)
	}
	return envelope, digest
}
func bootstrapObservation(t *testing.T, f nativeFixture, c NativeBootstrapClaim, outcome string) nativeidentity.BootstrapObservation {
	t.Helper()
	p := c.Plan
	r := nativeidentity.BootstrapRecord{ClaimID: c.ID, PlanDigest: c.PlanDigest, PermitID: p.PermitID, EnvelopeDigest: p.EnvelopeDigest, ParentClaimID: p.ParentClaimID, DomainID: p.DomainID, PolicyDigest: p.PolicyDigest, NativeName: p.NativeName, ResourceID: p.ResourceID, ImageFingerprint: p.ImageFingerprint, Outcome: outcome, LaunchSHA256: p.LaunchSHA256, LaunchBytes: p.LaunchBytes, JITSHA256: p.JITSHA256, JITBytes: p.JITBytes, LaunchMode: "0600", LaunchType: "file", JITMode: "0600", JITType: "file"}
	f.setEvidence("/v1/native/bootstraps/"+c.ID, r)
	o, e := nativeidentity.ObserveBootstrap(context.Background(), f.evidenceConfig, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func bootstrapProof(t *testing.T, f nativeFixture, p NativeExecutionEnvelope, policy nativeapi.Policy, data string) NativeBootstrapClaim {
	t.Helper()
	ctx := context.Background()
	plan, e := f.store.BindNativeBootstrap(ctx, p.PermitID, data, policy)
	if e != nil {
		t.Fatal(e)
	}
	c, e := f.store.ClaimNativeBootstrap(ctx, f.owner, p.PermitID, plan.EnvelopeDigest, plan.ParentClaimID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.RecordNativeBootstrap(ctx, bootstrapObservation(t, f, c, "verified")); e != nil {
		t.Fatal(e)
	}
	return c
}
func TestNativeBootstrapRequiresCanonicalBodyAndUnknownNeverStarts(t *testing.T) {
	f, policy, data := protectedNativeFixture(t, "boot-unknown")
	s := f.store
	ctx := context.Background()
	p, b := nativeStepPlan(t, f, "boot-unknown")
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		envelope, digest := protectedEnvelope(t, f, d, policy, data)
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e != nil {
			t.Fatal(e)
		}
		create, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.BindNativeBootstrap(ctx, d.ID(), data, policy); e == nil {
			t.Fatal("bound before create success")
		}
		if e = s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, envelope, "accepted", nativeCreateOp, "")); e != nil {
			t.Fatal(e)
		}
		if e = s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, envelope, "succeeded", nativeCreateOp, nativeResource)); e != nil {
			t.Fatal(e)
		}
		if e = s.RecordNativeQuota(ctx, nativeStepQuota(t, f, create, envelope, nativeCreateOp, nativeResource)); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ClaimNativeStart(ctx, f.owner, d.ID(), digest, parent.ClaimID); e == nil {
			t.Fatal("start without bootstrap")
		}
		if _, e = s.BindNativeBootstrap(ctx, d.ID(), data+" ", policy); e == nil {
			t.Fatal("changed body accepted")
		}
		plan, e := s.BindNativeBootstrap(ctx, d.ID(), data, policy)
		if e != nil {
			t.Fatal(e)
		}
		again, e := s.BindNativeBootstrap(ctx, d.ID(), data, policy)
		if e != nil || again != plan {
			t.Fatal("binding retry", e)
		}
		claim, e := s.ClaimNativeBootstrap(ctx, f.owner, d.ID(), digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.ClaimNativeBootstrap(ctx, f.owner, d.ID(), digest, parent.ClaimID); !errors.Is(e, ErrNativeBootstrapClaimed) {
			t.Fatal("claim replay", e)
		}
		if e = s.RecordNativeBootstrap(ctx, bootstrapObservation(t, f, claim, "unknown")); e != nil {
			t.Fatal(e)
		}
		if e = s.RecordNativeBootstrap(ctx, bootstrapObservation(t, f, claim, "verified")); e == nil {
			t.Fatal("upgraded unknown")
		}
		if _, e = s.ClaimNativeStart(ctx, f.owner, d.ID(), digest, parent.ClaimID); e == nil {
			t.Fatal("unknown bootstrap started")
		}
		return DispatchReceipt{}, errors.New("unknown bootstrap")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	if e = s.checkNativeBootstraps(ctx); e != nil {
		t.Fatal(e)
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("released unknown")
	}
}

func TestNativeBootstrapConcurrentLostACKAndRestoreBeforeClaim(t *testing.T) {
	f, policy, data := protectedNativeFixture(t, "boot-replay")
	s := f.store
	ctx := context.Background()
	p, b := nativeStepPlan(t, f, "boot-replay")
	backup := filepath.Join(sqliteBackupDir(t), "bound.db")
	var plan NativeBootstrapPlan
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		envelope, digest := protectedEnvelope(t, f, d, policy, data)
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e != nil {
			t.Fatal(e)
		}
		create, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, envelope, "accepted", nativeCreateOp, "")); e != nil {
			t.Fatal(e)
		}
		if e = s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, envelope, "succeeded", nativeCreateOp, nativeResource)); e != nil {
			t.Fatal(e)
		}
		plan, e = s.BindNativeBootstrap(ctx, d.ID(), data, policy)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.BackupSQLite(ctx, backup); e != nil {
			t.Fatal(e)
		}
		var wg sync.WaitGroup
		var fresh atomic.Int64
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, e := s.ClaimNativeBootstrap(ctx, f.owner, d.ID(), digest, parent.ClaimID)
				if e == nil {
					if c.ID == "" {
						t.Error("empty claim")
					}
					fresh.Add(1)
				} else if !errors.Is(e, ErrNativeBootstrapClaimed) {
					t.Error(e)
				}
			}()
		}
		wg.Wait()
		if fresh.Load() != 1 {
			t.Fatal("duplicate dispatch permission", fresh.Load())
		}
		return DispatchReceipt{}, errors.New("lost bootstrap ACK")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	reopened, e := OpenSQLite(s.path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if c, e := reopened.ClaimNativeBootstrap(ctx, f.owner, plan.PermitID, plan.EnvelopeDigest, plan.ParentClaimID); e == nil || c.ID != "" {
		t.Fatal("restart replay")
	}
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	if e = RestoreSQLite(ctx, backup, restored); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restored, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	fence, e := copy.BeginReconciliation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = copy.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(sqliteSnapshot(t, copy).Config, copy.now())}); e != nil {
		t.Fatal(e)
	}
	if c, e := copy.ClaimNativeBootstrap(ctx, f.owner, plan.PermitID, plan.EnvelopeDigest, plan.ParentClaimID); !errors.Is(e, ErrFence) || c.ID != "" {
		t.Fatal("restored preclaim backup renewed dispatch", e)
	}
	if sqliteSnapshot(t, copy).Allocated.Global.Runners != 1 {
		t.Fatal("restored charge lost")
	}
}
func TestNativeBootstrapMigrationFrom21ClosesReadiness(t *testing.T) {
	s := placementFixture(t)
	stockPlan(t, s, "migrate22")
	if _, e := s.db.Exec(nativeBootstrapDowngradeSQL + "PRAGMA user_version=21"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	copy, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	snap := sqliteSnapshot(t, copy)
	if snap.Ready || snap.Allocated.Global.Runners != 1 {
		t.Fatal("migration changed authority/charge")
	}
	var count int
	if e = copy.db.QueryRow("SELECT count(*) FROM native_bootstrap_bindings").Scan(&count); e != nil || count != 0 {
		t.Fatal("invented bootstrap", e)
	}
}
func TestNativeBootstrapAuditRollbackAndMissingHistory(t *testing.T) {
	f, policy, data := protectedNativeFixture(t, "boot-audit")
	s := f.store
	ctx := context.Background()
	p, b := nativeStepPlan(t, f, "boot-audit")
	_, e := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		envelope, digest := protectedEnvelope(t, f, d, policy, data)
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, d.ID(), digest)
		if e != nil {
			t.Fatal(e)
		}
		create, e := s.ClaimNativeCreate(ctx, f.owner, d.ID(), digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, envelope, "accepted", nativeCreateOp, "")); e != nil {
			t.Fatal(e)
		}
		if e = s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, envelope, "succeeded", nativeCreateOp, nativeResource)); e != nil {
			t.Fatal(e)
		}
		if _, e = s.BindNativeBootstrap(ctx, d.ID(), data, policy); e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.Exec("CREATE TRIGGER refuse_bootstrap BEFORE INSERT ON audit WHEN NEW.kind='native_bootstrap_claimed' BEGIN SELECT RAISE(ABORT,'fixture'); END"); e != nil {
			t.Fatal(e)
		}
		if c, e := s.ClaimNativeBootstrap(ctx, f.owner, d.ID(), digest, parent.ClaimID); e == nil || c.ID != "" {
			t.Fatal("returned claim without audit")
		}
		var count int
		if e = s.db.QueryRow("SELECT count(*) FROM native_bootstrap_intents").Scan(&count); e != nil || count != 0 {
			t.Fatal("partial claim")
		}
		if _, e = s.db.Exec("DROP TRIGGER refuse_bootstrap"); e != nil {
			t.Fatal(e)
		}
		c, e := s.ClaimNativeBootstrap(ctx, f.owner, d.ID(), digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		o := bootstrapObservation(t, f, c, "verified")
		if e = s.RecordNativeBootstrap(ctx, o); e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.Exec("DROP TRIGGER native_bootstrap_receipt_permanent; DELETE FROM native_bootstrap_receipts"); e != nil {
			t.Fatal(e)
		}
		if e = s.RecordNativeBootstrap(ctx, o); e == nil {
			t.Fatal("recreated missing receipt")
		}
		if e = s.checkNativeBootstraps(ctx); e == nil {
			t.Fatal("missing receipt ignored")
		}
		return DispatchReceipt{}, errors.New("corrupted fixture, no execution")
	})
	if !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
}

func TestNativeBootstrapMissingMigrationMarkerFailsClosed(t *testing.T) {
	s := placementFixture(t)
	if _, err := s.db.Exec("DELETE FROM audit WHERE kind='schema_migrated_v22'"); err != nil {
		t.Fatal(err)
	}
	if err := s.checkNativeBootstraps(context.Background()); !errors.Is(err, ErrNativeBootstrap) {
		t.Fatalf("missing migration marker accepted: %v", err)
	}
}
