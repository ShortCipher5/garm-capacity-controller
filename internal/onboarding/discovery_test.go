package onboarding

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// discoveryFixture spins up a disposable mTLS Incus fixture. Only loopback
// fixture servers are ever contacted.
func discoveryFixture(t *testing.T, serve func(w http.ResponseWriter, r *http.Request, root map[string]any)) *incusapi.Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable-discovery-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	pair := func(serial int64, client bool) (tls.Certificate, []byte) {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "disposable-discovery-leaf"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		if client {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		} else {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, parsed, &private.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		privateDER, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: private}, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	}
	serverPair, _ := pair(2, false)
	clientPair, _ := pair(3, true)
	hash := sha256.Sum256(serverPair.Certificate[0])
	root := map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}, "environment": map[string]any{"server_clustered": false, "server_name": "private-fixture-daemon", "server_version": "6.0.4", "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverPair.Certificate[0]})), "certificate_fingerprint": fmt.Sprintf("%x", hash)}}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serve(w, r, root) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverPair}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := incusapi.New(server.URL, "runners", &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{clientPair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func writeDiscoverySync(w http.ResponseWriter, metadata any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status_code": 200, "metadata": metadata})
}

func standardDiscoveryResources() map[string]any {
	return map[string]any{
		"cpu":           map[string]any{"total": 8},
		"memory":        map[string]any{"total": 34359738368},
		"disk":          []any{map[string]any{"size": 549755813888, "usage": 1099511627776}},
		"storage_pools": map[string]any{"pool-a": map[string]any{"space": map[string]any{"total": 1099511627776, "used": 549755813888}}},
	}
}

func standardDiscoveryGuests() []any {
	return []any{
		map[string]any{"name": "runner-1", "status": "Running", "created_at": "2026-11-17T00:00:00Z", "expanded_config": map[string]any{"limits.cpu": "2", "limits.memory": "4GiB", "user.runner-controller-id": "ctl-1", "user.runner-pool-id": "pool-1", "user.garm-capacity.reservation": "res-1"}, "expanded_devices": map[string]any{"root": map[string]any{"size": "10GiB"}}},
		map[string]any{"name": "runner-2", "status": "Running", "created_at": "2026-11-17T00:00:01Z", "expanded_config": map[string]any{"limits.cpu": "4", "limits.memory": "8GiB", "user.runner-controller-id": "ctl-1", "user.runner-pool-id": "pool-2"}},
		map[string]any{"name": "foreign", "status": "Running", "created_at": "2026-11-17T00:00:02Z", "expanded_config": map[string]any{"limits.cpu": "1", "limits.memory": "1GiB"}},
	}
}

func standardDiscoveryStates() map[string]map[string]any {
	usage := func(cpu, mem, disk int64) map[string]any {
		return map[string]any{"cpu": map[string]any{"usage": cpu}, "memory": map[string]any{"usage": mem}, "disk": []any{map[string]any{"usage": map[string]any{"usage_bytes": disk}}}}
	}
	return map[string]map[string]any{
		"runner-1": usage(3000000000, 2147483648, 3221225472),
		"runner-2": usage(9000000000, 4294967296, 5368709120),
		"foreign":  usage(500000000, 536870912, 1073741824),
	}
}

// standardDiscoveryServer routes every discovery read; mutate closes over the
// maps to customize or break individual sections.
func standardDiscoveryServer(resources map[string]any, guests []any, states map[string]map[string]any) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, r *http.Request, root map[string]any) {
		switch r.URL.Path {
		case "/1.0":
			writeDiscoverySync(w, root)
		case "/1.0/resources":
			writeDiscoverySync(w, resources)
		case "/1.0/instances":
			writeDiscoverySync(w, guests)
		default:
			name, found := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/1.0/instances/"), "/state")
			if found {
				if state, ok := states[name]; ok {
					writeDiscoverySync(w, state)
					return
				}
			}
			http.NotFound(w, r)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestDiscoveryAggregatesTotalsGuestsUtilizationAndSuggestsBudget(t *testing.T) {
	ctx := context.Background()
	store := storeFixture(t)
	states := standardDiscoveryStates()
	serve := standardDiscoveryServer(standardDiscoveryResources(), standardDiscoveryGuests(), states)
	var rootRequests atomic.Int32
	client := discoveryFixture(t, func(w http.ResponseWriter, r *http.Request, root map[string]any) {
		if r.URL.Path == "/1.0" {
			rootRequests.Add(1)
		}
		serve(w, r, root)
	})
	pooled, err := client.WithStoragePool("pool-a")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, map[string]Observer{"candidate-b": pooled}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, store)
	result, err := manager.Discover(ctx, Request{requestID, "candidate-b", "full discovery sweep"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != "candidate-b" || result.Identity.ObservationAvailable != true || result.Identity.ServerVersion != "6.0.4" || result.Identity.BindingClass != "no_match_observed" {
		t.Fatalf("identity section: %+v", result.Identity)
	}
	if result.Totals == nil || !result.Totals.Complete || result.Totals.CPUMillis == nil || *result.Totals.CPUMillis != 8000 || result.Totals.RAMMiB == nil || *result.Totals.RAMMiB != 32768 || result.Totals.DiskMiB == nil || *result.Totals.DiskMiB != 524288 {
		t.Fatalf("totals section: %+v", result.Totals)
	}
	if result.Totals.StoragePool != "pool-a" || result.Totals.PoolDiskMiB == nil || *result.Totals.PoolDiskMiB != 1048576 {
		t.Fatalf("storage pool totals missing: %+v", result.Totals)
	}
	if len(result.Guests) != 3 {
		t.Fatalf("guests: %+v", result.Guests)
	}
	managed, unmanaged := 0, 0
	for _, guest := range result.Guests {
		if guest.Name == "foreign" {
			if guest.Managed || guest.Controller != "" {
				t.Fatal("foreign guest presented as managed")
			}
			unmanaged++
			continue
		}
		if !guest.Managed || guest.CPUMillis == nil || guest.RAMMiB == nil {
			t.Fatalf("managed guest missing attribution or bounds: %+v", guest)
		}
		managed++
	}
	if managed != 2 || unmanaged != 1 {
		t.Fatalf("managed/unmanaged classification: %d/%d", managed, unmanaged)
	}
	if !containsString(result.Warnings, "1 unmanaged guests: the shared limit may not cover them") {
		t.Fatalf("unmanaged warning missing: %v", result.Warnings)
	}
	if result.Utilization == nil || result.Utilization.Source != "guest-state" || result.Utilization.Guests != 3 || result.Utilization.CPUUsedMillis != 12500 || result.Utilization.RAMUsedMiB != 6656 || result.Utilization.DiskUsedMiB != 9216 {
		t.Fatalf("utilization section: %+v", result.Utilization)
	}
	if !containsString(result.Warnings, "guest-state cpu usage is cumulative since guest start; a rate needs consecutive samples") {
		t.Fatalf("cumulative cpu note missing: %v", result.Warnings)
	}
	if !containsString(result.Warnings, "point-in-time snapshot: observations go stale immediately and are not reconciliation evidence") {
		t.Fatalf("staleness warning missing: %v", result.Warnings)
	}
	if result.SuggestedBudget.CPU != 6000 || result.SuggestedBudget.RAM != 30720 || result.SuggestedBudget.Disk != 471860 || result.SuggestedBudget.Runners != 0 {
		t.Fatalf("suggested budget: %+v", result.SuggestedBudget)
	}
	if len(result.Blockers) != 1 || result.Blockers[0] != "physical_capacity_scope_unqualified" {
		t.Fatalf("blockers: %v", result.Blockers)
	}
	after := snapshot(t, store)
	unchangedExceptAudit(t, before, after)
	if len(after.Audit) != len(before.Audit)+2 || after.Audit[0].Kind != "onboarding_discovery_completed" || after.Audit[1].Kind != "onboarding_discovery_requested" {
		t.Fatalf("discovery audit pair missing: %v", after.Audit)
	}
	if rootRequests.Load() < 4 {
		t.Fatalf("expected fresh bounded reads, got %d", rootRequests.Load())
	}
	encoded, _ := json.Marshal(struct {
		Result DiscoveryResult
		Audit  []admission.AuditEvent
	}{result, after.Audit})
	identity, err := client.PhysicalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{identity.ServerName(), identity.ServerKeyFingerprint(), identity.OriginFingerprint(), identity.ScopeFingerprint(), "BEGIN CERTIFICATE"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("identity detail or certificate exposed")
		}
	}
}

func TestSuggestedBudgetHeadroomMath(t *testing.T) {
	for name, tc := range map[string]struct {
		totals incusapi.HostTotals
		want   admission.Resources
	}{
		"standard host":  {incusapi.HostTotals{CPUMillisTotal: 8000, RAMMiBTotal: 32768, DiskMiBTotal: 524288}, admission.Resources{CPU: 6000, RAM: 30720, Disk: 471860}},
		"small host":     {incusapi.HostTotals{CPUMillisTotal: 1000, RAMMiBTotal: 1024, DiskMiBTotal: 10000}, admission.Resources{CPU: 0, RAM: 0, Disk: 9000}},
		"zero totals":    {incusapi.HostTotals{}, admission.Resources{}},
		"tiny disk":      {incusapi.HostTotals{CPUMillisTotal: 4000, RAMMiBTotal: 8192, DiskMiBTotal: 10}, admission.Resources{CPU: 2000, RAM: 6144, Disk: 9}},
		"unknown disk":   {incusapi.HostTotals{CPUMillisTotal: 4000, RAMMiBTotal: 8192}, admission.Resources{CPU: 2000, RAM: 6144}},
		"exact headroom": {incusapi.HostTotals{CPUMillisTotal: 2000, RAMMiBTotal: 2048, DiskMiBTotal: 1}, admission.Resources{Disk: 1}},
		"negative clamp": {incusapi.HostTotals{CPUMillisTotal: 500, RAMMiBTotal: 100, DiskMiBTotal: 5}, admission.Resources{Disk: 5}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := SuggestedBudgetFromTotals(tc.totals); got != tc.want {
				t.Fatalf("suggestion: got %+v want %+v", got, tc.want)
			}
			if got := SuggestedBudgetFromTotals(tc.totals); got.CPU < 0 || got.RAM < 0 || got.Disk < 0 {
				t.Fatal("negative suggestion")
			}
		})
	}
}

func TestDiscoveryZeroTotalsAreUnknownNotConfirmedZeros(t *testing.T) {
	store := storeFixture(t)
	client := discoveryFixture(t, standardDiscoveryServer(map[string]any{}, nil, nil))
	manager, err := New(store, map[string]Observer{"candidate-b": client}, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Discover(context.Background(), Request{requestID, "candidate-b", "empty resources check"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Totals == nil || result.Totals.Complete {
		t.Fatalf("zero totals should render incomplete, not absent: %+v", result.Totals)
	}
	if result.Totals.CPUMillis != nil || result.Totals.RAMMiB != nil || result.Totals.DiskMiB != nil {
		t.Fatalf("unknown dimensions rendered as confirmed zeros: %+v", result.Totals)
	}
	if !containsString(result.Warnings, "physical totals incomplete: cpu, disk, memory unknown (detection incomplete)") {
		t.Fatalf("incomplete warning: %v", result.Warnings)
	}
	if result.SuggestedBudget != (admission.Resources{}) {
		t.Fatalf("suggestion from unknown totals: %+v", result.SuggestedBudget)
	}
	encoded, _ := json.Marshal(result.Totals)
	if strings.Contains(string(encoded), `"cpu_millis":0`) || strings.Contains(string(encoded), `"disk_mib":0`) {
		t.Fatal("unknown totals serialized as zeros")
	}
}

func TestDiscoveryMalformedGuestRowsFailClosedWithoutPartialGuests(t *testing.T) {
	for name, rows := range map[string][]any{
		"missing name":      {map[string]any{"status": "Running", "created_at": "2026-11-17T00:00:00Z"}},
		"invalid name":      {map[string]any{"name": "../escape", "status": "Running", "created_at": "2026-11-17T00:00:00Z"}},
		"invalid timestamp": {map[string]any{"name": "runner-1", "status": "Running", "created_at": "not-a-time"}},
		"duplicate name":    {map[string]any{"name": "runner-1", "status": "Running", "created_at": "2026-11-17T00:00:00Z"}, map[string]any{"name": "runner-1", "status": "Running", "created_at": "2026-11-17T00:00:01Z"}},
	} {
		t.Run(name, func(t *testing.T) {
			store := storeFixture(t)
			client := discoveryFixture(t, standardDiscoveryServer(standardDiscoveryResources(), rows, nil))
			manager, err := New(store, map[string]Observer{"candidate-b": client}, true)
			if err != nil {
				t.Fatal(err)
			}
			result, err := manager.Discover(context.Background(), Request{requestID, "candidate-b", "malformed inventory check"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Guests) != 0 || !containsString(result.Blockers, "guest_inventory_unavailable") {
				t.Fatalf("malformed rows leaked partial guests: %+v blockers %v", result.Guests, result.Blockers)
			}
		})
	}
}

func TestDiscoverySectionFailureDegradesToBlocker(t *testing.T) {
	store := storeFixture(t)
	states := standardDiscoveryStates()
	serve := standardDiscoveryServer(standardDiscoveryResources(), standardDiscoveryGuests(), states)
	client := discoveryFixture(t, func(w http.ResponseWriter, r *http.Request, root map[string]any) {
		if r.URL.Path == "/1.0/resources" {
			http.Error(w, "internal detail must not leak", http.StatusInternalServerError)
			return
		}
		serve(w, r, root)
	})
	manager, err := New(store, map[string]Observer{"candidate-b": client}, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Discover(context.Background(), Request{requestID, "candidate-b", "degraded totals check"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Totals != nil || result.SuggestedBudget != (admission.Resources{}) {
		t.Fatalf("unavailable totals fabricated: %+v %+v", result.Totals, result.SuggestedBudget)
	}
	if !containsString(result.Blockers, "host_totals_unavailable") || !containsString(result.Blockers, "physical_capacity_scope_unqualified") {
		t.Fatalf("blockers: %v", result.Blockers)
	}
	if result.Utilization == nil || result.Utilization.Guests != 3 || len(result.Guests) != 3 || !result.Identity.ObservationAvailable {
		t.Fatalf("healthy sections degraded with totals: %+v", result)
	}
}

type cannedDiscovery struct {
	identity      func(context.Context) (incusapi.PhysicalServerIdentity, error)
	totals        func(context.Context) (incusapi.HostTotals, error)
	guests        func(context.Context) ([]incusapi.Guest, error)
	utilization   func(context.Context) (incusapi.UtilizationSample, error)
	identityCalls atomic.Int32
}

func (c *cannedDiscovery) PhysicalIdentity(ctx context.Context) (incusapi.PhysicalServerIdentity, error) {
	c.identityCalls.Add(1)
	return c.identity(ctx)
}
func (c *cannedDiscovery) HostTotals(ctx context.Context) (incusapi.HostTotals, error) {
	return c.totals(ctx)
}
func (c *cannedDiscovery) Guests(ctx context.Context) ([]incusapi.Guest, error) {
	return c.guests(ctx)
}
func (c *cannedDiscovery) SampleUtilization(ctx context.Context) (incusapi.UtilizationSample, error) {
	return c.utilization(ctx)
}

func TestDiscoveryRejectsDisabledUnknownAndIdentityOnlySource(t *testing.T) {
	store := storeFixture(t)
	var calls atomic.Int32
	canned := &cannedDiscovery{
		identity: func(context.Context) (incusapi.PhysicalServerIdentity, error) {
			calls.Add(1)
			return incusapi.PhysicalServerIdentity{}, nil
		},
	}
	manager, err := New(store, map[string]Observer{"candidate-b": canned}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Discover(context.Background(), Request{requestID, "unknown", "full discovery sweep"}); !errors.Is(err, ErrDenied) || calls.Load() != 0 {
		t.Fatal("unknown profile contacted observer")
	}
	manager, err = New(store, map[string]Observer{"candidate-b": observerFunc(func(context.Context) (incusapi.PhysicalServerIdentity, error) {
		calls.Add(1)
		return incusapi.PhysicalServerIdentity{}, nil
	})}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Discover(context.Background(), Request{requestID, "candidate-b", "identity-only source"}); !errors.Is(err, ErrDenied) || calls.Load() != 0 {
		t.Fatal("identity-only observer allowed discovery")
	}
	disabled, err := New(store, map[string]Observer{"candidate-b": canned}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Discover(context.Background(), Request{requestID, "candidate-b", "must remain disabled"}); !errors.Is(err, ErrDisabled) || calls.Load() != 0 {
		t.Fatal("disabled manager allowed discovery")
	}
}

func TestLateIdentityObservationIsNotAvailable(t *testing.T) {
	store := storeFixture(t)
	fixture := testsupportIdentity(t)
	identity, err := fixture.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	canned := &cannedDiscovery{
		identity: func(ctx context.Context) (incusapi.PhysicalServerIdentity, error) {
			close(entered)
			<-ctx.Done()
			return identity, nil
		},
		totals:      func(context.Context) (incusapi.HostTotals, error) { return incusapi.HostTotals{}, nil },
		guests:      func(context.Context) ([]incusapi.Guest, error) { return nil, nil },
		utilization: func(context.Context) (incusapi.UtilizationSample, error) { return incusapi.UtilizationSample{}, nil },
	}
	manager, err := New(store, map[string]Observer{"candidate-b": canned}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var result DiscoveryResult
	go func() {
		var err error
		result, err = manager.Discover(ctx, Request{requestID, "candidate-b", "late identity check"})
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result.Identity.ObservationAvailable || result.Identity.ServerVersion != "" || result.Identity.BindingClass != "not_checked" {
		t.Fatalf("late identity presented as current: %+v", result.Identity)
	}
	if !containsString(result.Blockers, "authenticated_identity_unavailable") {
		t.Fatalf("blockers: %v", result.Blockers)
	}
	after := snapshot(t, store)
	unchangedExceptAudit(t, before, after)
	if len(after.Audit) != len(before.Audit)+2 {
		t.Fatal("lost request/completed discovery audit")
	}
}

// testsupportIdentity builds a disposable identity fixture without guest
// routes; only the identity observation is needed here.
func testsupportIdentity(t *testing.T) *incusapi.Client {
	t.Helper()
	return discoveryFixture(t, func(w http.ResponseWriter, r *http.Request, root map[string]any) {
		if r.URL.Path != "/1.0" {
			http.NotFound(w, r)
			return
		}
		writeDiscoverySync(w, root)
	})
}
