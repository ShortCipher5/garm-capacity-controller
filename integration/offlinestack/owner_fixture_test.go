//go:build linux

package offlinestack

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

const bridgePool = "11111111-1111-4111-8111-111111111111"
const bridgeRepo = "22222222-2222-4222-8222-222222222222"
const bridgeCreateOp = "11111111-1111-4111-8111-111111111111"
const bridgeStartOp = "22222222-2222-4222-8222-222222222222"
const bridgeResource = "33333333-3333-4333-8333-333333333333"

type nativeBridgeOwner struct {
	payload          protectedboot.Payload
	retirementClaims map[string]admission.NativeRetirementClaim
	retirementCalls  map[string]int
	retirementCancel context.CancelFunc

	journalAt    time.Time
	journalCalls int

	beforeQuota func()

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

func (o *nativeBridgeOwner) serve(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.serveRetirement(w, r) {
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/journal") {
		o.journalCalls++
		env := o.parent.Envelope
		if o.journalAt.IsZero() {
			o.journalAt = time.Now().UTC()
		}
		if o.mode == "journal-ack-lost" && o.journalCalls == 1 {
			if h, ok := w.(http.Hijacker); ok {
				c, _, err := h.Hijack()
				if err == nil {
					c.Close()
					return
				}
			}
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(nativeidentity.ProtectedJournalRecord{PermitID: env.PermitID, EnvelopeDigest: o.parent.EnvelopeDigest, DomainID: env.DomainID, PolicyDigest: env.PolicyDigest, NativeName: env.NativeName, ResourceID: bridgeResource, ImageFingerprint: env.Execution.Fingerprint, InitSHA256: strings.Repeat("3", 64), State: "process_tree_exited_unqualified", RecordAt: o.journalAt, GuestNetworkClosed: true, ExitCode: 0})
		return
	}
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
		payload, err := protectedboot.Decode([]byte(in.UserData))
		if err != nil {
			o.t.Error("invalid actual bootstrap payload", err)
			w.WriteHeader(400)
			return
		}
		o.payload = payload
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

		if o.mode == "create-ack-lost" {
			if h, ok := w.(http.Hijacker); ok {
				c, _, err := h.Hijack()
				if err == nil {
					c.Close()
					return
				}
			}
			o.t.Error("could not drop test response")
			return
		}
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
		files, err := protectedboot.Materialize(o.payload, protectedboot.Binding{DomainID: claim.Plan.DomainID, PolicyDigest: claim.Plan.PolicyDigest, ResourceID: claim.Plan.ResourceID, ImageFingerprint: claim.Plan.ImageFingerprint})
		if err != nil || files[0].SHA256 != claim.Plan.LaunchSHA256 || files[1].SHA256 != claim.Plan.JITSHA256 {
			o.t.Error("actual codec does not match canonical bootstrap plan", err)
			w.WriteHeader(409)
			return
		}
		o.bootstrap = claim

		if o.mode == "bootstrap-ack-lost" {
			if h, ok := w.(http.Hijacker); ok {
				c, _, err := h.Hijack()
				if err == nil {
					c.Close()
					return
				}
			}
			o.t.Error("could not drop test response")
			return
		}
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
			record.ResourceID = bridgeStartOp
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

		if o.mode == "start-ack-lost" {
			if h, ok := w.(http.Hijacker); ok {
				c, _, err := h.Hijack()
				if err == nil {
					c.Close()
					return
				}
			}
			o.t.Error("could not drop test response")
			return
		}
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
		op := bridgeCreateOp
		if id == o.start.ID {
			claim = o.start
			op = bridgeStartOp
		}
		if id != claim.ID {
			w.WriteHeader(404)
			return
		}
		outcome := "accepted"
		rid := ""
		if claim.Step == admission.NativeStart {
			rid = bridgeResource
		}
		if terminal && o.mode != "pending" {
			outcome = "succeeded"
			rid = bridgeResource
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
		if o.beforeQuota != nil {
			o.beforeQuota()
			o.beforeQuota = nil
		}
		envelope := o.parent.Envelope
		bytes := envelope.Resources.Disk * (1 << 20)
		enforced := bytes
		if o.mode == "quota-bad" {
			enforced++
		}
		json.NewEncoder(w).Encode(nativeidentity.QuotaRecord{PermitID: o.create.PermitID, EnvelopeDigest: o.create.EnvelopeDigest, ParentClaimID: o.create.ParentClaimID, CreateStepClaimID: o.create.ID, DomainID: envelope.DomainID, PolicyDigest: envelope.PolicyDigest, NativeName: envelope.NativeName, ResourceID: bridgeResource, CreateOperationID: bridgeCreateOp, Backend: "zfs", Dataset: "private/containers/" + envelope.NativeName, DatasetGUID: "1234", QuotaKind: "quota", RequestedBytes: bytes, EnforcedBytes: enforced, InstanceState: "stopped"})
		return
	}
	w.WriteHeader(404)
}

type nativeBridgeFixture struct {
	path   string
	s      *admission.SQLiteStore
	auth   admission.StockProviderAuthority
	owner  *nativeBridgeOwner
	config nativecontroller.Configuration
}

func nativeBridgeSetupConfigured(t *testing.T, mode, controller string) nativeBridgeFixture {
	t.Helper()
	ctx := context.Background()
	owner := &nativeBridgeOwner{t: t, mode: mode}
	server, controllerTLS, ownerTLS := testsupport.NativeTLS(t, http.HandlerFunc(owner.serve))
	domain := strings.Repeat("a", 64)
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("4", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	policy, e := nativeapi.NewPolicy(nativeapi.PolicySpec{Version: nativeapi.ProtectedPolicyVersion, Controller: controller, Host: "a", DomainID: domain, NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: "6.0.4", Pools: map[string]nativeapi.Pool{bridgePool: {Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge", ZFSDataset: "private", ProtectedImage: &image}}})
	if e != nil {
		t.Fatal(e)
	}
	owner.descriptor = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: policy.Digest(), OwnerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(ownerTLS.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("e", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "test-owner", NativeServerVersion: "6.0.4"}
	endpoint := nativeidentity.Configuration{Endpoint: server.URL, TLS: controllerTLS, DomainID: domain, PolicyDigest: policy.Digest(), OwnerServerLeaf: owner.descriptor.OwnerServerLeaf, OwnerClientLeaf: owner.descriptor.OwnerClientLeaf}
	budget := admission.Resources{CPU: 4000, RAM: 8192, Disk: 16384, Runners: 4}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{bridgePool: {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Hour}
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, e := admission.OpenSQLite(path, config)
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
	binding, e := s.EnrollNativeDomain(ctx, admission.NativeDomainSpec{Controller: controller, Host: "a", HostBindingSequence: host.Sequence, DomainID: domain, PolicyDigest: policy.Digest(), OwnerClientLeaf: endpoint.OwnerClientLeaf, OwnerServerLeaf: endpoint.OwnerServerLeaf}, observed)
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
	auth := admission.StockProviderAuthority{Controller: controller, PoolID: bridgePool, ProviderName: "stock", ClientFingerprint: strings.Repeat("f", 64)}
	cconfig := nativecontroller.Configuration{Store: s, Policy: policy, Owner: endpoint, DomainBindingSequence: binding.Sequence, PollInterval: 10 * time.Millisecond, TerminalTimeout: time.Second}
	return nativeBridgeFixture{path: path, s: s, auth: auth, owner: owner, config: cconfig}
}

func bootstrap(name, pool string) common.BootstrapInstance {
	osType, arch := "linux", "x64"
	download, filename := "https://example.invalid/disposable-runner.tar.gz", "actions-runner-linux-x64.tar.gz"
	return common.BootstrapInstance{Name: name, PoolID: pool, OSType: common.Linux, OSArch: common.Amd64, RepoURL: "https://github.com/example/disposable", CallbackURL: "https://example.invalid/callback", ExtraSpecs: []byte("{}"), JitConfigEnabled: true, Tools: []common.RunnerApplicationDownload{{OS: &osType, Architecture: &arch, DownloadURL: &download, Filename: &filename}}}
}
