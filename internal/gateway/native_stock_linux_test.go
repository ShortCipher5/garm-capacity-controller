//go:build linux

package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
	"net/http/httptest"
)

const bridgePool = "11111111-1111-4111-8111-111111111111"
const bridgeRepo = "22222222-2222-4222-8222-222222222222"
const bridgeCreateOp = "11111111-1111-4111-8111-111111111111"
const bridgeStartOp = "22222222-2222-4222-8222-222222222222"
const bridgeResource = "33333333-3333-4333-8333-333333333333"

type nativeBridgeOwner struct {
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
	c        *nativecontroller.Coordinator
	s        *admission.SQLiteStore
	plan     admission.ExecutionPlan
	host     admission.HostBinding
	auth     admission.StockProviderAuthority
	digest   string
	owner    *nativeBridgeOwner
	config   nativecontroller.Configuration
	userData string
}

func nativeBridgeSetup(t *testing.T, mode string) nativeBridgeFixture {
	t.Helper()
	ctx := context.Background()
	owner := &nativeBridgeOwner{t: t, mode: mode}
	server, controllerTLS, ownerTLS := testsupport.NativeTLS(t, http.HandlerFunc(owner.serve))
	domain := strings.Repeat("a", 64)
	files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
	raw, _ := json.Marshal(files)
	image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("4", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
	settings, _ := json.Marshal(map[string]any{"agentName": "runner-test", "workFolder": "_work", "ephemeral": true, "disableUpdate": true})
	jit, _ := json.Marshal(map[string]string{".runner": base64.StdEncoding.EncodeToString(settings), ".credentials": base64.StdEncoding.EncodeToString([]byte(`{"scheme":"OAuth"}`)), ".credentials_rsaparams": base64.StdEncoding.EncodeToString([]byte(`{"fixture":true}`))})
	payload, _ := json.Marshal(protectedboot.Payload{Version: protectedboot.Version, RunnerName: "runner-test", Image: image, JIT: base64.StdEncoding.EncodeToString(jit)})
	policy, e := nativeapi.NewPolicy(nativeapi.PolicySpec{Version: nativeapi.ProtectedPolicyVersion, Controller: "controller", Host: "a", DomainID: domain, NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: "6.0.4", Pools: map[string]nativeapi.Pool{bridgePool: {Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge", ZFSDataset: "private", ProtectedImage: &image}}})
	if e != nil {
		t.Fatal(e)
	}
	owner.descriptor = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: policy.Digest(), OwnerServerLeaf: nativeidentity.Fingerprint(server.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(ownerTLS.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("e", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "test-owner", NativeServerVersion: "6.0.4"}
	endpoint := nativeidentity.Configuration{Endpoint: server.URL, TLS: controllerTLS, DomainID: domain, PolicyDigest: policy.Digest(), OwnerServerLeaf: owner.descriptor.OwnerServerLeaf, OwnerClientLeaf: owner.descriptor.OwnerClientLeaf}
	budget := admission.Resources{CPU: 4000, RAM: 8192, Disk: 16384, Runners: 4}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{bridgePool: {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Hour}
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
	auth := admission.StockProviderAuthority{Controller: "controller", PoolID: bridgePool, ProviderName: "stock", ClientFingerprint: strings.Repeat("f", 64)}
	digest := strings.Repeat("b", 64)
	execution := admission.ExecutionImage{Controller: "controller", Name: "runner-test", Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("d", 64), Profiles: []string{"runner"}, Type: "container", Arch: "amd64"}
	request := admission.PlacementRequest{Key: nativeidentity.Fingerprint([]byte("controller\x00app\x00runner-test")), Pool: bridgePool, Resources: admission.Resources{CPU: 1000, RAM: 1024, Disk: 4096, Runners: 1}, Execution: execution, Hosts: []string{"a"}, Strategy: "first_fit"}
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	execution.Name = ""
	if _, e = s.IssueBrokerGrants(ctx, admission.BrokerGrantBatch{ID: strings.Repeat("c", 64), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: []admission.BrokerGrantSpec{{Authority: auth, Host: "a", HostBindingSequence: host.Sequence, Resources: request.Resources, Execution: execution, ExpiresAt: time.Now().Add(time.Hour)}}}); e != nil {
		t.Fatal(e)
	}
	plan := admission.ExecutionPlan{}
	cconfig := nativecontroller.Configuration{Store: s, Policy: policy, Owner: endpoint, DomainBindingSequence: binding.Sequence, PollInterval: 10 * time.Millisecond, TerminalTimeout: time.Second}
	c, e := nativecontroller.New(ctx, cconfig)
	if e != nil {
		t.Fatal(e)
	}
	return nativeBridgeFixture{c, s, plan, host, auth, digest, owner, cconfig, string(payload)}
}

func TestNativeStockBridgeUsesActualSourceAndCanonicalCoordinator(t *testing.T) {
	for _, mode := range []string{"", "binding-drift", "create-drop", "source-pool", "source-agent"} {
		t.Run(mode, func(t *testing.T) {
			f := nativeBridgeSetup(t, mode)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("unexpected source mutation")
				}
				switch r.URL.Path {
				case "/api/v1/pools/" + bridgePool:
					if r.Header.Get("Authorization") != "Bearer admin" {
						t.Error("wrong pool credential")
					}
					json.NewEncoder(w).Encode(map[string]any{"id": bridgePool, "repo_id": bridgeRepo, "provider_name": "stock", "flavor": "small", "image": "prebuilt", "os_type": "linux", "os_arch": "amd64", "max_runners": 4})
				case "/api/v1/instances/runner-test":
					if r.Header.Get("Authorization") != "Bearer admin" {
						t.Error("wrong instance credential")
					}
					pool := bridgePool
					if mode == "source-pool" {
						pool = bridgeRepo
					}
					json.NewEncoder(w).Encode(map[string]any{"id": "99999999-9999-4999-8999-999999999999", "name": "runner-test", "pool_id": pool, "provider_name": "stock", "agent_id": 123, "status": "creating", "runner_status": "pending"})
				case "/api/v1/metadata/credentials/runner":
					if r.Header.Get("Authorization") != "Bearer instance-secret" {
						t.Error("wrong metadata credential")
					}
					id := 123
					if mode == "source-agent" {
						id = 124
					}
					json.NewEncoder(w).Encode(map[string]any{"agentId": id, "agentName": "runner-test", "gitHubUrl": "https://github.com/example/disposable", "ephemeral": true, "disableUpdate": true, "workFolder": "_work"})
				case "/api/v1/metadata/credentials/credentials":
					fmt.Fprint(w, `{"scheme":"OAuth","secret":"fixture"}`)
				case "/api/v1/metadata/credentials/credentials_rsaparams":
					fmt.Fprint(w, `{"private":"fixture"}`)
				default:
					t.Error("unexpected source URL", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			trust := &tls.Config{RootCAs: roots}
			ownership := stockidentity.Configuration{Endpoint: server.URL, Token: "admin", Provider: "stock", Pool: bridgePool, TLS: trust}
			observed, e := stockidentity.Observe(context.Background(), ownership)
			if e != nil {
				t.Fatal(e)
			}
			if e = f.s.ProfileUpsert(context.Background(), "small", 1000, 1024, 4096); e != nil {
				t.Fatal(e)
			}
			execution := admission.ExecutionImage{Controller: "controller", Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("d", 64), Profiles: []string{"runner"}, Type: "container", Arch: "amd64"}
			profile, e := f.s.RegisterBrokerPoolProfile(context.Background(), admission.BrokerPoolProfileRequest{SourceIdentity: observed.SourceIdentity(), SizeProfile: "small", Authority: f.auth, Execution: execution}, observed, map[int64]nativeapi.Policy{f.config.DomainBindingSequence: f.config.Policy})
			if e != nil {
				t.Fatal(e)
			}
			metadata, e := stockjit.New(stockjit.Policy{MetadataURL: server.URL + "/api/v1/metadata", ControllerID: "controller", PoolID: bridgePool, GitHubURL: "https://github.com/example/disposable"}, trust)
			if e != nil {
				t.Fatal(e)
			}
			defer metadata.Close()
			facade, e := NewNativeStock(context.Background(), f.s, "controller", map[string]NativeStockPool{bridgePool: {Metadata: metadata, Ownership: ownership, Hosts: map[string]nativecontroller.Configuration{"a": f.config}}})
			if e != nil {
				t.Fatal(e)
			}
			if mode == "binding-drift" {
				alternate := httptest.NewTLSServer(server.Config.Handler)
				defer alternate.Close()
				other := ownership
				other.Endpoint = alternate.URL
				alternateObserved, err := stockidentity.Observe(context.Background(), other)
				if err != nil {
					t.Fatal(err)
				}
				f.owner.beforeQuota = func() {
					_, err := f.s.RegisterBrokerPoolProfile(context.Background(), admission.BrokerPoolProfileRequest{ExpectedProfileSequence: profile.Sequence, SourceIdentity: alternateObserved.SourceIdentity(), SizeProfile: "small", Authority: f.auth, Execution: execution}, alternateObserved, map[int64]nativeapi.Policy{f.config.DomainBindingSequence: f.config.Policy})
					if err != nil {
						t.Error(err)
					}
				}
			}
			b := bootstrap("runner-test", bridgePool)
			b.Flavor = "small"
			b.Image = "prebuilt"
			b.MetadataURL = server.URL + "/api/v1/metadata"
			b.InstanceToken = "instance-secret"
			b.OSType = common.Linux
			result, e := facade.CreateStockProvider(context.Background(), b, f.auth)
			if mode == "" {
				if e != nil || result.Status != common.InstanceRunning || result.ProviderID != b.Name {
					t.Fatal(result, e)
				}
				f.owner.mu.Lock()
				permit := f.owner.parent.Envelope.PermitID
				f.owner.mu.Unlock()
				linked, err := f.s.LookupNativeRunnerBinding(context.Background(), permit)
				if err != nil || linked.Runner.RunnerID != 123 || linked.Instance.Name != b.Name || linked.StockSourceIdentity != observed.SourceIdentity() || linked.ProfileSequence <= 0 {
					t.Fatal("authentic runner incarnation not persisted", err, linked)
				}
				if _, e = facade.CreateStockProvider(context.Background(), b, f.auth); e == nil {
					t.Fatal("repeated create reported stale instance as live")
				}
			} else if e == nil {
				t.Fatal("uncertain or wrong source succeeded")
			}
			f.owner.mu.Lock()
			if mode == "binding-drift" && (f.owner.bootstrapCalls != 0 || f.owner.startCalls != 0) {
				t.Error("runner binding failure continued to bootstrap/start")
			}
			calls := f.owner.createCalls
			f.owner.mu.Unlock()
			want := 1
			if strings.HasPrefix(mode, "source-") {
				want = 0
			}
			if calls != want {
				t.Fatal("source mismatch or replay mutated native", calls)
			}
			snapshot, e := f.s.Snapshot(context.Background())
			if e != nil || snapshot.Allocated.Global.Runners != 1 {
				t.Fatal("capacity charge changed", e)
			}
		})
	}
}
