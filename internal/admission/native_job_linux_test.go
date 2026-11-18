package admission

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
	common "github.com/cloudbase/garm-provider-common/params"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const nativeDomainOverheadDowngradeSQL = `
DROP TRIGGER reservation_current_accounting;
CREATE TRIGGER reservation_current_accounting BEFORE INSERT ON reservations WHEN NEW.accounting_version!=18 BEGIN SELECT RAISE(ABORT,'current shared grant accounting required'); END;
DROP TRIGGER dispatch_current_accounting;
CREATE TRIGGER dispatch_current_accounting BEFORE INSERT ON dispatches WHEN NEW.grant_accounting_version!=18 BEGIN SELECT RAISE(ABORT,'current shared grant accounting required'); END;
DROP TABLE native_domain_overheads;DELETE FROM audit WHERE kind='schema_migrated_v26';`
const nativeRetirementDowngradeSQL = nativeDomainOverheadDowngradeSQL + `DROP TRIGGER native_operation_not_retirement;DROP TABLE native_retirement_receipts;DROP TABLE native_retirement_claims;DROP TABLE native_retirement_intents;DELETE FROM audit WHERE kind='schema_migrated_v25';`
const nativeFenceDowngradeSQL = nativeRetirementDowngradeSQL + `DROP TABLE native_root_fences; DELETE FROM audit WHERE kind='schema_migrated_v24';`
const nativeJobDowngradeSQL = nativeFenceDowngradeSQL + `
DROP TABLE native_job_observations;
DROP TABLE native_runner_bindings;
DELETE FROM audit WHERE kind IN ('native_runner_bound','native_job_in_progress','native_job_completed','schema_migrated_v23');
`

func TestNativeJobAuthenticBindingTerminalReplayAndChargeRetention(t *testing.T) {
	ctx := context.Background()
	name := "job-runner"
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("3", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	f, profileRequest, _, policies := poolProfileFixture(t, image)
	s := f.store
	policy := policies[f.binding.Sequence]
	var endpoint string
	started := time.Now()
	completed := time.Now()
	status := "in_progress"
	wrongRunner := false
	sourceRepo := profileRepo
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/pools/" + profilePool:
			json.NewEncoder(w).Encode(map[string]any{"id": profilePool, "repo_id": sourceRepo, "provider_name": "stock-provider", "flavor": "small", "image": "prebuilt", "os_type": "linux", "os_arch": "amd64"})
		case "/api/v1/instances/" + name:
			json.NewEncoder(w).Encode(map[string]any{"id": "33333333-3333-4333-8333-333333333333", "name": name, "pool_id": profilePool, "provider_name": "stock-provider", "agent_id": 17, "status": "creating", "runner_status": "pending"})
		case "/api/v1/metadata/credentials/runner":
			json.NewEncoder(w).Encode(map[string]any{"agentId": 17, "agentName": name, "gitHubUrl": "https://github.example/owner/repo", "ephemeral": true, "disableUpdate": true, "workFolder": "_work"})
		case "/api/v1/metadata/credentials/credentials":
			fmt.Fprint(w, `{"scheme":"OAuth","data":"fixture"}`)
		case "/api/v1/metadata/credentials/credentials_rsaparams":
			fmt.Fprint(w, `{"D":"fixture"}`)
		case "/repos/owner/repo/actions/jobs/91":
			var conclusion, end any
			if status == "completed" {
				conclusion = "success"
				end = completed
			}
			runner := 17
			if wrongRunner {
				runner = 18
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 91, "run_id": 12, "run_attempt": 1, "runner_id": runner, "runner_name": name, "url": endpoint + r.URL.Path, "run_url": endpoint + "/repos/owner/repo/actions/runs/12", "status": status, "conclusion": conclusion, "started_at": started, "completed_at": end})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	endpoint = server.URL
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	trust := &tls.Config{RootCAs: roots}
	metadata, e := stockjit.New(stockjit.Policy{MetadataURL: endpoint + "/api/v1/metadata", ControllerID: "controller", PoolID: profilePool, GitHubURL: "https://github.example/owner/repo"}, trust)
	if e != nil {
		t.Fatal(e)
	}
	defer metadata.Close()
	source, e := metadata.FetchProtected(ctx, "controller", common.BootstrapInstance{Name: name, PoolID: profilePool, MetadataURL: endpoint + "/api/v1/metadata", RepoURL: "https://github.example/owner/repo", InstanceToken: "fixture", JitConfigEnabled: true}, image, stockidentity.Configuration{Endpoint: endpoint, Token: "fixture", Provider: "stock-provider", Pool: profilePool, TLS: trust})
	if e != nil {
		t.Fatal(e)
	}
	otherServer := httptest.NewTLSServer(server.Config.Handler)
	defer otherServer.Close()
	otherRoots := x509.NewCertPool()
	otherRoots.AddCert(otherServer.Certificate())
	otherTrust := &tls.Config{RootCAs: otherRoots}
	otherMetadata, e := stockjit.New(stockjit.Policy{MetadataURL: otherServer.URL + "/api/v1/metadata", ControllerID: "controller", PoolID: profilePool, GitHubURL: "https://github.example/owner/repo"}, otherTrust)
	if e != nil {
		t.Fatal(e)
	}
	defer otherMetadata.Close()
	wrongSource, e := otherMetadata.FetchProtected(ctx, "controller", common.BootstrapInstance{Name: name, PoolID: profilePool, MetadataURL: otherServer.URL + "/api/v1/metadata", RepoURL: "https://github.example/owner/repo", InstanceToken: "fixture", JitConfigEnabled: true}, image, stockidentity.Configuration{Endpoint: otherServer.URL, Token: "fixture", Provider: "stock-provider", Pool: profilePool, TLS: otherTrust})
	if e != nil {
		t.Fatal(e)
	}
	sourceRepo = "44444444-4444-4444-8444-444444444444"
	wrongRepository, e := metadata.FetchProtected(ctx, "controller", common.BootstrapInstance{Name: name, PoolID: profilePool, MetadataURL: endpoint + "/api/v1/metadata", RepoURL: "https://github.example/owner/repo", InstanceToken: "fixture", JitConfigEnabled: true}, image, stockidentity.Configuration{Endpoint: endpoint, Token: "fixture", Provider: "stock-provider", Pool: profilePool, TLS: trust})
	if e != nil {
		t.Fatal(e)
	}
	sourceRepo = profileRepo
	repo := demandrepair.Repository{GarmID: profileRepo, Host: "github.example", Owner: "owner", Name: "repo"}
	github, e := demandrepair.NewGitHub(endpoint, "github.example", "fixture", func(context.Context, string) (string, error) { return "fixture", nil }, trust, []demandrepair.Repository{repo})
	if e != nil {
		t.Fatal(e)
	}
	defer github.Close()
	request := placementRequest(name)
	request.Pool = profilePool
	request.Key = executionKey(request.Execution, profilePool)
	request.Resources.Disk = 4096
	authority := stockAuthority()
	authority.PoolID = profilePool
	observed, e := stockidentity.Observe(ctx, stockidentity.Configuration{Endpoint: endpoint, Token: "fixture", Provider: "stock-provider", Pool: profilePool, TLS: trust})
	if e != nil {
		t.Fatal(e)
	}
	profileRequest.Authority = authority
	profileRequest.SourceIdentity = observed.SourceIdentity()
	if _, e = s.RegisterBrokerPoolProfile(ctx, profileRequest, observed, policies); e != nil {
		t.Fatal(e)
	}
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	host, e := s.LookupHostBinding(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	execution := request.Execution
	execution.Name = ""
	if _, e = s.IssueBrokerGrants(ctx, BrokerGrantBatch{ID: strings.Repeat("1", 64), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: []BrokerGrantSpec{{Authority: authority, Host: "a", HostBindingSequence: host.Sequence, Resources: request.Resources, Execution: execution, ExpiresAt: s.now().Add(time.Hour)}}}); e != nil {
		t.Fatal(e)
	}
	plan, e := s.AdmitStockProviderPlacement(ctx, request, authority, stockDigest())
	if e != nil {
		t.Fatal(e)
	}
	var permit string
	_, e = s.ExecuteStockProviderDispatch(ctx, plan, host, authority, stockDigest(), func(ctx context.Context, d DispatchPermit) (DispatchReceipt, error) {
		permit = d.ID()
		env, digest := protectedEnvelope(t, f, d, policy, source.Payload())
		parent, e := s.ConsumeNativeExecution(ctx, f.owner, permit, digest)
		if e != nil {
			t.Fatal(e)
		}
		create, e := s.ClaimNativeCreate(ctx, f.owner, permit, digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		for _, outcome := range []string{"accepted", "succeeded"} {
			resource := ""
			if outcome == "succeeded" {
				resource = nativeResource
			}
			if e = s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, create, env, outcome, nativeCreateOp, resource)); e != nil {
				t.Fatal(e)
			}
		}
		if e = s.RecordNativeQuota(ctx, nativeStepQuota(t, f, create, env, nativeCreateOp, nativeResource)); e != nil {
			t.Fatal(e)
		}
		bootstrapProof(t, f, env, policy, source.Payload())
		if _, e = s.BindNativeRunner(ctx, permit, stockjit.ProtectedBootstrap{}, policy); e == nil {
			t.Fatal("fabricated producer accepted")
		}
		for _, bad := range []stockjit.ProtectedBootstrap{wrongSource, wrongRepository} {
			if bad.Payload() != source.Payload() {
				t.Fatal("fixture must isolate source authority")
			}
			if _, e = s.BindNativeRunner(ctx, permit, bad, policy); e == nil {
				t.Fatal("different source/repository accepted")
			}
		}
		if _, e = s.db.Exec("CREATE TRIGGER refuse_runner_audit BEFORE INSERT ON audit WHEN NEW.kind='native_runner_bound' BEGIN SELECT RAISE(ABORT,'fixture'); END"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.BindNativeRunner(ctx, permit, source, policy); e == nil {
			t.Fatal("binding without audit")
		}
		var rows int
		if e = s.db.QueryRow("SELECT count(*) FROM native_runner_bindings").Scan(&rows); e != nil || rows != 0 {
			t.Fatal("partial binding")
		}
		if _, e = s.db.Exec("DROP TRIGGER refuse_runner_audit"); e != nil {
			t.Fatal(e)
		}
		binding, e := s.BindNativeRunner(ctx, permit, source, policy)
		if e != nil || binding.Runner.RunnerID != 17 || binding.ResourceID != nativeResource {
			t.Fatal("binding", binding, e)
		}
		if again, e := s.BindNativeRunner(ctx, permit, source, policy); e != nil || !reflect.DeepEqual(binding, again) {
			t.Fatal("binding replay", e)
		}
		start, e := s.ClaimNativeStart(ctx, f.owner, permit, digest, parent.ClaimID)
		if e != nil {
			t.Fatal(e)
		}
		for _, outcome := range []string{"accepted", "succeeded"} {
			if e = s.RecordNativeOperation(ctx, nativeStepReceipt(t, f, start, env, outcome, nativeStartOp, nativeResource)); e != nil {
				t.Fatal(e)
			}
		}
		started = start.ClaimedAt
		completed = time.Now().UTC()
		return DispatchReceipt{State: "confirmed", OperationID: nativeStartOp, ResourceID: nativeResource}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	o, e := github.ObserveExactJob(ctx, repo, 91)
	if e != nil {
		t.Fatal(e)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.RecordNativeJob(ctx, permit, o) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent progress", err)
		}
	}
	var recorded int
	if e = s.db.QueryRow("SELECT count(*) FROM native_job_observations").Scan(&recorded); e != nil || recorded != 1 {
		t.Fatal("duplicate concurrent history")
	}

	backup := filepath.Join(sqliteBackupDir(t), "before-terminal.db")
	if e = s.BackupSQLite(ctx, backup); e != nil {
		t.Fatal(e)
	}
	wrongRunner = true
	o, e = github.ObserveExactJob(ctx, repo, 91)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RecordNativeJob(ctx, permit, o); e == nil {
		t.Fatal("different runner admitted")
	}
	wrongRunner = false
	status = "completed"
	o, e = github.ObserveExactJob(ctx, repo, 91)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RecordNativeJob(ctx, permit, o); e != nil {
		t.Fatal("terminal", e)
	}
	if e = s.RecordNativeJob(ctx, permit, o); e != nil {
		t.Fatal("exact terminal retry", e)
	}
	restored := filepath.Join(sqliteBackupDir(t), "restored-job.db")
	if e = RestoreSQLite(ctx, backup, restored); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restored, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	if e = copy.RecordNativeJob(ctx, permit, o); !errors.Is(e, ErrFence) {
		t.Fatal("unreconciled restore wrote lifecycle", e)
	}
	fence, e := copy.BeginReconciliation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = copy.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(sqliteSnapshot(t, copy).Config, copy.now())}); e != nil {
		t.Fatal(e)
	}
	if e = copy.RecordNativeJob(ctx, permit, o); !errors.Is(e, ErrFence) {
		t.Fatal("restored old lineage wrote lifecycle", e)
	}
	bound, e := s.LookupNativeRunnerBinding(ctx, permit)
	if e != nil {
		t.Fatal(e)
	}
	journal := nativeidentity.ProtectedJournalRecord{PermitID: permit, EnvelopeDigest: bound.EnvelopeDigest, DomainID: bound.DomainID, PolicyDigest: bound.PolicyDigest, NativeName: name, ResourceID: nativeResource, ImageFingerprint: strings.Repeat("a", 64), InitSHA256: image.InitSHA256, State: "process_tree_exited_unqualified", RecordAt: time.Now().UTC(), ExitCode: 0}
	f.setEvidence("/v1/native/executions/"+permit+"/journal", journal)
	jo, e := nativeidentity.ObserveProtectedJournal(ctx, f.evidenceConfig, permit)
	if e != nil {
		t.Fatal(e)
	}
	correlation, e := s.CorrelateProtectedJournal(ctx, jo, policy)
	if e != nil || !correlation.RootJournalAndTerminalJobObserved || correlation.ReleaseAuthorized || correlation.AcceptanceFenceQualified || correlation.PhysicalDeletionProven {
		t.Fatal("unqualified correlation", correlation, e)
	}
	if e = s.RecordNativeRootFence(ctx, jo, policy); e == nil {
		t.Fatal("unclosed network became fence")
	}
	journal.GuestNetworkClosed = true
	f.setEvidence("/v1/native/executions/"+permit+"/journal", journal)
	jo, e = nativeidentity.ObserveProtectedJournal(ctx, f.evidenceConfig, permit)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec("CREATE TRIGGER fence_audit_failure BEFORE INSERT ON audit WHEN NEW.kind='native_root_fence_observed' BEGIN SELECT RAISE(ABORT,'fault'); END;"); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordNativeRootFence(ctx, jo, policy); e == nil {
		t.Fatal("audit failure accepted")
	}
	if _, e = s.LookupNativeRootFence(ctx, permit); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("partial fence", e)
	}
	if _, e = s.db.Exec("DROP TRIGGER fence_audit_failure"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = s.RecordNativeRootFence(ctx, jo, policy); e != nil {
			t.Fatal(e)
		}
	}
	var fenceWG sync.WaitGroup
	fenceErrors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		fenceWG.Add(1)
		go func() { defer fenceWG.Done(); fenceErrors <- s.RecordNativeRootFence(ctx, jo, policy) }()
	}
	fenceWG.Wait()
	close(fenceErrors)
	for err := range fenceErrors {
		if err != nil {
			t.Fatal("concurrent receipt retry", err)
		}
	}
	changedJournal := journal
	changedJournal.ExitCode = 1
	f.setEvidence("/v1/native/executions/"+permit+"/journal", changedJournal)
	changedProof, err := nativeidentity.ObserveProtectedJournal(ctx, f.evidenceConfig, permit)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordNativeRootFence(ctx, changedProof, policy); !errors.Is(err, ErrConflict) {
		t.Fatal("changed closure replaced original", err)
	}
	f.setEvidence("/v1/native/executions/"+permit+"/journal", journal)
	rootFence, e := s.LookupNativeRootFence(ctx, permit)
	if e != nil || rootFence.JobID != o.Record().JobID || rootFence.Qualification != "local_network_closed_containment_unqualified" {
		t.Fatal(rootFence, e)
	}
	if e = copy.RecordNativeRootFence(ctx, jo, policy); !errors.Is(e, ErrFence) {
		t.Fatal("restored lineage minted fence", e)
	}
	if e = s.checkNativeRootFences(ctx); e != nil {
		t.Fatal(e)
	}
	exerciseNativeRetirement(t, f, permit, jo, policy)
	journal.ResourceID = "22222222-2222-4222-8222-222222222222"
	f.setEvidence("/v1/native/executions/"+permit+"/journal", journal)
	jo, e = nativeidentity.ObserveProtectedJournal(ctx, f.evidenceConfig, permit)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CorrelateProtectedJournal(ctx, jo, policy); e == nil {
		t.Fatal("wrong incarnation correlated")
	}
	if e = s.checkNativeJobs(ctx); e != nil {
		t.Fatal(e)
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("job completion released capacity")
	}
	path := s.path
	s.Close()
	reopened, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if _, _, e = reopened.LookupNativeRetirement(ctx, permit); e != nil {
		t.Fatal("retirement lost on reopen", e)
	}
	if e = reopened.checkNativeRetirements(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.LookupNativeRootFence(ctx, permit); e != nil {
		t.Fatal("fence lost on reopen", e)
	}
	if e = reopened.RecordNativeJob(ctx, permit, o); e != nil {
		t.Fatal("restart retry", e)
	}
	if _, e = reopened.db.Exec("UPDATE native_job_observations SET state='in_progress'"); e == nil {
		t.Fatal("mutable job history")
	}
	if _, e = reopened.db.Exec("DROP TRIGGER native_job_observation_permanent; DELETE FROM native_job_observations WHERE state='completed'"); e != nil {
		t.Fatal(e)
	}
	if e = reopened.RecordNativeJob(ctx, permit, o); e == nil {
		t.Fatal("recreated deleted terminal evidence")
	}
	if e = reopened.checkNativeRootFences(ctx); e == nil {
		t.Fatal("fence retained without terminal job")
	}
	if e = reopened.checkNativeJobs(ctx); e == nil {
		t.Fatal("missing history ignored")
	}
}

func TestNativeJobMigration22PreservesChargeAndClosesReadiness(t *testing.T) {
	s := placementFixture(t)
	stockPlan(t, s, "job-migrate")
	if _, e := s.db.Exec(nativeJobDowngradeSQL + "PRAGMA user_version=22"); e != nil {
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
		t.Fatal("migration lost charge/retained readiness")
	}
}

func TestNativeFenceMigration23PreservesCharge(t *testing.T) {
	s := placementFixture(t)
	stockPlan(t, s, "fence-migrate")
	if _, e := s.db.Exec(nativeFenceDowngradeSQL + "PRAGMA user_version=23"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	v, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	snap := sqliteSnapshot(t, v)
	if snap.Ready || snap.Allocated.Global.Runners != 1 {
		t.Fatal("migration lost charge or readiness gate", snap)
	}
}
