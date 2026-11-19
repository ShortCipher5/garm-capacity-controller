package brokerinput

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeartifacts"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/overheadrefresh"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func TestDomainOverheadRealLedgerPlanner(t *testing.T) {
	for _, dimension := range []string{"cpu", "ram", "disk", "artifact"} {
		t.Run(dimension, func(t *testing.T) {
			ctx := context.Background()
			domain := strings.Repeat("a", 64)
			execution := admission.ExecutionImage{Controller: "controller", Flavor: "small", Image: "image", Fingerprint: strings.Repeat("b", 64), Type: "container", Profiles: []string{"runner"}, Arch: "amd64"}
			policy, err := nativeapi.NewPolicy(nativeapi.PolicySpec{Version: nativeapi.PolicyVersion, Controller: "controller", Host: "host", DomainID: domain, NativeSHA256: strings.Repeat("c", 64), LauncherSHA256: strings.Repeat("d", 64), ServerVersion: nativeapi.NativeVersion, Pools: map[string]nativeapi.Pool{pool: {Flavor: execution.Flavor, Image: execution.Image, Fingerprint: execution.Fingerprint, Type: execution.Type, Arch: execution.Arch, ExpectedProfiles: execution.Profiles, StoragePool: "storage", Bridge: "bridge"}}})
			if err != nil {
				t.Fatal(err)
			}
			var desc nativeidentity.Descriptor
			var enforcement nativeenforcement.Descriptor
			var artifact nativeartifacts.Descriptor
			var pause atomic.Bool
			entered := make(chan struct{}, 1)
			native, controllerTLS, ownerTLS := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if pause.Load() {
					select {
					case entered <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					return
				}
				if r.URL.Path == nativeartifacts.Path {
					evidence := artifact
					evidence.Nonce = r.URL.Query().Get("nonce")
					evidence.IssuedAt = time.Now()
					json.NewEncoder(w).Encode(evidence)
					return
				}
				if r.URL.Path == nativeenforcement.Path {
					evidence := enforcement
					evidence.Nonce = r.URL.Query().Get("nonce")
					evidence.IssuedAt = time.Now()
					json.NewEncoder(w).Encode(evidence)
					return
				}
				if r.URL.Path != nativeidentity.Path {
					t.Error("unexpected native execution")
					w.WriteHeader(500)
					return
				}
				json.NewEncoder(w).Encode(desc)
			}))
			desc = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: policy.Digest(), OwnerServerLeaf: nativeidentity.Fingerprint(native.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(ownerTLS.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("e", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "private-native-domain", NativeServerVersion: "6.0.4"}
			nativeObserved, err := nativeidentity.Observe(ctx, nativeidentity.Configuration{Endpoint: native.URL, TLS: controllerTLS, OwnerServerLeaf: desc.OwnerServerLeaf, OwnerClientLeaf: desc.OwnerClientLeaf, PolicyDigest: desc.PolicyDigest, DomainID: domain})
			if err != nil {
				t.Fatal(err)
			}
			state, _, _ := ledgerFixture()
			path := filepath.Join(t.TempDir(), "ledger.db")
			if err = os.Chmod(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			identity := filepath.Join(filepath.Dir(path), "identity")
			if err = admission.InitializeSQLite(path, identity, state.Config); err != nil {
				t.Fatal(err)
			}
			store, err := admission.OpenRuntimeSQLite(path, identity)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			if err = store.ProfileUpsert(ctx, "small", 1000, 1000, 1000); err != nil {
				t.Fatal(err)
			}
			fence, err := store.BeginReconciliation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			nativeObserved, err = nativeidentity.Observe(ctx, nativeidentity.Configuration{Endpoint: native.URL, TLS: controllerTLS, OwnerServerLeaf: desc.OwnerServerLeaf, OwnerClientLeaf: desc.OwnerClientLeaf, PolicyDigest: desc.PolicyDigest, DomainID: domain})
			if err != nil {
				t.Fatal(err)
			}
			measured, err := admission.ObservedNativeHostBinding("host", nativeObserved)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.BindHostIdentities(ctx, fence, []admission.HostBinding{measured}); err != nil {
				t.Fatal(err)
			}
			binding, err := store.LookupHostBinding(ctx, "host")
			if err != nil {
				t.Fatal(err)
			}
			enrolled, err := store.EnrollNativeDomain(ctx, admission.NativeDomainSpec{Controller: "controller", Host: "host", HostBindingSequence: binding.Sequence, DomainID: domain, PolicyDigest: policy.Digest(), OwnerClientLeaf: desc.OwnerClientLeaf, OwnerServerLeaf: desc.OwnerServerLeaf}, nativeObserved)
			if err != nil {
				t.Fatal(err)
			}

			desired := state.Policy
			desired.Revision = 0
			if err = store.ReplaceBrokerPolicy(ctx, desired); err != nil {
				t.Fatal(err)
			}
			if err = store.CompleteReconciliation(ctx, fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host", ObservedAt: time.Now(), Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}}); err != nil {
				t.Fatal(err)
			}
			_, profiles, opt := ledgerFixture()
			opt.Now = time.Now()
			// The first real read has capacity. Each dimensional ceiling must then
			// independently suppress planning, not merely fail a later grant write.
			snap, saved, err := Ledger(ctx, store, profiles, opt)
			if err != nil {
				t.Fatal(err)
			}
			_, _, demand, _ := fixture()
			demand.StartedAt = opt.Now
			demand.FinishedAt = opt.Now
			demand.Jobs[0].CreatedAt = opt.Now
			before, err := Stock(snap, saved.Policy, demand, opt)
			if err != nil || len(before.Assignments) != 1 {
				t.Fatal("baseline", before, err)
			}
			limits := nativeenforcement.Limits{CPUMilli: 1, MemoryBytes: 1, DiskBytes: 1, Tasks: 128}
			switch dimension {
			case "cpu":
				limits.CPUMilli = uint64(state.Config.Global.CPU)
			case "ram":
				limits.MemoryBytes = uint64(state.Config.Global.RAM) << 20
			case "artifact":
				limits.DiskBytes = 1000 << 20
			case "disk":
				limits.DiskBytes = uint64(state.Config.Global.Disk) << 20
			}
			enforcement = nativeenforcement.Descriptor{Version: nativeenforcement.Version, Owner: desc, CapsuleBirthID: domain, RootImageSHA256: strings.Repeat("1", 64), DeviceProgramSHA256: strings.Repeat("2", 64), EgressPolicySHA256: strings.Repeat("3", 64), Limits: limits}
			enforcement.LimitsDigest, err = nativeenforcement.DigestLimits(limits)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := nativeenforcement.Observe(ctx, nativeenforcement.Configuration{Endpoint: native.URL, TLS: controllerTLS, Expected: desc})
			if err != nil {
				t.Fatal(err)
			}
			recorded, err := store.RecordNativeDomainOverhead(ctx, enrolled.Sequence, observed)
			if err != nil {
				t.Fatal(err)
			}
			if dimension == "artifact" {
				artifact = nativeartifacts.Descriptor{Version: nativeartifacts.Version, Owner: desc, ArtifactID: strings.Repeat("9", 64), PoolGUID: "101", PoolLoadGUID: "102", RootGUID: "103", Dataset: "pool/control-" + strings.Repeat("9", 64), QuotaBytes: 3000 << 20}
				proof, e := nativeartifacts.Observe(ctx, nativeartifacts.Configuration{Endpoint: native.URL, TLS: controllerTLS, Expected: desc})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = store.RecordNativeArtifactOverhead(ctx, enrolled.Sequence, proof); e != nil {
					t.Fatal(e)
				}
			}
			snap, saved, err = Ledger(ctx, store, profiles, opt)
			if err != nil {
				t.Fatal(err)
			}
			after, err := Stock(snap, saved.Policy, demand, opt)
			if err != nil || len(after.Assignments) != 0 || len(snap.Charges) != 0 || len(saved.DomainOverheads) != 1 || saved.Allocated.DomainOverhead != recorded.Resources {
				t.Fatal("actual ledger overhead did not constrain planner", after, saved.Allocated, err)
			}
			if dimension == "artifact" && (len(saved.ArtifactOverheads) != 1 || saved.Allocated.ArtifactOverhead.Disk != 3000) {
				t.Fatal("artifact subtotal omitted", saved.Allocated)
			}
			if snap.Pools[pool].Budget[0] != state.Config.Pools[pool].Budget.CPU || snap.Global[3] != state.Config.Global.Runners {
				t.Fatal("overhead became pool/runner charge")
			}
			// A separately authenticated host artifact root is populated through
			// the actual canonical transaction, then projected from one read.
			id := strings.Repeat("7", 64)
			quota := int64((1 << 20) + 1)
			if dimension == "artifact" {
				id = strings.Repeat("9", 64)
				quota = 3000 << 20
			}
			artifact = nativeartifacts.Descriptor{Version: nativeartifacts.Version, Owner: desc, ArtifactID: id, PoolGUID: "101", PoolLoadGUID: "102", RootGUID: "103", Dataset: "pool/control-" + id, QuotaBytes: quota}
			art, err := nativeartifacts.Observe(ctx, nativeartifacts.Configuration{Endpoint: native.URL, TLS: controllerTLS, Expected: desc})
			if err != nil {
				t.Fatal(err)
			}
			retained, err := store.RecordNativeArtifactOverhead(ctx, enrolled.Sequence, art)
			if err != nil {
				t.Fatal(err)
			}
			current, err := store.ReadBrokerGrantState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			projected, err := profilesnapshot.New("controller", strings.Repeat("8", 64), strings.Repeat("9", 64), current, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(projected)
			decoded, err := profilesnapshot.Decode(raw, projected.Controller, projected.Nonce, time.Now())
			if err != nil || decoded.Accounting.Allocated.ArtifactOverhead != retained.Resources || len(decoded.Accounting.ArtifactOverheads) != 1 || decoded.Accounting.ArtifactOverheads[0].Sequence != retained.Sequence || decoded.Accounting.Allocated.DomainOverhead != recorded.Resources {
				t.Fatal("actual artifact snapshot", err)
			}

			targets := []overheadrefresh.Target{{BindingSequence: enrolled.Sequence, Endpoint: native.URL, TLS: controllerTLS, Kind: "both"}}
			refreshed, e := overheadrefresh.Refresh(ctx, store, targets)
			if e != nil || len(refreshed) != 1 || refreshed[0].DomainSequence != recorded.Sequence || refreshed[0].ArtifactSequence != retained.Sequence {
				t.Fatal("actual collector replay", refreshed, e)
			}
			targets = append(targets, overheadrefresh.Target{BindingSequence: 999999, Endpoint: native.URL, TLS: controllerTLS, Kind: "domain"})
			partial, e := overheadrefresh.Refresh(ctx, store, targets)
			if e == nil || len(partial) != 1 || partial[0].DomainSequence != recorded.Sequence || partial[0].ArtifactSequence != retained.Sequence {
				t.Fatal("partial collector lost durable sequences", partial, e)
			}

			// Actual runtime-ledger + pinned owner + durable service path.
			directory := t.TempDir()
			if err = os.Chmod(directory, 0700); err != nil {
				t.Fatal(err)
			}
			recipes := []overheadrefresh.Recipe{{ID: "owner", TrustDigest: strings.Repeat("f", 64), Targets: []overheadrefresh.Target{{BindingSequence: enrolled.Sequence, Endpoint: native.URL, TLS: controllerTLS, Kind: "both"}}}}
			service, err := overheadrefresh.NewService(ctx, store, directory, recipes)
			if err != nil {
				t.Fatal(err)
			}
			view, err := service.Snapshot()
			if err != nil || len(view.Recipes) != 1 {
				t.Fatal(err)
			}
			requestID := strings.Repeat("a", 32)
			op, err := service.Start("owner", view.Recipes[0].Revision, requestID)
			if err != nil || op.State != "pending" {
				t.Fatal(op, err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				view, err = service.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if !view.Running {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("collector did not complete")
				}
				time.Sleep(time.Millisecond)
			}
			if len(view.Operations) != 1 || view.Operations[0].State != "complete" || len(view.Operations[0].Results) != 1 {
				t.Fatal(view)
			}
			pause.Store(true)
			nextID := strings.Repeat("b", 32)
			if _, err = service.Start("owner", view.Recipes[0].Revision, nextID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("actual owner read not started")
			}
			if _, err = service.Start("owner", view.Recipes[0].Revision, strings.Repeat("c", 32)); err == nil {
				t.Fatal("concurrent collection accepted")
			}
			service.Close()
			pause.Store(false)
			service, err = overheadrefresh.NewService(ctx, store, directory, recipes)
			if err != nil {
				t.Fatal(err)
			}
			op, err = service.Start("owner", view.Recipes[0].Revision, requestID)
			if err != nil || op.State != "complete" {
				t.Fatal("durable replay lost", op, err)
			}
			service.Close()

		})
	}
}
