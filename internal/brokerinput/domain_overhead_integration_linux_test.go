package brokerinput

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func TestDomainOverheadRealLedgerPlanner(t *testing.T) {
	for _, dimension := range []string{"cpu", "ram", "disk"} {
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
			native, controllerTLS, ownerTLS := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			store, err := admission.OpenSQLite(path, state.Config)
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
			snap, saved, err = Ledger(ctx, store, profiles, opt)
			if err != nil {
				t.Fatal(err)
			}
			after, err := Stock(snap, saved.Policy, demand, opt)
			if err != nil || len(after.Assignments) != 0 || len(snap.Charges) != 0 || len(saved.DomainOverheads) != 1 || saved.Allocated.DomainOverhead != recorded.Resources {
				t.Fatal("actual ledger overhead did not constrain planner", after, saved.Allocated, err)
			}
			if snap.Pools[pool].Budget[0] != state.Config.Pools[pool].Budget.CPU || snap.Global[3] != state.Config.Global.Runners {
				t.Fatal("overhead became pool/runner charge")
			}

		})
	}
}
