package brokerinput

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func TestPublicGrantCycleRealEnrollmentTransportAndRestart(t *testing.T) {
	publicCycleTransport(t, false)
}
func TestManagedGrantCycleFirstObservedIdentityAndRestart(t *testing.T) {
	publicCycleTransport(t, true)
}
func publicCycleTransport(t *testing.T, managed bool) {
	controllerID := "controller"
	if managed {
		controllerID = "11111111-1111-4111-8111-111111111111"
	}

	ctx := context.Background()
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
	var drift atomic.Bool
	stock := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedAuth := "Bearer token"
		if managed {
			expectedAuth = ""
			if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
				t.Error("missing client certificate")
			}
			source := strings.Repeat("1", 64)
			if drift.Load() {
				source = strings.Repeat("2", 64)
			}
			w.Header().Set(managedsource.SourceHeader, source)
			w.Header().Set(managedsource.ControllerHeader, controllerID)
		}
		if r.Method != "GET" || r.Header.Get("Authorization") != expectedAuth {
			t.Error("unexpected mutation/auth")
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/api/v1/pools":
			json.NewEncoder(w).Encode([]any{poolRow})
		case "/api/v1/pools/" + pool:
			json.NewEncoder(w).Encode(poolRow)
		case "/api/v1/jobs":
			json.NewEncoder(w).Encode([]map[string]any{{"workflow_job_id": 1, "repo_id": repo, "status": "queued", "labels": []string{"linux"}, "created_at": time.Now().Add(-time.Minute), "locked_by": "00000000-0000-0000-0000-000000000000"}})
		case "/api/v1/instances":
			json.NewEncoder(w).Encode([]any{})
		default:
			t.Error("unexpected stock path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	if managed {
		stock.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAnyClientCert}
	}
	stock.StartTLS()
	defer stock.Close()
	roots := x509.NewCertPool()
	roots.AddCert(stock.Certificate())
	trust := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	identityConfig := stockidentity.Configuration{Endpoint: stock.URL, Token: "token", Provider: "capacity", Pool: pool, TLS: trust}
	serverPin := ""
	if managed {
		sum := sha256.Sum256(stock.Certificate().RawSubjectPublicKeyInfo)
		serverPin = hex.EncodeToString(sum[:])
		trust.Certificates = []tls.Certificate{stock.TLS.Certificates[0]}
		identityConfig.Token = ""
		identityConfig.ManagedServerKeyPin = serverPin
		identityConfig.ManagedControllerID = controllerID
	}
	observed, err := stockidentity.Observe(ctx, identityConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.RegisterBrokerPoolProfile(ctx, admission.BrokerPoolProfileRequest{SourceIdentity: observed.SourceIdentity(), SizeProfile: "small", Authority: admission.StockProviderAuthority{Controller: controllerID, PoolID: pool, ProviderName: "capacity", ClientFingerprint: strings.Repeat("f", 64)}, Execution: execution}, observed, map[int64]nativeapi.Policy{enrolled.Sequence: policy})
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
	var source *garmapi.DemandClient
	if managed {
		source, err = garmapi.NewManagedDemand(stock.URL, serverPin, controllerID, trust, []string{"capacity"}, []string{repo})
	} else {
		source, err = garmapi.NewDemand(stock.URL, "token", trust, []string{"capacity"}, []string{repo})
	}
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if managed && source.SourceIdentity() != "" {
		t.Fatal("configuration minted source")
	}

	opt := capacitybroker.Options{MaxAge: time.Minute, MaxAssignments: 100}
	first, err := GrantCycle(ctx, store, source, opt, time.Minute)
	if err != nil || len(first.Grants) != 1 {
		t.Fatalf("public first cycle %+v %v", first, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	next, err := GrantCycle(ctx, store, source, opt, time.Minute)
	if err != nil || len(next.Grants) != 0 || len(next.Plan.CoveredByGrant) != 1 {
		t.Fatalf("public restarted cycle %+v %v", next, err)
	}
	final, err := store.ReadBrokerGrantState(ctx)
	if err != nil || len(final.Grants) != 1 || final.Allocated.Global.Runners != 1 {
		t.Fatal("public cycle duplicate accounting", err)
	}
	service, err := NewService(store, source, opt, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	runctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- service.Run(runctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for service.Snapshot().SuccessCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := service.Snapshot()
	cancel()
	<-done
	if status.SuccessCount != 1 || status.AttemptCount != 1 || status.LastGrantCount != 0 || status.LastSuccessAt.IsZero() {
		t.Fatalf("actual service status %+v", status)
	}
	final, err = store.ReadBrokerGrantState(ctx)
	if err != nil || len(final.Grants) != 1 || final.Allocated.Global.Runners != 1 {
		t.Fatal("service duplicated grants", err)
	}

	if managed {
		drift.Store(true)
		if _, err = GrantCycle(ctx, store, source, opt, time.Minute); err == nil {
			t.Fatal("changed supervisor source accepted")
		}
		if source.SourceIdentity() != "" {
			t.Fatal("source drift did not revoke")
		}
		drift.Store(false)
		if _, err = GrantCycle(ctx, store, source, opt, time.Minute); err == nil {
			t.Fatal("source revocation reset")
		}
		final, err = store.ReadBrokerGrantState(ctx)
		if err != nil || len(final.Grants) != 1 || final.Allocated.Global.Runners != 1 {
			t.Fatal("source failure changed grants", err)
		}
	}

}
