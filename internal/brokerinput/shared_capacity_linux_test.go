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
	"reflect"
	"slices"
	"strings"
	"sync"
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

// sharedCapacityStoreFixture builds a real SQLite ledger with the full
// enrollment ceremony: native domain, host binding, revisioned policy and
// three enrolled pool profiles observed through a real local stock
// transport, plus three already-issued durable grants. Exactly one slot of
// headroom remains. Observations use the real clock because the store
// enforces freshness against it.
func sharedCapacityStoreFixture(t *testing.T, stock *httptest.Server, trust *tls.Config) (*admission.SQLiteStore, string) {
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
	for i := range repos {
		policy.Repositories = append(policy.Repositories, admission.BrokerRepositoryPolicy{ID: repos[i], Weight: 1, MaxRunners: 4, Pools: []admission.BrokerPoolPolicy{{ID: pools[i], MaxRunners: 4}}})
		config.Pools[pools[i]] = admission.Pool{Budget: total, Hosts: []string{"host"}}
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
	return store, path
}

const scPoolD = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"

type scPool struct {
	id, repo string
	disabled bool
}

var scPools = []scPool{{scPoolA, scRepoA, false}, {scPoolB, scRepoB, false}, {scPoolC, scRepoC, false}}

type scRunner struct {
	name, pool, kind string
}

// All stock rows and TLS identities originate from this test's GET-only server.
func scStock(t *testing.T, pools []scPool, demand []string, runners []scRunner) (*httptest.Server, *tls.Config, *garmapi.DemandClient) {
	t.Helper()
	rows, jobs, instances := []map[string]any{}, []map[string]any{}, []map[string]any{}
	repos := []string{}
	for _, p := range pools {
		if !slices.Contains(repos, p.repo) {
			repos = append(repos, p.repo)
		}
		rows = append(rows, map[string]any{"id": p.id, "repo_id": p.repo, "provider_name": "capacity", "generation": 1, "max_runners": 4, "enabled": !p.disabled, "tags": []map[string]string{{"name": p.id}, {"name": "linux"}}, "flavor": "small", "image": "image", "os_arch": "amd64", "os_type": "linux"})
		for i, id := range demand {
			if p.id == id {
				jobs = append(jobs, map[string]any{"workflow_job_id": i + 1, "repo_id": p.repo, "status": "queued", "labels": []string{id}, "created_at": time.Now().UTC().Add(-time.Minute)})
			}
		}
	}
	for i, r := range runners {
		status, registration := "running", "idle"
		if r.kind == "creating" {
			status, registration = "pending_create", "pending"
		}
		instances = append(instances, map[string]any{"id": fmt.Sprintf("eeeeeeee-eeee-4eee-8eee-%012d", i), "name": r.name, "pool_id": r.pool, "provider_name": "capacity", "status": status, "runner_status": registration})
	}
	stock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("unexpected stock mutation")
			w.WriteHeader(405)
			return
		}
		var result any
		switch r.URL.Path {
		case "/api/v1/pools":
			result = rows
		case "/api/v1/jobs":
			result = jobs
		case "/api/v1/instances":
			result = instances
		default:
			for _, p := range rows {
				if r.URL.Path == "/api/v1/pools/"+p["id"].(string) {
					result = p
					break
				}
			}
			if result == nil {
				t.Errorf("unexpected stock path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
		}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(stock.Close)
	roots := x509.NewCertPool()
	roots.AddCert(stock.Certificate())
	trust := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	source, err := garmapi.NewDemand(stock.URL, "token", trust, []string{"capacity"}, repos)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.Close)
	return stock, trust, source
}

type scInput struct {
	state    admission.BrokerGrantState
	charges  []admission.BrokerPlanningCharge
	observed garmapi.DemandObservation
	source   string
	opt      capacitybroker.Options
}

func scInputFixture(t *testing.T, pools []scPool, demand []string, runners []scRunner) *scInput {
	t.Helper()
	_, _, source := scStock(t, pools, demand, runners)
	obs, err := source.ObserveWithRepositoryIdleScope(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	total := admission.Resources{CPU: 6000, RAM: 6000, Disk: 6000, Runners: 6}
	state := admission.BrokerGrantState{Ready: true, Sequence: 41, Policy: admission.BrokerPolicy{Revision: 3},
		Config:       admission.Config{Global: total, MaxObservationAge: time.Minute, Hosts: map[string]admission.Host{"host": {Budget: total, Healthy: true, Mode: "enabled", ObservedAt: now}}, Pools: map[string]admission.Pool{}},
		Allocated:    admission.Allocations{Pools: map[string]admission.Resources{}, Hosts: map[string]admission.Resources{}},
		HostBindings: map[string]admission.HostBinding{"host": {Host: "host", Sequence: 9}},
		PoolProfiles: map[string]admission.BrokerPoolProfile{}, Reservations: map[string]admission.Reservation{}, ExecutionPlans: map[string]admission.ExecutionPlan{},
	}
	for _, p := range obs.Pools {
		id, repo := p.ID, p.RepositoryID
		index := slices.IndexFunc(state.Policy.Repositories, func(r admission.BrokerRepositoryPolicy) bool { return r.ID == repo })
		if index < 0 {
			index = len(state.Policy.Repositories)
			state.Policy.Repositories = append(state.Policy.Repositories, admission.BrokerRepositoryPolicy{ID: repo, Weight: 1, MaxRunners: 4})
		}
		r := &state.Policy.Repositories[index]
		r.Pools = append(r.Pools, admission.BrokerPoolPolicy{ID: id, MaxRunners: 4})
		state.Config.Pools[id] = admission.Pool{Budget: total, Hosts: []string{"host"}}
		state.PoolProfiles[id] = admission.BrokerPoolProfile{Pool: id, Sequence: 5, Authority: scAuthority(id), Resources: scSize, Execution: scExecution(), Repository: repo, PoolFingerprint: p.OwnershipFingerprint, SourceIdentity: source.SourceIdentity(), Domains: []admission.BrokerProfileDomain{{Host: "host", HostBindingSequence: 9, Sequence: 4}}}
	}
	f := &scInput{state: state, observed: obs, source: source.SourceIdentity(), opt: capacitybroker.Options{Now: now, MaxAge: time.Minute, MaxAssignments: 100}}
	for _, r := range runners {
		req := admission.Request{Key: r.name, Pool: r.pool, Host: "host", Resources: scSize}
		f.state.Reservations[r.name] = admission.Reservation{Request: req, State: "creating"}
		f.state.ExecutionPlans[r.name] = admission.ExecutionPlan{Request: req, Execution: admission.ExecutionImage{Controller: "controller", Name: r.name}}
		f.state.Grants = append(f.state.Grants, admission.BrokerGrant{State: "consumed", RequestKey: r.name, Spec: admission.BrokerGrantSpec{Authority: scAuthority(r.pool)}})
		f.charge("reservation:"+r.name, r.pool, scSize)
	}
	return f
}

func (f *scInput) charge(key, id string, size admission.Resources) {
	f.charges = append(f.charges, admission.BrokerPlanningCharge{ID: key, Host: "host", Pool: id, Resources: size})
	add := func(used admission.Resources) admission.Resources {
		return admission.Resources{CPU: used.CPU + size.CPU, RAM: used.RAM + size.RAM, Disk: used.Disk + size.Disk, Runners: used.Runners + size.Runners}
	}
	f.state.Allocated.Global = add(f.state.Allocated.Global)
	f.state.Allocated.Hosts["host"] = add(f.state.Allocated.Hosts["host"])
	f.state.Allocated.Pools[id] = add(f.state.Allocated.Pools[id])
}
func (f *scInput) evaluate(t *testing.T) SharedCapacityDecision {
	t.Helper()
	d, err := EvaluateSharedCapacity(f.state, f.charges, f.observed, f.source, f.opt)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSharedCapacityWarmUsesEnabledPoolAndVerifiedSupply(t *testing.T) {
	for _, tc := range []struct {
		name     string
		disabled bool
		runners  []scRunner
		want     int
	}{
		{"fresh scoped zero", false, nil, 1}, {"disabled floor", true, nil, 0},
		{"already idle", false, []scRunner{{"runner", scPoolA, "idle"}}, 0},
		{"creating once", false, []scRunner{{"runner", scPoolA, "creating"}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := scInputFixture(t, []scPool{{scPoolA, scRepoA, tc.disabled}}, nil, tc.runners)
			floor := int64(1)
			if tc.name == "creating once" {
				floor = 2
			}
			f.state.Policy.Repositories[0].Pools[0].WarmIdle = floor
			d := f.evaluate(t)
			if len(d.Plan.Assignments) != tc.want || len(d.Refusals) != 0 || d.Remaining.Global[3] != 6-int64(len(tc.runners)) {
				t.Fatalf("%+v", d)
			}
			for _, a := range d.Plan.Assignments {
				if !a.Warm || a.Pool != scPoolA {
					t.Fatal(a)
				}
			}
		})
	}
}

func TestSharedCapacityRejectsUnusableSupply(t *testing.T) {
	for _, mode := range []string{"missing supply", "missing source", "wrong source", "wrong pool enrollment", "wrong repo", "expired", "missing host binding", "missing profile", "wrong provider", "unmatched charged runner", "unmatched stock identity", "mismatched charge projection"} {
		t.Run(mode, func(t *testing.T) {
			f := scInputFixture(t, scPools, nil, []scRunner{{"runner", scPoolA, "idle"}})
			f.state.Policy.Repositories[0].Pools[0].WarmIdle = 2
			switch mode {
			case "missing supply":
				f.observed.Supply = nil
			case "missing source":
				f.source = ""
			case "wrong source":
				f.source = "foreign"
			case "wrong pool enrollment":
				p := f.state.PoolProfiles[scPoolA]
				p.PoolFingerprint = "different"
				f.state.PoolProfiles[scPoolA] = p
			case "wrong repo":
				f.state.Policy.Repositories[0].ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
			case "expired":
				f.opt.Now = f.observed.StartedAt.Add(f.opt.MaxAge)
			case "missing host binding":
				delete(f.state.HostBindings, "host")
			case "missing profile":
				delete(f.state.PoolProfiles, scPoolA)
			case "wrong provider":
				p := f.state.PoolProfiles[scPoolA]
				p.Authority.ProviderName = "another"
				f.state.PoolProfiles[scPoolA] = p
			case "unmatched charged runner":
				f.state.ExecutionPlans = nil
			case "mismatched charge projection":
				f.state.Allocated.Hosts["host"] = admission.Resources{}
			case "unmatched stock identity":
				f.state.Grants = nil
			}
			d, err := EvaluateSharedCapacity(f.state, f.charges, f.observed, f.source, f.opt)
			if !errors.Is(err, capacitybroker.ErrSnapshot) || len(d.Plan.Assignments) != 0 {
				t.Fatalf("%+v %v", d, err)
			}
		})
	}
}

func TestSharedCapacityQueueAndWarmSupplyAccountedSeparately(t *testing.T) {
	// Extra idle and pending in pool A cannot mask queued jobs that only fit B.
	// B's first job consumes its single charged creating runner, exactly once.
	pools := []scPool{{scPoolA, scRepoA, false}, {scPoolB, scRepoA, false}}
	f := scInputFixture(t, pools, []string{scPoolB, scPoolB}, []scRunner{{"idle", scPoolA, "idle"}, {"pending", scPoolA, "creating"}, {"startup", scPoolB, "creating"}})
	f.state.Config.Global = f.state.Allocated.Global
	f.state.Policy.Repositories[0].MaxRunners = 6
	d := f.evaluate(t)
	if len(d.Plan.DeferredByInflight) != 1 || len(d.Plan.RemainingJobs) != 1 || len(d.Plan.Assignments) != 0 ||
		!slices.Contains(d.Refusals, SharedCapacityRefusal{"pool", scPoolB, SharedCapacityGlobalBudget}) {
		t.Fatalf("%+v", d)
	}
	// Charging and deferring the first job does not also satisfy a warm floor.
	f.state.Policy.Repositories[0].Pools[1].WarmIdle = 1
	f.state.Config.Global = admission.Resources{CPU: 4000, RAM: 4000, Disk: 4000, Runners: 4}
	d = f.evaluate(t)
	if len(d.Plan.Assignments) != 1 || d.Plan.Assignments[0].Warm || len(d.Plan.RemainingJobs) != 0 ||
		!slices.Contains(d.Refusals, SharedCapacityRefusal{"pool", scPoolB, SharedCapacityGlobalBudget}) {
		t.Fatalf("%+v", d)
	}
}

func TestSharedCapacityBudgetAndPolicyBoundaries(t *testing.T) {
	for _, scope := range []string{"global", "host", "pool"} {
		for dim := 0; dim < 4; dim++ {
			t.Run(fmt.Sprintf("%s/%d", scope, dim), func(t *testing.T) {
				f := scInputFixture(t, scPools, []string{scPoolA}, nil)
				limit := resources(scSize)
				set := func() {
					b := heldResources(limit)
					switch scope {
					case "global":
						f.state.Config.Global = b
					case "host":
						h := f.state.Config.Hosts["host"]
						h.Budget = b
						f.state.Config.Hosts["host"] = h
					case "pool":
						p := f.state.Config.Pools[scPoolA]
						p.Budget = b
						f.state.Config.Pools[scPoolA] = p
					}
				}
				set()
				if d := f.evaluate(t); len(d.Plan.Assignments) != 1 || len(d.Refusals) > 0 {
					t.Fatalf("exact fit %+v", d)
				}
				limit[dim]--
				set()
				reason := map[string]string{"global": SharedCapacityGlobalBudget, "pool": SharedCapacityPoolBudget, "host": SharedCapacityNoEligibleHost}[scope]
				d := f.evaluate(t)
				if len(d.Plan.Assignments) != 0 || !slices.Contains(d.Refusals, SharedCapacityRefusal{"pool", scPoolA, reason}) {
					t.Fatalf("one unit short %+v", d)
				}
			})
		}
	}
	for _, tc := range []struct{ name, want string }{{"pool cap", SharedCapacityPoolCap}, {"repo cap", SharedCapacityRepoCap}, {"cycle bound", SharedCapacityPlanLimit}, {"ineligible host", SharedCapacityNoEligibleHost}} {
		t.Run(tc.name, func(t *testing.T) {
			f := scInputFixture(t, []scPool{{scPoolA, scRepoA, false}, {scPoolD, scRepoA, false}}, []string{scPoolA, scPoolA}, nil)
			switch tc.name {
			case "pool cap":
				f.state.Policy.Repositories[0].Pools[0].MaxRunners = 0
			case "repo cap":
				r := &f.state.Policy.Repositories[0]
				r.MaxRunners = 1
				for i := range r.Pools {
					r.Pools[i].MaxRunners = 1
				}
				f.charge("retained-sibling", scPoolD, scSize)
			case "cycle bound":
				f.opt.MaxAssignments = 1
			case "ineligible host":
				h := f.state.Config.Hosts["host"]
				h.Mode = "paused"
				f.state.Config.Hosts["host"] = h
			}
			d := f.evaluate(t)
			if !slices.Contains(d.Refusals, SharedCapacityRefusal{"pool", scPoolA, tc.want}) {
				t.Fatalf("%+v", d)
			}
		})
	}
}

func TestSharedCapacityExistingLedgerContentionAndRestart(t *testing.T) {
	stock, trust, source := scStock(t, scPools, []string{scPoolA, scPoolA, scPoolB, scPoolB, scPoolC, scPoolC}, nil)
	store, path := sharedCapacityStoreFixture(t, stock, trust)
	read := func(s *admission.SQLiteStore) (admission.BrokerGrantState, SharedCapacityDecision) {
		t.Helper()
		obs, err := source.ObserveWithRepositoryIdleScope(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		state, charges, err := s.ReadBrokerPlanningState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		opt := capacitybroker.Options{Now: time.Now().UTC(), MaxAge: time.Minute, MaxAssignments: 100}
		d, err := EvaluateSharedCapacity(state, charges, obs, source.SourceIdentity(), opt)
		if err != nil {
			t.Fatal(err)
		}
		return state, d
	}
	state, d := read(store)
	if len(d.Plan.CoveredByGrant) != 3 || len(d.Plan.Assignments) != 1 || len(d.Plan.RemainingJobs) != 2 || len(d.Refusals) != 2 || d.Remaining.Global[3] != 1 {
		t.Fatalf("%+v", d)
	}
	// Model two independent fixture writers competing at the pre-existing
	// ledger CAS. No writable batch or commit method comes from the planner.
	before := state.Sequence
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, pool := range []string{scPoolA, scPoolB} {
		batch := admission.BrokerGrantBatch{ID: strings.Repeat(fmt.Sprint(i+1), 64), ExpectedSequence: before, PolicyRevision: state.Policy.Revision, Grants: []admission.BrokerGrantSpec{{Authority: scAuthority(pool), Host: "host", HostBindingSequence: state.HostBindings["host"].Sequence, Resources: scSize, Execution: scExecution(), ExpiresAt: time.Now().UTC().Add(time.Hour)}}}
		wg.Go(func() { _, errs[i] = store.IssueBrokerGrants(t.Context(), batch) })
	}
	wg.Wait()
	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, admission.ErrBrokerConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("durable contention outcomes %v", errs)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, d = read(reopened)
	if state.Allocated.Global.Runners != 4 || d.Remaining.Global[3] != 0 || len(d.Plan.Assignments) != 0 || len(d.Plan.CoveredByGrant) != 4 || len(d.Plan.RemainingJobs) != 2 {
		t.Fatalf("reopen %+v", d)
	}
}

func TestSharedCapacityDeterminismWeightsAndDrift(t *testing.T) {
	f := scInputFixture(t, scPools, []string{scPoolA, scPoolA, scPoolB, scPoolB}, nil)
	f.state.Policy.Repositories[0].Weight = 3
	f.state.Config.Global = admission.Resources{CPU: 5000, RAM: 5000, Disk: 5000, Runners: 5}
	f.charge("retained-B", scPoolB, scSize)
	f.charge("retained-A", scPoolA, scSize)
	first := f.evaluate(t)
	if len(first.Plan.Assignments) != 3 || first.Plan.Assignments[0].Pool != scPoolA || first.Plan.Assignments[1].Pool != scPoolA {
		t.Fatalf("weighted allocation %+v", first)
	}
	slices.Reverse(f.charges)
	if next := f.evaluate(t); !reflect.DeepEqual(first, next) {
		t.Fatal("order changed the decision")
	}
	f.state.Config.Global = scSize
	d := f.evaluate(t)
	if len(d.Plan.Assignments) != 0 || !slices.Contains(d.Refusals, SharedCapacityRefusal{"global", "global", SharedCapacityOverage}) {
		t.Fatalf("retained drift %+v", d)
	}
}

func TestSharedCapacityDisabledIdleRemainsAvailableToQueue(t *testing.T) {
	f := scInputFixture(t, []scPool{{scPoolA, scRepoA, true}}, []string{scPoolA, scPoolA}, []scRunner{{"idle", scPoolA, "idle"}})
	f.state.Policy.Repositories[0].Pools[0].WarmIdle = 2
	d := f.evaluate(t)
	if len(d.Plan.CoveredByIdle) != 1 || len(d.Plan.RemainingJobs) != 1 || len(d.Plan.Assignments) != 0 ||
		len(d.Plan.RemainingJobs[0].Candidates) != 0 || d.Remaining.Global[3] != 5 {
		t.Fatalf("disabled pool %+v", d)
	}
}

func TestSharedCapacityUnknownAndLoweredCapRetainCharge(t *testing.T) {
	f := scInputFixture(t, scPools, []string{scPoolB}, nil)
	req := admission.Request{Key: "uncertain", Pool: scPoolA, Host: "host", Resources: scSize}
	f.state.Grants = []admission.BrokerGrant{{State: "consumed", RequestKey: req.Key, Spec: admission.BrokerGrantSpec{Authority: scAuthority(scPoolA)}}}
	f.state.ExecutionPlans[req.Key] = admission.ExecutionPlan{Request: req, Execution: admission.ExecutionImage{Controller: "controller", Name: "missing"}}
	f.state.Reservations[req.Key] = admission.Reservation{Request: req, State: "unknown"}
	f.charge("reservation:"+req.Key, scPoolA, scSize)
	f.state.Config.Global = scSize
	f.state.Policy.Repositories[0].MaxRunners = 0
	f.state.Policy.Repositories[0].Pools[0].MaxRunners = 0
	d := f.evaluate(t)
	if len(d.Plan.Assignments) != 0 || d.Remaining.Global[3] != 0 || len(d.Plan.RemainingJobs) != 1 ||
		!slices.Contains(d.Refusals, SharedCapacityRefusal{"pool", scPoolB, SharedCapacityGlobalBudget}) ||
		!slices.Contains(d.Refusals, SharedCapacityRefusal{"pool_runner_cap", scPoolA, SharedCapacityOverage}) ||
		!slices.Contains(d.Refusals, SharedCapacityRefusal{"repository", scRepoA, SharedCapacityOverage}) {
		t.Fatalf("missing stock row and lower cap %+v", d)
	}
}
