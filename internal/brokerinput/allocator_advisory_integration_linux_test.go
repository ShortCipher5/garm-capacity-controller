package brokerinput

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

// TestAllocatorAdvisoryReaderRealStoreWeightsCommitmentsRestart runs the
// observe-only allocator tick against a real SQLite ledger and a real local
// stock transport: weights come from the revisioned broker_policy document,
// advisory reads never write, persisted commitments are held before any
// further award, and a restart recomputes identical decisions.
func TestAllocatorAdvisoryReaderRealStoreWeightsCommitmentsRestart(t *testing.T) {
	ctx := context.Background()
	controllerID := "controller"
	domain := strings.Repeat("a", 64)
	execution := admission.ExecutionImage{Controller: controllerID, Flavor: "small", Image: "image", Fingerprint: strings.Repeat("b", 64), Type: "container", Profiles: []string{"runner"}, Arch: "amd64"}
	policy, err := nativeapi.NewPolicy(nativeapi.PolicySpec{Version: nativeapi.PolicyVersion, Controller: controllerID, Host: "host", DomainID: domain, NativeSHA256: strings.Repeat("c", 64), LauncherSHA256: strings.Repeat("d", 64), ServerVersion: nativeapi.NativeVersion, Pools: map[string]nativeapi.Pool{pool: {Flavor: execution.Flavor, Image: execution.Image, Fingerprint: execution.Fingerprint, Type: execution.Type, Arch: execution.Arch, ExpectedProfiles: execution.Profiles, StoragePool: "storage", Bridge: "bridge"}}})
	if err != nil {
		t.Fatal(err)
	}
	var desc nativeidentity.Descriptor
	native, controllerTLS, ownerTLS := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	enrolled, err := store.EnrollNativeDomain(ctx, admission.NativeDomainSpec{Controller: controllerID, Host: "host", HostBindingSequence: binding.Sequence, DomainID: domain, PolicyDigest: policy.Digest(), OwnerClientLeaf: desc.OwnerClientLeaf, OwnerServerLeaf: desc.OwnerServerLeaf}, nativeObserved)
	if err != nil {
		t.Fatal(err)
	}
	poolRow := map[string]any{"id": pool, "repo_id": repo, "provider_name": "capacity", "generation": 1, "max_runners": 4, "enabled": true, "tags": []map[string]string{{"name": "linux"}}, "flavor": "small", "image": "image", "os_arch": "amd64", "os_type": "linux"}
	stock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("unexpected mutation/auth")
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/api/v1/pools":
			json.NewEncoder(w).Encode([]map[string]any{poolRow})
		case "/api/v1/pools/" + pool:
			json.NewEncoder(w).Encode(poolRow)
		case "/api/v1/jobs":
			json.NewEncoder(w).Encode([]map[string]any{{"workflow_job_id": 1, "repo_id": repo, "status": "queued", "labels": []string{"linux"}, "created_at": time.Now().UTC().Add(-time.Minute), "locked_by": "00000000-0000-0000-0000-000000000000"}})
		case "/api/v1/instances":
			json.NewEncoder(w).Encode([]any{})
		default:
			t.Error("unexpected stock path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer stock.Close()
	roots := x509.NewCertPool()
	roots.AddCert(stock.Certificate())
	trust := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	observed, err := stockidentity.Observe(ctx, stockidentity.Configuration{Endpoint: stock.URL, Token: "token", Provider: "capacity", Pool: pool, TLS: trust})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RegisterBrokerPoolProfile(ctx, admission.BrokerPoolProfileRequest{SourceIdentity: observed.SourceIdentity(), SizeProfile: "small", Authority: admission.StockProviderAuthority{Controller: controllerID, PoolID: pool, ProviderName: "capacity", ClientFingerprint: strings.Repeat("f", 64)}, Execution: execution}, observed, map[int64]nativeapi.Policy{enrolled.Sequence: policy}); err != nil {
		t.Fatal(err)
	}
	desired := state.Policy
	desired.Revision = 0
	desired.Repositories[0].Weight = 4
	if err = store.ReplaceBrokerPolicy(ctx, desired); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteReconciliation(ctx, fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host", ObservedAt: time.Now(), Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}}); err != nil {
		t.Fatal(err)
	}
	source, err := garmapi.NewDemand(stock.URL, "token", trust, []string{"capacity"}, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader, err := NewAdvisoryReaderWithAllocatorProposal(store, source, false, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadBrokerGrantState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reader.ReadPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Allocator == nil || len(plan.Allocator.Awards) != 1 || plan.Allocator.Awards[0].Reason != allocatorReasonQueuedDemand ||
		plan.Allocator.Awards[0].DestinationEstablished {
		t.Fatalf("allocator section missing or wrong: %+v", plan.Allocator)
	}
	if got := plan.Allocator.Decisions; len(got) != 1 || got[0].RepositoryWeight != 4 {
		t.Fatalf("weights not read from broker_policy document: %+v", got)
	}
	if plan.Allocator.Held.Commitments != 0 || plan.Allocator.Held.Global.Runners != 0 {
		t.Fatalf("held not empty before commitments: %+v", plan.Allocator.Held)
	}
	after, err := store.ReadBrokerGrantState(ctx)
	if err != nil || after.Sequence != before.Sequence || len(after.Grants) != 0 {
		t.Fatal("observe-only allocator tick wrote ledger", err)
	}

	// Measured usage vetoes the increase through the real read path.
	highAt := time.Now().UTC().Truncate(time.Second).Add(-pressureAge + 90*time.Second)
	if err = store.RecordUsageSample(ctx, "host", highAt, before.Config.Hosts["host"].Budget.CPU, 0, 0, "guest-state"); err != nil {
		t.Fatal(err)
	}
	suppressed, err := reader.ReadPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(suppressed.Allocator.Awards) != 0 {
		t.Fatalf("vetoed host funded an increase: %+v", suppressed.Allocator.Awards)
	}
	if got := suppressed.Allocator.Decisions[0]; got.Reason != allocatorDecisionBlockedPressure {
		t.Fatalf("pressure decision: %+v", got)
	}
	found := false
	for _, risk := range suppressed.Allocator.Risks {
		if risk == allocatorRiskHostPressureSuppressed {
			found = true
		}
	}
	if !found {
		t.Fatalf("pressure risk missing: %+v", suppressed.Allocator.Risks)
	}

	// A persisted commitment is held before any further award: the queued job
	// is covered by the outstanding grant, not double-funded.
	cycle, err := GrantCycle(ctx, store, source, capacitybroker.Options{MaxAge: time.Minute, MaxAssignments: 100}, time.Minute)
	if err != nil || len(cycle.Grants) != 1 {
		t.Fatalf("fixture commitment not persisted: %+v %v", cycle, err)
	}
	held, err := reader.ReadPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(held.Allocator.Awards) != 0 || held.Allocator.Held.Commitments != 1 || held.Allocator.Held.Global.Runners != 1 {
		t.Fatalf("commitment not held: %+v", held.Allocator)
	}
	if got := held.Allocator.Decisions[0]; got.CoveredByGrant != 1 || got.Reason != allocatorDecisionCovered {
		t.Fatalf("commitment coverage: %+v", got)
	}
	final, err := store.ReadBrokerGrantState(ctx)
	if err != nil || len(final.Grants) != 1 || final.Allocated.Global.Runners != 1 {
		t.Fatal("commitment accounting wrong", err)
	}

	// Restart: the durable ledger alone recomputes identical decisions.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewAdvisoryReaderWithAllocatorProposal(store, source, false, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.ReadPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Allocator.Awards, held.Allocator.Awards) ||
		!reflect.DeepEqual(again.Allocator.Decisions, held.Allocator.Decisions) ||
		!reflect.DeepEqual(again.Allocator.Held, held.Allocator.Held) {
		t.Fatalf("restart changed decisions:\n%+v\n%+v", again.Allocator, held.Allocator)
	}
}
