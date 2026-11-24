package brokerinput

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

const (
	scRepoA = "11111111-1111-4111-8111-111111111111"
	scRepoB = "22222222-2222-4222-8222-222222222222"
	scRepoC = "33333333-3333-4333-8333-333333333333"
	scPoolA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	scPoolB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	scPoolC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

var scSize = admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1}

func scAuthority(pool string) admission.StockProviderAuthority {
	return admission.StockProviderAuthority{Controller: "controller", PoolID: pool, ProviderName: "capacity", ClientFingerprint: strings.Repeat("f", 64)}
}

func scExecution() admission.ExecutionImage {
	return admission.ExecutionImage{Controller: "controller", Flavor: "small", Image: "image", Fingerprint: strings.Repeat("b", 64), Type: "container", Profiles: []string{"runner"}, Arch: "amd64"}
}

// sharedCapacityFixture builds one coherent three-repository ledger revision:
// one host with a six-slot budget, three enrolled pools with the same
// effective runner profile, revisioned policy and host bindings.
func sharedCapacityFixture(t *testing.T) (admission.BrokerGrantState, map[string]LedgerProfile, capacitybroker.Options) {
	t.Helper()
	now := time.Unix(1700000000, 0).UTC()
	total := admission.Resources{CPU: 6000, RAM: 6000, Disk: 6000, Runners: 6}
	repos := []string{scRepoA, scRepoB, scRepoC}
	pools := []string{scPoolA, scPoolB, scPoolC}
	state := admission.BrokerGrantState{
		Ready: true, Sequence: 41,
		Config: admission.Config{Global: total, MaxObservationAge: time.Minute,
			Hosts: map[string]admission.Host{"host": {Budget: total, Healthy: true, Mode: "enabled", ObservedAt: now}},
			Pools: map[string]admission.Pool{}},
		HostBindings: map[string]admission.HostBinding{"host": {Host: "host", Sequence: 9}},
		PoolProfiles: map[string]admission.BrokerPoolProfile{},
	}
	profiles := map[string]LedgerProfile{}
	policy := admission.BrokerPolicy{Revision: 3}
	for i := range repos {
		policy.Repositories = append(policy.Repositories, admission.BrokerRepositoryPolicy{ID: repos[i], Weight: 1, MaxRunners: 4, Pools: []admission.BrokerPoolPolicy{{ID: pools[i], MaxRunners: 4}}})
		state.Config.Pools[pools[i]] = admission.Pool{Budget: total, Hosts: []string{"host"}}
		state.PoolProfiles[pools[i]] = admission.BrokerPoolProfile{Sequence: int64(60 + i), Pool: pools[i], Repository: repos[i], SourceIdentity: "source", Authority: scAuthority(pools[i]), Resources: scSize, Execution: scExecution()}
		profiles[pools[i]] = LedgerProfile{Repository: repos[i], Size: scSize, Authority: scAuthority(pools[i]), Execution: scExecution()}
	}
	state.Policy = policy
	return state, profiles, capacitybroker.Options{Now: now, MaxAge: time.Minute, MaxAssignments: 100}
}

func scCharge(id, pool string, n int64) []admission.BrokerPlanningCharge {
	out := []admission.BrokerPlanningCharge{}
	for i := int64(0); i < n; i++ {
		out = append(out, admission.BrokerPlanningCharge{ID: fmt.Sprintf("%s:%d", id, i), Host: "host", Pool: pool, Resources: scSize})
	}
	return out
}

func scDemand(pools ...string) map[string]SharedCapacityDemand {
	out := map[string]SharedCapacityDemand{}
	for _, p := range pools {
		out[p] = SharedCapacityDemand{Waiting: 1, OldestWaiting: time.Unix(1700000000, 0).UTC().Add(-time.Minute)}
	}
	return out
}

func TestSharedCapacityOneRemainingSlotThreeRepos(t *testing.T) {
	state, profiles, opt := sharedCapacityFixture(t)
	charges := append(scCharge("reservation:a", scPoolA, 2), scCharge("reservation:b", scPoolB, 2)...)
	charges = append(charges, scCharge("reservation:c", scPoolC, 1)...)
	decision, err := EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolA, scPoolB, scPoolC), opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Five charged slots leave exactly one: at most one distinct commitment
	// across all reconciliations, and the losers see an explicit exhausted
	// reason rather than silently reused headroom.
	if len(decision.Plan.Assignments) != 1 || len(decision.Batch.Grants) != 1 {
		t.Fatalf("one slot produced %d assignments: %+v", len(decision.Plan.Assignments), decision.Plan.Assignments)
	}
	if decision.Remaining.Global[3] != 1 {
		t.Fatalf("remaining headroom wrong: %+v", decision.Remaining.Global)
	}
	winner := decision.Plan.Assignments[0].Pool
	refused := map[string]string{}
	for _, r := range decision.Refusals {
		if r.Kind == "pool" {
			refused[r.Scope] = r.Reason
		}
	}
	if len(refused) != 2 || refused[scPoolA] != SharedCapacityExhausted && refused[scPoolB] != SharedCapacityExhausted && refused[scPoolC] != SharedCapacityExhausted {
		t.Fatalf("unserved demand must show exhausted: %+v (winner %s)", decision.Refusals, winner)
	}
	for pool := range refused {
		if pool == winner {
			t.Fatal("winner also refused")
		}
	}
	// The commitment is bound to the exact eligible host binding and the
	// enrolled effective profile revision.
	grant := decision.Batch.Grants[0]
	if grant.Host != "host" || grant.HostBindingSequence != 9 || grant.Resources != scSize || grant.Authority != scAuthority(winner) {
		t.Fatalf("grant not bound to host/profile: %+v", grant)
	}
}

func TestSharedCapacityDeterministicAcrossInputOrder(t *testing.T) {
	state, profiles, opt := sharedCapacityFixture(t)
	charges := append(scCharge("reservation:a", scPoolA, 2), scCharge("reservation:b", scPoolB, 2)...)
	charges = append(charges, scCharge("reservation:c", scPoolC, 1)...)
	demand := scDemand(scPoolA, scPoolB, scPoolC)
	first, err := EvaluateSharedCapacity(state, charges, profiles, demand, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// The ledger serves charges in canonical sorted order; reversing the
	// caller's slice must not change the decision.
	reversed := append([]admission.BrokerPlanningCharge(nil), charges...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	second, err := EvaluateSharedCapacity(state, reversed, profiles, demand, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if first.Batch.ID != second.Batch.ID || len(first.Plan.Assignments) != len(second.Plan.Assignments) ||
		first.Plan.Assignments[0] != second.Plan.Assignments[0] {
		t.Fatalf("input order changed the decision: %+v vs %+v", first.Plan.Assignments, second.Plan.Assignments)
	}
}

func TestSharedCapacityWeightsChooseBetweenSafeCandidatesOnly(t *testing.T) {
	state, profiles, opt := sharedCapacityFixture(t)
	total := admission.Resources{CPU: 3000, RAM: 3000, Disk: 3000, Runners: 3}
	state.Config.Global = total
	host := state.Config.Hosts["host"]
	host.Budget = total
	state.Config.Hosts["host"] = host
	for _, pool := range []string{scPoolA, scPoolB, scPoolC} {
		p := state.Config.Pools[pool]
		p.Budget = total
		state.Config.Pools[pool] = p
	}
	// Weight 3 versus weight 1 with equal demand and three slots: the higher
	// weight receives more opportunity, never more than the cap allows.
	policy := state.Policy
	policy.Repositories[0].Weight = 3
	state.Policy = policy
	demand := map[string]SharedCapacityDemand{
		scPoolA: {Waiting: 2, OldestWaiting: opt.Now.Add(-time.Minute)},
		scPoolB: {Waiting: 2, OldestWaiting: opt.Now.Add(-time.Minute)},
	}
	decision, err := EvaluateSharedCapacity(state, nil, profiles, demand, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, a := range decision.Plan.Assignments {
		counts[a.Pool]++
	}
	if counts[scPoolA] != 2 || counts[scPoolB] != 1 || len(decision.Plan.Assignments) != 3 {
		t.Fatalf("weighted split wrong: %+v", counts)
	}
	// Weights never override the cap: with one slot the higher weight wins
	// the single commitment and the cap still holds.
	state.Config.Global = admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1}
	host.Budget = state.Config.Global
	state.Config.Hosts["host"] = host
	for _, pool := range []string{scPoolA, scPoolB, scPoolC} {
		p := state.Config.Pools[pool]
		p.Budget = state.Config.Global
		state.Config.Pools[pool] = p
	}
	decision, err = EvaluateSharedCapacity(state, nil, profiles, demand, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Plan.Assignments) != 1 || decision.Plan.Assignments[0].Pool != scPoolA {
		t.Fatalf("cap override or wrong winner: %+v", decision.Plan.Assignments)
	}
}

func TestSharedCapacityWarmIdleFloorWithinLimits(t *testing.T) {
	state, profiles, opt := sharedCapacityFixture(t)
	policy := state.Policy
	policy.Repositories[0].Pools[0].WarmIdle = 1
	state.Policy = policy
	// A configured positive idle floor is real demand for safe headroom.
	decision, err := EvaluateSharedCapacity(state, nil, profiles, nil, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Plan.Assignments) != 1 || !decision.Plan.Assignments[0].Warm || decision.Plan.Assignments[0].Pool != scPoolA {
		t.Fatalf("idle floor not served: %+v", decision.Plan.Assignments)
	}
	// An exhausted budget serves no floor and says so; a floor is not a
	// guarantee when the budget is gone.
	charges := append(scCharge("reservation:a", scPoolA, 2), scCharge("reservation:b", scPoolB, 2)...)
	charges = append(charges, scCharge("reservation:c", scPoolC, 2)...)
	decision, err = EvaluateSharedCapacity(state, charges, profiles, nil, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Plan.Assignments) != 0 {
		t.Fatalf("exhausted budget served a floor: %+v", decision.Plan.Assignments)
	}
	found := false
	for _, r := range decision.Refusals {
		if r.Kind == "pool" && r.Scope == scPoolA && r.Reason == SharedCapacityExhausted {
			found = true
		}
	}
	if !found {
		t.Fatalf("unmet floor invisible: %+v", decision.Refusals)
	}
	// Queued work outranks the optional floor for the last slot.
	charges = charges[:5]
	decision, err = EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolB), opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Plan.Assignments) != 1 || decision.Plan.Assignments[0].Warm || decision.Plan.Assignments[0].Pool != scPoolB {
		t.Fatalf("queued work must outrank the floor: %+v", decision.Plan.Assignments)
	}
}

func TestSharedCapacityPendingAndUnresolvedWorkStaysCharged(t *testing.T) {
	state, profiles, opt := sharedCapacityFixture(t)
	// An unused durable grant is pending work: it is charged and suppresses
	// new commitments exactly like a running reservation.
	grant := admission.BrokerGrant{ID: strings.Repeat("9", 64), State: "unused", Spec: admission.BrokerGrantSpec{
		Authority: scAuthority(scPoolC), Host: "host", HostBindingSequence: 9, Resources: scSize, Execution: scExecution(), ExpiresAt: opt.Now.Add(time.Hour)}}
	state.Grants = []admission.BrokerGrant{grant}
	charges := append(scCharge("reservation:a", scPoolA, 2), scCharge("reservation:b", scPoolB, 2)...)
	charges = append(charges, scCharge("reservation:c", scPoolC, 1)...)
	charges = append(charges, admission.BrokerPlanningCharge{ID: "grant:" + grant.ID, Host: "host", Pool: scPoolC, Resources: scSize})
	decision, err := EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolA), opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Plan.Assignments) != 0 || decision.Remaining.Global[3] != 0 {
		t.Fatalf("pending grant was double-spent: %+v", decision.Remaining.Global)
	}
	// A lowered configured maximum never funds growth: the same charges with
	// reduced pool maxima still refuse.
	policy := state.Policy
	for i := range policy.Repositories {
		policy.Repositories[i].MaxRunners = 1
		policy.Repositories[i].Pools[0].MaxRunners = 1
	}
	state.Policy = policy
	decision, err = EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolA), opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Plan.Assignments) != 0 {
		t.Fatalf("lowered settings erased retained charges: %+v", decision.Plan.Assignments)
	}
}

func TestSharedCapacityOverageRefusesGrowthVisibly(t *testing.T) {
	state, profiles, opt := sharedCapacityFixture(t)
	charges := append(scCharge("reservation:a", scPoolA, 3), scCharge("reservation:b", scPoolB, 3)...)
	charges = append(charges, scCharge("reservation:c", scPoolC, 1)...)
	decision, err := EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolA, scPoolB), opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Plan.Assignments) != 0 {
		t.Fatalf("overdrawn budget admitted growth: %+v", decision.Plan.Assignments)
	}
	overage, exhausted := false, false
	for _, r := range decision.Refusals {
		if r.Kind == "global" && r.Reason == SharedCapacityOverage {
			overage = true
		}
		if r.Kind == "pool" && r.Reason == SharedCapacityOverage {
			exhausted = true
		}
	}
	if !overage || !exhausted {
		t.Fatalf("drifted overage invisible: %+v", decision.Refusals)
	}
}

func TestSharedCapacityRejectsStaleForeignAndMismatchedInput(t *testing.T) {
	for name, mutate := range map[string]func(*admission.BrokerGrantState, map[string]LedgerProfile, map[string]SharedCapacityDemand){
		"stale host observation": func(s *admission.BrokerGrantState, _ map[string]LedgerProfile, _ map[string]SharedCapacityDemand) {
			h := s.Config.Hosts["host"]
			h.ObservedAt = h.ObservedAt.Add(-2 * time.Minute)
			s.Config.Hosts["host"] = h
		},
		"unknown demand pool": func(_ *admission.BrokerGrantState, _ map[string]LedgerProfile, d map[string]SharedCapacityDemand) {
			d["dddddddd-dddd-4ddd-8ddd-dddddddddddd"] = SharedCapacityDemand{Waiting: 1, OldestWaiting: time.Unix(1700000000, 0).UTC()}
		},
		"authority mismatch": func(s *admission.BrokerGrantState, _ map[string]LedgerProfile, _ map[string]SharedCapacityDemand) {
			p := s.PoolProfiles[scPoolA]
			p.Authority.ClientFingerprint = strings.Repeat("0", 64)
			s.PoolProfiles[scPoolA] = p
		},
		"missing enrolled profile": func(s *admission.BrokerGrantState, _ map[string]LedgerProfile, _ map[string]SharedCapacityDemand) {
			delete(s.PoolProfiles, scPoolA)
		},
		"demand without oldest": func(_ *admission.BrokerGrantState, _ map[string]LedgerProfile, d map[string]SharedCapacityDemand) {
			d[scPoolA] = SharedCapacityDemand{Waiting: 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			state, profiles, opt := sharedCapacityFixture(t)
			demand := scDemand(scPoolA)
			mutate(&state, profiles, demand)
			if _, err := EvaluateSharedCapacity(state, nil, profiles, demand, opt, time.Hour); !errors.Is(err, capacitybroker.ErrSnapshot) {
				t.Fatal("incoherent input admitted", err)
			}
		})
	}
	state, profiles, opt := sharedCapacityFixture(t)
	if _, err := EvaluateSharedCapacity(state, nil, profiles, scDemand(scPoolA), opt, time.Second); !errors.Is(err, capacitybroker.ErrSnapshot) {
		t.Fatal("out-of-bounds ttl admitted", err)
	}
}

func TestSharedCapacityExactBoundaryFit(t *testing.T) {
	state, profiles, opt := sharedCapacityFixture(t)
	charges := append(scCharge("reservation:a", scPoolA, 2), scCharge("reservation:b", scPoolB, 2)...)
	charges = append(charges, scCharge("reservation:c", scPoolC, 1)...)
	// Exactly one runner footprint remains: it fits, once.
	decision, err := EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolA), opt, time.Hour)
	if err != nil || len(decision.Plan.Assignments) != 1 {
		t.Fatalf("exact fit refused: %+v %v", decision.Plan.Assignments, err)
	}
	// One millicpu short: the full footprint does not fit and nothing is
	// partially committed.
	charges = append(charges, admission.BrokerPlanningCharge{ID: "reservation:cpu", Host: "host", Pool: scPoolC, Resources: admission.Resources{CPU: 1}})
	decision, err = EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolA), opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Plan.Assignments) != 0 {
		t.Fatalf("partial footprint committed: %+v", decision.Plan.Assignments)
	}
}

// sharedCapacityStoreFixture builds a real SQLite ledger with the full
// enrollment ceremony: native domain, host binding, revisioned policy and
// three enrolled pool profiles observed through a real local stock
// transport, plus three already-issued durable grants. Exactly one slot of
// headroom remains. Observations use the real clock because the store
// enforces freshness against it.
func sharedCapacityStoreFixture(t *testing.T) (*admission.SQLiteStore, string, map[string]LedgerProfile, capacitybroker.Options) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	controllerID := "controller"
	domain := strings.Repeat("a", 64)
	repos := []string{scRepoA, scRepoB, scRepoC}
	pools := []string{scPoolA, scPoolB, scPoolC}
	policyPools := map[string]nativeapi.Pool{}
	for _, pool := range pools {
		policyPools[pool] = nativeapi.Pool{Flavor: "small", Image: "image", Fingerprint: strings.Repeat("b", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge"}
	}
	nativePolicy, err := nativeapi.NewPolicy(nativeapi.PolicySpec{Version: nativeapi.PolicyVersion, Controller: controllerID, Host: "host", DomainID: domain, NativeSHA256: strings.Repeat("c", 64), LauncherSHA256: strings.Repeat("d", 64), ServerVersion: nativeapi.NativeVersion, Pools: policyPools})
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
	defer native.Close()
	desc = nativeidentity.Descriptor{DomainID: domain, PolicyDigest: nativePolicy.Digest(), OwnerServerLeaf: nativeidentity.Fingerprint(native.TLS.Certificates[0].Certificate[0]), OwnerClientLeaf: nativeidentity.Fingerprint(ownerTLS.Certificates[0].Certificate[0]), NativeServerKey: strings.Repeat("e", 64), NativeOrigin: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain)), NativeScope: nativeidentity.Fingerprint([]byte("garm-owned-domain/v1\x00" + domain + "\x00default")), NativeServerName: "private-native-domain", NativeServerVersion: "6.0.4"}
	observeNative := func() nativeidentity.Observation {
		t.Helper()
		observed, err := nativeidentity.Observe(ctx, nativeidentity.Configuration{Endpoint: native.URL, TLS: controllerTLS, OwnerServerLeaf: desc.OwnerServerLeaf, OwnerClientLeaf: desc.OwnerClientLeaf, PolicyDigest: desc.PolicyDigest, DomainID: domain})
		if err != nil {
			t.Fatal(err)
		}
		return observed
	}
	total := admission.Resources{CPU: 4000, RAM: 4000, Disk: 4000, Runners: 4}
	config := admission.Config{Global: total, MaxObservationAge: time.Minute,
		Hosts: map[string]admission.Host{"host": {Budget: total, Healthy: true, Mode: "enabled", ObservedAt: now}},
		Pools: map[string]admission.Pool{}}
	policy := admission.BrokerPolicy{Revision: 0}
	profiles := map[string]LedgerProfile{}
	poolRows := []map[string]any{}
	for i := range repos {
		policy.Repositories = append(policy.Repositories, admission.BrokerRepositoryPolicy{ID: repos[i], Weight: 1, MaxRunners: 4, Pools: []admission.BrokerPoolPolicy{{ID: pools[i], MaxRunners: 4}}})
		config.Pools[pools[i]] = admission.Pool{Budget: total, Hosts: []string{"host"}}
		profiles[pools[i]] = LedgerProfile{Repository: repos[i], Size: scSize, Authority: scAuthority(pools[i]), Execution: scExecution()}
		poolRows = append(poolRows, map[string]any{"id": pools[i], "repo_id": repos[i], "provider_name": "capacity", "generation": 1, "max_runners": 4, "enabled": true, "tags": []map[string]string{{"name": "linux"}}, "flavor": "small", "image": "image", "os_arch": "amd64", "os_type": "linux"})
	}
	path := filepath.Join(t.TempDir(), "ledger.db")
	store, err := admission.OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.ProfileUpsert(ctx, "small", 1000, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	measured, err := admission.ObservedNativeHostBinding("host", observeNative())
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
	enrolled, err := store.EnrollNativeDomain(ctx, admission.NativeDomainSpec{Controller: controllerID, Host: "host", HostBindingSequence: binding.Sequence, DomainID: domain, PolicyDigest: nativePolicy.Digest(), OwnerClientLeaf: desc.OwnerClientLeaf, OwnerServerLeaf: desc.OwnerServerLeaf}, observeNative())
	if err != nil {
		t.Fatal(err)
	}
	stock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("unexpected mutation/auth")
			w.WriteHeader(500)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/pools":
			json.NewEncoder(w).Encode(poolRows)
		case strings.HasPrefix(r.URL.Path, "/api/v1/pools/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/pools/")
			for _, row := range poolRows {
				if row["id"] == id {
					json.NewEncoder(w).Encode(row)
					return
				}
			}
			w.WriteHeader(404)
		case r.URL.Path == "/api/v1/jobs":
			json.NewEncoder(w).Encode([]any{})
		case r.URL.Path == "/api/v1/instances":
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
	for _, pool := range pools {
		observed, err := stockidentity.Observe(ctx, stockidentity.Configuration{Endpoint: stock.URL, Token: "token", Provider: "capacity", Pool: pool, TLS: trust})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.RegisterBrokerPoolProfile(ctx, admission.BrokerPoolProfileRequest{SourceIdentity: observed.SourceIdentity(), SizeProfile: "small", Authority: scAuthority(pool), Execution: scExecution()}, observed, map[int64]nativeapi.Policy{enrolled.Sequence: nativePolicy}); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.ReplaceBrokerPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteReconciliation(ctx, fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host", ObservedAt: time.Now().UTC(), Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}}); err != nil {
		t.Fatal(err)
	}
	state, err := store.ReadBrokerGrantState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Three durable grants are already committed: one slot remains.
	grants := make([]admission.BrokerGrantSpec, 0, 3)
	for _, pool := range pools {
		grants = append(grants, admission.BrokerGrantSpec{Authority: scAuthority(pool), Host: "host", HostBindingSequence: binding.Sequence, Resources: scSize, Execution: scExecution(), ExpiresAt: now.Add(time.Hour)})
	}
	batchID := sha256.Sum256([]byte("shared-capacity-fixture-grants"))
	if _, err := store.IssueBrokerGrants(ctx, admission.BrokerGrantBatch{ID: hex.EncodeToString(batchID[:]), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: grants}); err != nil {
		t.Fatal("issue grants:", err)
	}
	opt := capacitybroker.Options{Now: time.Now().UTC(), MaxAge: time.Minute, MaxAssignments: 100}
	return store, path, profiles, opt
}

func TestSharedCapacityCommitContentionAllocatesOnce(t *testing.T) {
	store, _, profiles, opt := sharedCapacityStoreFixture(t)
	ctx := context.Background()
	read := func() (admission.BrokerGrantState, []admission.BrokerPlanningCharge) {
		t.Helper()
		state, charges, err := store.ReadBrokerPlanningState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return state, charges
	}
	state, charges := read()
	if len(charges) != 3 {
		t.Fatalf("fixture charges: %+v", charges)
	}
	preState, preCharges := state, charges
	demand := scDemand(scPoolA, scPoolB, scPoolC)
	decision, err := EvaluateSharedCapacity(state, charges, profiles, demand, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Batch.Grants) != 1 {
		t.Fatalf("one remaining slot produced %d commitments", len(decision.Batch.Grants))
	}
	// Two overlapping cycles committing the identical decision replay
	// idempotently: exactly one new charge exists afterwards.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = CommitSharedCapacity(ctx, store, decision)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal("identical decision replay conflicted", err)
		}
	}
	state, charges = read()
	if len(charges) != 4 {
		t.Fatalf("identical decisions double-spent: %d charges", len(charges))
	}
	// Re-evaluation from the fresh revision finds no headroom and commits
	// nothing: the slot was not allocated twice across reconciliations.
	replayed, err := EvaluateSharedCapacity(state, charges, profiles, demand, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Batch.Grants) != 0 {
		t.Fatalf("exhausted budget produced commitments: %+v", replayed.Batch.Grants)
	}
	if _, err := CommitSharedCapacity(ctx, store, replayed); err != nil {
		t.Fatal(err)
	}
	// A genuinely different decision built from the pre-commit revision
	// loses the CAS instead of spending the same slot again.
	competing, err := EvaluateSharedCapacity(preState, preCharges, profiles, map[string]SharedCapacityDemand{scPoolB: {Waiting: 1, OldestWaiting: opt.Now.Add(-time.Minute)}}, opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(competing.Batch.Grants) != 1 || competing.Batch.ID == decision.Batch.ID {
		t.Fatalf("competing decision not distinct: %+v", competing.Batch)
	}
	if _, err := CommitSharedCapacity(ctx, store, competing); !errors.Is(err, admission.ErrBrokerConflict) {
		t.Fatal("stale competing decision committed", err)
	}
	state, charges = read()
	if len(charges) != 4 {
		t.Fatalf("conflicting decision changed accounting: %d charges", len(charges))
	}
}

func TestSharedCapacityRestartRetainsCommitments(t *testing.T) {
	store, path, profiles, opt := sharedCapacityStoreFixture(t)
	ctx := context.Background()
	state, charges, err := store.ReadBrokerPlanningState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolA, scPoolB, scPoolC), opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Batch.Grants) != 1 {
		t.Fatalf("fixture headroom wrong: %+v", decision.Batch.Grants)
	}
	if _, err := CommitSharedCapacity(ctx, store, decision); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, charges, err = reopened.ReadBrokerPlanningState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(charges) != 4 {
		t.Fatalf("restart lost commitments: %d charges", len(charges))
	}
	replayed, err := EvaluateSharedCapacity(state, charges, profiles, scDemand(scPoolA, scPoolB, scPoolC), opt, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Batch.Grants) != 0 {
		t.Fatalf("blank in-memory history became new headroom: %+v", replayed.Batch.Grants)
	}
}
