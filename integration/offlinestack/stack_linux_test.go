//go:build linux

package offlinestack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
	common "github.com/cloudbase/garm-provider-common/params"
)

// This is an offline protocol integration, not a root-host qualification. The
// ledger, managed transport, demand planner, grant issue/redemption, opaque JIT
// producer and native coordinator are real implementations. The native owner
// boundary is explicitly a test TLS server: no Incus, ZFS, PID1 or live runner.
func TestOfflineManagedDemandThroughNativeCreationAndRestart(t *testing.T) {
	for _, mode := range []string{"retire-stop-cancel-restart", "retire-success", "retire-stop-ack-lost", "retire-delete-ack-lost", "retire-stop-unknown", "retire-delete-unknown", "success", "grant-result-lost", "journal-ack-lost", "create-ack-lost", "bootstrap-ack-lost", "start-ack-lost"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			const controller = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			const repository = "22222222-2222-4222-8222-222222222222"
			f := nativeBridgeSetupConfigured(t, mode, controller)
			var pending atomic.Bool
			pool := map[string]any{"id": bridgePool, "repo_id": repository, "provider_name": "stock", "generation": 1, "enabled": true, "max_runners": 1, "min_idle_runners": 0, "tags": []map[string]string{{"name": "linux"}}, "flavor": "small", "image": "prebuilt", "os_type": "linux", "os_arch": "amd64"}
			instance := map[string]any{"id": "33333333-3333-4333-8333-333333333333", "name": "runner-test", "pool_id": bridgePool, "provider_name": "stock", "agent_id": 123, "status": "creating", "runner_status": "pending"}
			originalRunner := `{"agentId":123,"agentName":"runner-test","gitHubUrl":"https://github.com/example/disposable","ephemeral":true,"workFolder":"_work","disableUpdate":true}`
			var credentialReads atomic.Int32
			stock, trust, _ := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 || r.Header.Get("Authorization") != "" {
					t.Error("managed scope/credential violated")
					w.WriteHeader(401)
					return
				}
				w.Header().Set(managedsource.SourceHeader, strings.Repeat("1", 64))
				w.Header().Set(managedsource.ControllerHeader, controller)
				if r.Method == http.MethodPost {
					prefix := "/v1/managed/metadata/" + bridgePool + "/runner-test/"
					if !pending.Load() || !strings.HasPrefix(r.URL.Path, prefix) {
						t.Error("unexpected metadata target")
						w.WriteHeader(400)
						return
					}
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["instance_token"] != "instance-secret" {
						t.Error("wrong metadata token")
						w.WriteHeader(401)
						return
					}
					credentialReads.Add(1)
					switch strings.TrimPrefix(r.URL.Path, prefix) {
					case "runner":
						fmt.Fprint(w, originalRunner)
					case "credentials":
						fmt.Fprint(w, `{"scheme":"OAuth","secret":"offline-fixture"}`)
					case "credentials_rsaparams":
						fmt.Fprint(w, `{"D":"offline-rsa-fixture"}`)
					default:
						t.Error("arbitrary credential path")
						w.WriteHeader(404)
					}
					return
				}
				if r.Method != http.MethodGet {
					t.Error("unexpected mutation")
					w.WriteHeader(405)
					return
				}
				switch r.URL.Path {
				case "/api/v1/pools":
					json.NewEncoder(w).Encode([]any{pool})
				case "/api/v1/pools/" + bridgePool:
					json.NewEncoder(w).Encode(pool)
				case "/api/v1/jobs":
					json.NewEncoder(w).Encode([]any{map[string]any{"workflow_job_id": 1, "repo_id": repository, "status": "queued", "labels": []string{"linux"}, "created_at": time.Now().Add(-time.Minute), "locked_by": "00000000-0000-0000-0000-000000000000"}})
				case "/api/v1/instances":
					if pending.Load() {
						json.NewEncoder(w).Encode([]any{instance})
					} else {
						json.NewEncoder(w).Encode([]any{})
					}
				default:
					t.Error("unapproved source path", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer stock.Close()
			pin := sha256.Sum256(stock.Certificate().RawSubjectPublicKeyInfo)
			ownership := stockidentity.Configuration{Endpoint: stock.URL, Provider: "stock", Pool: bridgePool, TLS: trust, ManagedServerKeyPin: hex.EncodeToString(pin[:]), ManagedControllerID: controller}
			observed, e := stockidentity.Observe(ctx, ownership)
			if e != nil {
				t.Fatal(e)
			}
			if e = f.s.ProfileUpsert(ctx, "small", 1000, 1024, 4096); e != nil {
				t.Fatal(e)
			}
			execution := admission.ExecutionImage{Controller: controller, Flavor: "small", Image: "prebuilt", Fingerprint: strings.Repeat("d", 64), Profiles: []string{"runner"}, Type: "container", Arch: "amd64"}
			_, e = f.s.RegisterBrokerPoolProfile(ctx, admission.BrokerPoolProfileRequest{SourceIdentity: observed.SourceIdentity(), SizeProfile: "small", Authority: f.auth, Execution: execution}, observed, map[int64]nativeapi.Policy{f.config.DomainBindingSequence: f.config.Policy})
			if e != nil {
				t.Fatal(e)
			}
			if e = f.s.ReplaceBrokerPolicy(ctx, admission.BrokerPolicy{Repositories: []admission.BrokerRepositoryPolicy{{ID: repository, Weight: 1, MaxRunners: 1, Pools: []admission.BrokerPoolPolicy{{ID: bridgePool, MaxRunners: 1}}}}}); e != nil {
				t.Fatal(e)
			}
			demand, e := garmapi.NewManagedDemand(stock.URL, ownership.ManagedServerKeyPin, controller, trust, []string{"stock"}, []string{repository})
			if e != nil {
				t.Fatal(e)
			}
			defer demand.Close()
			opt := capacitybroker.Options{MaxAge: time.Minute, MaxAssignments: 10}
			first, e := brokerinput.GrantCycle(ctx, f.s, demand, opt, time.Minute)
			if e != nil || len(first.Grants) != 1 {
				t.Fatalf("real demand did not issue one grant: %+v %v", first, e)
			}
			reopen := func() {
				if e = f.s.Close(); e != nil {
					t.Fatal(e)
				}
				f.s, e = admission.OpenSQLite(f.path, admission.Config{})
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { f.s.Close() })
				f.owner.mu.Lock()
				f.owner.store = f.s
				f.owner.mu.Unlock()
				f.config.Store = f.s
			}
			// Deliberately discard the first successful caller result: restart must reuse
			// the durable pregrant, not issue a second charge or invent an acknowledgement.
			if mode == "grant-result-lost" {
				first = brokerinput.GrantCycleResult{}
				reopen()
			}
			again, e := brokerinput.GrantCycle(ctx, f.s, demand, opt, time.Minute)
			if e != nil || len(again.Grants) != 0 || len(again.Plan.CoveredByGrant) != 1 {
				t.Fatalf("pregrant replay duplicated capacity: %+v %v", again, e)
			}
			pending.Store(true)
			policy := stockjit.Policy{MetadataURL: "https://stock.invalid/api/v1/metadata", ControllerID: controller, PoolID: bridgePool, GitHubURL: "https://github.com/example/disposable"}
			metadata, e := stockjit.NewManaged(ctx, policy, ownership)
			if e != nil {
				t.Fatal(e)
			}
			defer metadata.Close()
			facade := func() *gateway.NativeStock {
				v, e := gateway.NewNativeStock(ctx, f.s, controller, map[string]gateway.NativeStockPool{bridgePool: {Metadata: metadata, Ownership: ownership, Hosts: map[string]nativecontroller.Configuration{"a": f.config}}})
				if e != nil {
					t.Fatal(e)
				}
				return v
			}
			b := bootstrap("runner-test", bridgePool)
			b.Flavor = "small"
			b.Image = "prebuilt"
			b.MetadataURL = policy.MetadataURL
			b.InstanceToken = "instance-secret"
			b.OSType = common.Linux
			result, e := facade().CreateStockProvider(ctx, b, f.auth)
			success := strings.HasPrefix(mode, "retire-") || mode == "success" || mode == "grant-result-lost" || mode == "journal-ack-lost"
			if success {
				if e != nil || result.Status != common.InstanceRunning {
					t.Fatal("complete offline path failed", e)
				}
			} else if e == nil {
				t.Fatal("lost response reported successful")
			}
			if credentialReads.Load() < 4 {
				t.Fatal("did not retrieve authentic managed files")
			}
			f.owner.mu.Lock()
			creates, boots, starts := f.owner.createCalls, f.owner.bootstrapCalls, f.owner.startCalls
			permit := f.owner.parent.Envelope.PermitID
			f.owner.mu.Unlock()
			if creates != 1 || success && (boots != 1 || starts != 1) || mode == "create-ack-lost" && (boots != 0 || starts != 0) || mode == "bootstrap-ack-lost" && (boots != 1 || starts != 0) || mode == "start-ack-lost" && (boots != 1 || starts != 1) {
				t.Fatalf("unexpected native sequence %d/%d/%d", creates, boots, starts)
			}
			if success {
				linked, e := f.s.LookupNativeRunnerBinding(ctx, permit)
				if e != nil || linked.Runner.RunnerID != 123 || linked.StockSourceIdentity != observed.SourceIdentity() {
					t.Fatal("durable native runner linkage missing", e)
				}
			}
			reopen()
			if _, e = facade().CreateStockProvider(ctx, b, f.auth); e == nil {
				t.Fatal("restart retried or reported stale success")
			}
			f.owner.mu.Lock()
			if f.owner.createCalls != creates || f.owner.bootstrapCalls != boots || f.owner.startCalls != starts {
				t.Error("restart replayed native mutation")
			}
			f.owner.mu.Unlock()
			if success {
				observeTerminal(t, &f, repository, permit, reopen)
				if strings.HasPrefix(mode, "retire-") {
					exerciseRetirement(t, &f, permit, reopen)
				}
			}
			state, e := f.s.ReadBrokerGrantState(ctx)
			if e != nil || state.Allocated.Global.Runners != 1 || len(state.Grants) != 1 {
				t.Fatal("restart released or duplicated capacity", e, state.Allocated)
			}
		})
	}
}
