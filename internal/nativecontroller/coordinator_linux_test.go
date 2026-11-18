//go:build linux

package nativecontroller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

const createOp = "11111111-1111-4111-8111-111111111111"
const startOp = "22222222-2222-4222-8222-222222222222"
const resource = "33333333-3333-4333-8333-333333333333"

type ownerFixture struct {
	t                       *testing.T
	mu                      sync.Mutex
	mode                    string
	descriptor              nativeidentity.Descriptor
	store                   *admission.SQLiteStore
	authority               admission.NativeOwnerAuthority
	parent                  admission.FreshNativeClaim
	create, start           admission.NativeOperationClaim
	bootstrap               admission.NativeBootstrapClaim
	bootstrapCalls          int
	createCalls, startCalls int
}

func (o *ownerFixture) serve(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if r.URL.Path == nativeidentity.Path {
		json.NewEncoder(w).Encode(o.descriptor)
		return
	}
	if r.URL.Path == "/test/auth" {
		a, e := admission.AuthenticateNativeOwner(r.TLS)
		if e != nil {
			o.t.Error(e)
		}
		o.authority = a
		w.WriteHeader(204)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/native/executions" {
		o.createCalls++
		if o.mode == "redirect" {
			http.Redirect(w, r, "https://elsewhere.invalid", http.StatusTemporaryRedirect)
			return
		}
		var in struct {
			Envelope admission.NativeExecutionEnvelope `json:"envelope"`
			UserData string                            `json:"user_data"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		digest, e := admission.NativeEnvelopeDigest(in.Envelope)
		if e != nil {
			o.t.Error(e)
			w.WriteHeader(500)
			return
		}
		parent, e := o.store.ConsumeNativeExecution(r.Context(), o.authority, in.Envelope.PermitID, digest)
		if e != nil {
			o.t.Error(e)
			w.WriteHeader(409)
			return
		}
		o.parent = parent
		claim, e := o.store.ClaimNativeCreate(r.Context(), o.authority, parent.Envelope.PermitID, parent.EnvelopeDigest, parent.ClaimID)
		if e != nil {
			o.t.Error(e)
			w.WriteHeader(409)
			return
		}
		o.create = claim
		if o.mode == "create-drop" {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"create_step_claim_id": claim.ID})
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/bootstrap") {
		o.bootstrapCalls++
		claim, e := o.store.ClaimNativeBootstrap(r.Context(), o.authority, o.parent.Envelope.PermitID, o.parent.EnvelopeDigest, o.parent.ClaimID)
		if e != nil {
			o.t.Error(e)
			w.WriteHeader(409)
			return
		}
		o.bootstrap = claim
		if o.mode == "bootstrap-drop" {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"bootstrap_claim_id": claim.ID})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/native/bootstraps/"+o.bootstrap.ID {
		c := o.bootstrap
		p := c.Plan
		outcome := "verified"
		if o.mode == "bootstrap-unknown" {
			outcome = "unknown"
		}
		record := nativeidentity.BootstrapRecord{ClaimID: c.ID, PlanDigest: c.PlanDigest, PermitID: p.PermitID, EnvelopeDigest: p.EnvelopeDigest, ParentClaimID: p.ParentClaimID, DomainID: p.DomainID, PolicyDigest: p.PolicyDigest, NativeName: p.NativeName, ResourceID: p.ResourceID, ImageFingerprint: p.ImageFingerprint, Outcome: outcome, LaunchSHA256: p.LaunchSHA256, LaunchBytes: p.LaunchBytes, JITSHA256: p.JITSHA256, JITBytes: p.JITBytes, LaunchMode: "0600", LaunchType: "file", JITMode: "0600", JITType: "file"}
		if o.mode == "bootstrap-wrong-uuid" {
			record.ResourceID = startOp
		}
		json.NewEncoder(w).Encode(record)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start") {
		o.startCalls++
		claim, e := o.store.ClaimNativeStart(r.Context(), o.authority, o.parent.Envelope.PermitID, o.parent.EnvelopeDigest, o.parent.ClaimID)
		if e != nil {
			o.t.Error(e)
			w.WriteHeader(409)
			return
		}
		o.start = claim
		if o.mode == "start-drop" {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"start_step_claim_id": claim.ID})
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/native/operations/") {
		id := strings.TrimPrefix(r.URL.Path, "/v1/native/operations/")
		terminal := strings.HasSuffix(id, "/terminal")
		id = strings.TrimSuffix(id, "/terminal")
		claim := o.create
		op := createOp
		if id == o.start.ID {
			claim = o.start
			op = startOp
		}
		if id != claim.ID {
			w.WriteHeader(404)
			return
		}
		outcome := "accepted"
		rid := ""
		if claim.Step == admission.NativeStart {
			rid = resource
		}
		if terminal && o.mode != "pending" {
			outcome = "succeeded"
			rid = resource
			if o.mode == "terminal-failed" {
				outcome = "failed"
				rid = ""
			}
		}
		record := nativeidentity.OperationRecord{PermitID: claim.PermitID, EnvelopeDigest: claim.EnvelopeDigest, ParentClaimID: claim.ParentClaimID, Step: claim.Step, StepClaimID: claim.ID, DomainID: o.parent.Envelope.DomainID, PolicyDigest: o.parent.Envelope.PolicyDigest, NativeName: o.parent.Envelope.NativeName, OperationID: op, Outcome: outcome, ResourceID: rid}
		json.NewEncoder(w).Encode(record)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/native/quotas/"+o.create.ID {
		envelope := o.parent.Envelope
		bytes := envelope.Resources.Disk * (1 << 20)
		enforced := bytes
		if o.mode == "quota-bad" {
			enforced++
		}
		json.NewEncoder(w).Encode(nativeidentity.QuotaRecord{PermitID: o.create.PermitID, EnvelopeDigest: o.create.EnvelopeDigest, ParentClaimID: o.create.ParentClaimID, CreateStepClaimID: o.create.ID, DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest, NativeName: envelope.NativeName, ResourceID: resource, CreateOperationID: createOp, Backend: "zfs", Dataset: "private/containers/" + envelope.NativeName, DatasetGUID: "1234", QuotaKind: "quota", RequestedBytes: bytes, EnforcedBytes: enforced, InstanceState: "stopped"})
		return
	}
	w.WriteHeader(404)
}

type testFixture struct {
	c        *Coordinator
	s        *admission.SQLiteStore
	plan     admission.ExecutionPlan
	host     admission.HostBinding
	auth     admission.StockProviderAuthority
	digest   string
	owner    *ownerFixture
	config   Configuration
	userData string
}

func setup(t *testing.T, mode string) testFixture {
	t.Helper()
	ctx := context.Background()
	owner := &ownerFixture{t: t, mode: mode}
	server, controllerTLS, ownerTLS := testsupport.NativeTLS(t, http.HandlerFunc(owner.serve))
	domain := strings.Repeat("a", 64)
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("4", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	settings, _ := json.Marshal(map[string]any{"agentName": "runner-test", "workFolder": "_work", "ephemeral": true, "disableUpdate": true})
	jit, _ := json.Marshal(map[string]string{".runner": base64.StdEncoding.EncodeToString(settings), ".credentials": base64.StdEncoding.EncodeToString([]byte(`{"scheme":"OAuth"}`)), ".credentials_rsaparams": base64.StdEncoding.EncodeToString([]byte(`{"fixture":true}`))})
	payload, _ := json.Marshal(protectedboot.Payload{Version: protectedboot.Version, RunnerName: "runner-test", Image: image, JIT: base64.StdEncoding.EncodeToString(jit)})
	policy, e := nativeapi.NewPolicy(nativeapi.PolicySpec{Version: nativeapi.ProtectedPolicyVersion, Controller: "controller", Host: "a", DomainID: domain, NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: "6.0.4", Pools: map[string]nativeapi.Pool{"app": {Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge", ZFSDataset: "private", ProtectedImage: &image}}})
	if e != nil {
		t.Fatal(e)
	}
	owner.descriptor = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: policy.Digest(), OwnerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(ownerTLS.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("e", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "test-owner", NativeServerVersion: "6.0.4"}
	endpoint := nativeidentity.Configuration{Endpoint: server.URL, TLS: controllerTLS, DomainID: domain, PolicyDigest: policy.Digest(), OwnerServerLeaf: owner.descriptor.OwnerServerLeaf, OwnerClientLeaf: owner.descriptor.OwnerClientLeaf}
	budget := admission.Resources{CPU: 4000, RAM: 8192, Disk: 16384, Runners: 4}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"app": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Hour}
	s, e := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	owner.store = s
	fence, e := s.BeginReconciliation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	observed, e := nativeidentity.Observe(ctx, endpoint)
	if e != nil {
		t.Fatal(e)
	}
	measured, e := admission.ObservedNativeHostBinding("a", observed)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.BindHostIdentities(ctx, fence, []admission.HostBinding{measured}); e != nil {
		t.Fatal(e)
	}
	host, e := s.LookupHostBinding(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	binding, e := s.EnrollNativeDomain(ctx, admission.NativeDomainSpec{Controller: "controller", Host: "a", HostBindingSequence: host.Sequence, DomainID: domain, PolicyDigest: policy.Digest(), OwnerClientLeaf: endpoint.OwnerClientLeaf, OwnerServerLeaf: endpoint.OwnerServerLeaf}, observed)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteReconciliation(ctx, fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "a", ObservedAt: time.Now(), Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}}); e != nil {
		t.Fatal(e)
	}
	transport := &http.Transport{TLSClientConfig: ownerTLS, DisableKeepAlives: true}
	client := &http.Client{Transport: transport}
	response, e := client.Get(server.URL + "/test/auth")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	transport.CloseIdleConnections()
	auth := admission.StockProviderAuthority{Controller: "controller", PoolID: "app", ProviderName: "stock", ClientFingerprint: strings.Repeat("f", 64)}
	digest := strings.Repeat("b", 64)
	execution := admission.ExecutionImage{Controller: "controller", Name: "runner-test", Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("d", 64), Profiles: []string{"runner"}, Type: "container", Arch: "amd64"}
	request := admission.PlacementRequest{Key: nativeidentity.Fingerprint([]byte("controller\x00app\x00runner-test")), Pool: "app", Resources: admission.Resources{CPU: 1000, RAM: 1024, Disk: 4096, Runners: 1}, Execution: execution, Hosts: []string{"a"}, Strategy: "first_fit"}
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	execution.Name = ""
	if _, e = s.IssueBrokerGrants(ctx, admission.BrokerGrantBatch{ID: strings.Repeat("c", 64), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: []admission.BrokerGrantSpec{{Authority: auth, Host: "a", HostBindingSequence: host.Sequence, Resources: request.Resources, Execution: execution, ExpiresAt: time.Now().Add(time.Hour)}}}); e != nil {
		t.Fatal(e)
	}
	plan, e := s.AdmitStockProviderPlacement(ctx, request, auth, digest)
	if e != nil {
		t.Fatal(e)
	}
	cconfig := Configuration{Store: s, Policy: policy, Owner: endpoint, DomainBindingSequence: binding.Sequence, PollInterval: 10 * time.Millisecond, TerminalTimeout: time.Second}
	c, e := New(ctx, cconfig)
	if e != nil {
		t.Fatal(e)
	}
	return testFixture{c, s, plan, host, auth, digest, owner, cconfig, string(payload)}
}
func TestCoordinatorRunsCanonicalOwnerSequenceOnce(t *testing.T) {
	f := setup(t, "")
	ctx := context.Background()
	record, e := f.c.Execute(ctx, f.plan, f.host, f.auth, f.digest, f.userData)
	if e != nil || record.State != "confirmed" || record.ResourceID != resource || record.OperationID != startOp {
		t.Fatal(record, e)
	}
	if _, e = f.c.Execute(ctx, f.plan, f.host, f.auth, f.digest, f.userData); !errors.Is(e, admission.ErrDispatchClaimed) {
		t.Fatal("replay did not preserve canonical one-shot", e)
	}
	f.owner.mu.Lock()
	creates, starts, boots := f.owner.createCalls, f.owner.startCalls, f.owner.bootstrapCalls
	f.owner.mu.Unlock()
	if creates != 1 || starts != 1 || boots != 1 {
		t.Fatal("mutation replay", creates, starts, boots)
	}
	snap, e := f.s.Snapshot(ctx)
	if e != nil || snap.Allocated.Global.Runners != 1 {
		t.Fatal("charge lost", e)
	}
}
func TestCoordinatorUncertaintyNeverRetriesOrReleases(t *testing.T) {
	for _, mode := range []string{"create-drop", "start-drop", "bootstrap-drop", "bootstrap-unknown", "bootstrap-wrong-uuid", "redirect", "quota-bad", "terminal-failed", "pending"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t, mode)
			ctx := context.Background()
			if _, e := f.c.Execute(ctx, f.plan, f.host, f.auth, f.digest, f.userData); !errors.Is(e, admission.ErrDispatchUnknown) {
				t.Fatal("uncertainty hidden", e)
			}
			if _, e := f.c.Execute(ctx, f.plan, f.host, f.auth, f.digest, f.userData); !errors.Is(e, admission.ErrDispatchClaimed) {
				t.Fatal("uncertain request redispatched", e)
			}
			f.owner.mu.Lock()
			creates, starts, boots := f.owner.createCalls, f.owner.startCalls, f.owner.bootstrapCalls
			f.owner.mu.Unlock()
			wantStarts := 0
			if mode == "start-drop" {
				wantStarts = 1
			}
			wantBoots := 0
			if strings.HasPrefix(mode, "bootstrap-") || mode == "start-drop" {
				wantBoots = 1
			}
			if creates != 1 || starts != wantStarts || boots != wantBoots {
				t.Fatal("unexpected mutation", creates, starts, boots)
			}
			snap, e := f.s.Snapshot(ctx)
			if e != nil || snap.Allocated.Global.Runners != 1 {
				t.Fatal("unknown charge released", e)
			}
		})
	}
}
func TestCoordinatorRefusesWrongEnrollmentAndActiveRuntime(t *testing.T) {
	f := setup(t, "")
	bad := f.config
	bad.Owner.PolicyDigest = strings.Repeat("0", 64)
	if _, e := New(context.Background(), bad); e == nil {
		t.Fatal("wrong policy accepted")
	}
	snapshot, e := f.s.Snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	db, identity := filepath.Join(dir, "runtime.db"), filepath.Join(dir, "identity")
	if e := admission.InitializeSQLite(db, identity, snapshot.Config); e != nil {
		t.Fatal(e)
	}
	active, e := admission.OpenRuntimeSQLite(db, identity)
	if e != nil {
		t.Fatal(e)
	}
	defer active.Close()
	bad = f.config
	bad.Store = active
	if _, e := New(context.Background(), bad); !errors.Is(e, ErrUnqualified) {
		t.Fatal("active runtime bypassed qualification guard", e)
	}
}

func TestCoordinatorFreezesNestedTLSConfiguration(t *testing.T) {
	f := setup(t, "")
	original := f.config.Owner.TLS
	frozen := f.c.owner.TLS
	if frozen == original || frozen.RootCAs == original.RootCAs || frozen.Certificates[0].PrivateKey == original.Certificates[0].PrivateKey {
		t.Fatal("caller authority material retained")
	}
	before := string(frozen.Certificates[0].Certificate[0])
	original.Certificates[0].Certificate[0][0] ^= 0xff
	original.Certificates = nil
	original.RootCAs = nil
	if string(frozen.Certificates[0].Certificate[0]) != before {
		t.Fatal("certificate bytes changed")
	}
	record, e := f.c.Execute(context.Background(), f.plan, f.host, f.auth, f.digest, f.userData)
	if e != nil || record.State != "confirmed" {
		t.Fatal("caller config mutation changed frozen client", record, e)
	}
}

func TestExecuteProtectedRejectsAbsentSourceBeforeMutation(t *testing.T) {
	var c *Coordinator
	if _, e := c.ExecuteProtected(context.Background(), admission.ExecutionPlan{}, admission.HostBinding{}, admission.StockProviderAuthority{}, "", stockjit.ProtectedBootstrap{}); e == nil {
		t.Fatal("absent source accepted")
	}
}
