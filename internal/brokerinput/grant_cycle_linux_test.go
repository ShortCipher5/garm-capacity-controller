package brokerinput

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func enrolledCycleFixture(t *testing.T) (admission.BrokerGrantState, garmapi.DemandObservation, string, capacitybroker.Options) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("unexpected request")
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/api/v1/pools":
			json.NewEncoder(w).Encode([]map[string]any{{"id": pool, "repo_id": repo, "provider_name": "capacity", "generation": 1, "max_runners": 4, "enabled": true, "tags": []map[string]string{{"name": "linux"}}, "flavor": "small", "image": "image", "os_arch": "amd64", "os_type": "linux"}})
		case "/api/v1/jobs":
			json.NewEncoder(w).Encode([]map[string]any{{"workflow_job_id": 1, "repo_id": repo, "status": "queued", "labels": []string{"linux"}, "created_at": time.Now().UTC().Add(-time.Minute), "locked_by": "00000000-0000-0000-0000-000000000000"}})
		case "/api/v1/instances":
			json.NewEncoder(w).Encode([]any{})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := garmapi.NewDemand(server.URL, "token", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, []string{"capacity"}, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	observation, err := client.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, profiles, opt := ledgerFixture()
	opt.Now = time.Now().UTC()
	h := state.Config.Hosts["host"]
	h.ObservedAt = opt.Now
	state.Config.Hosts["host"] = h
	state.HostBindings = map[string]admission.HostBinding{"host": {Sequence: 4}}
	state.PoolProfiles = map[string]admission.BrokerPoolProfile{pool: {Sequence: 10, Pool: pool, Repository: repo, SourceIdentity: client.SourceIdentity(), PoolFingerprint: observation.Pools[0].OwnershipFingerprint, Resources: profiles[pool].Size, Authority: admission.StockProviderAuthority{PoolID: pool, Controller: "controller", ProviderName: "capacity"}, Domains: []admission.BrokerProfileDomain{{Host: "host", HostBindingSequence: 4, Sequence: 8}}}}
	return state, observation, client.SourceIdentity(), opt
}
func TestEnrolledCycleBuildsBoundedRevisionBatch(t *testing.T) {
	state, obs, source, opt := enrolledCycleFixture(t)
	plan, batch, err := enrolledCycle(state, nil, obs, source, opt, time.Minute)
	if err != nil || len(plan.Assignments) != 1 || len(batch.Grants) != 1 || batch.ExpectedSequence != state.Sequence || batch.PolicyRevision != state.Policy.Revision {
		t.Fatalf("%+v %+v %v", plan, batch, err)
	}
	_, retry, err := enrolledCycle(state, nil, obs, source, opt, time.Minute)
	if err != nil || retry.ID != batch.ID {
		t.Fatal("same revision changed cycle identity")
	}
	state.Sequence++
	_, next, err := enrolledCycle(state, nil, obs, source, opt, time.Minute)
	if err != nil || next.ID == batch.ID {
		t.Fatal("new revision reused cycle identity")
	}
	if batch.Grants[0].HostBindingSequence != 4 || batch.Grants[0].Execution.Name != "" || batch.Grants[0].ExpiresAt != opt.Now.Add(time.Minute) {
		t.Fatal("grant lost enrolled binding")
	}
}
func TestEnrolledCycleRejectsUntrustedOrChangedMapping(t *testing.T) {
	for _, mode := range []string{"source", "serialized", "fingerprint", "binding", "missing", "duration"} {
		t.Run(mode, func(t *testing.T) {
			state, obs, source, opt := enrolledCycleFixture(t)
			ttl := time.Minute
			switch mode {
			case "source":
				source = strings.Repeat("f", 64)
			case "serialized":
				raw, _ := json.Marshal(obs)
				obs = garmapi.DemandObservation{}
				if json.Unmarshal(raw, &obs) != nil {
					t.Fatal("decode")
				}
			case "fingerprint":
				p := state.PoolProfiles[pool]
				p.PoolFingerprint = "wrong"
				state.PoolProfiles[pool] = p
			case "binding":
				state.HostBindings["host"] = admission.HostBinding{Sequence: 5}
			case "missing":
				delete(state.PoolProfiles, pool)
			case "duration":
				ttl = 24 * time.Hour
			}
			if _, _, err := enrolledCycle(state, nil, obs, source, opt, ttl); err == nil {
				t.Fatal("unsafe mapping accepted")
			}
		})
	}
}

func TestEnrolledCycleReusesDurableGrantAfterRestart(t *testing.T) {
	state, obs, source, opt := enrolledCycleFixture(t)
	_, batch, err := enrolledCycle(state, nil, obs, source, opt, time.Minute)
	if err != nil || len(batch.Grants) != 1 {
		t.Fatal(err)
	}
	// Model the subsequent coherent read after a successful or uncertain commit;
	// the same queued job must reuse the existing charged grant, not issue again.
	state.Sequence++
	state.Grants = []admission.BrokerGrant{{ID: strings.Repeat("a", 64), State: "unused", Spec: batch.Grants[0]}}
	charge := admission.BrokerPlanningCharge{ID: "grant:" + state.Grants[0].ID, Host: "host", Pool: pool, Resources: batch.Grants[0].Resources}
	state.Allocated = admission.Allocations{Global: charge.Resources, Hosts: map[string]admission.Resources{"host": charge.Resources}, Pools: map[string]admission.Resources{pool: charge.Resources}}
	plan, next, err := enrolledCycle(state, []admission.BrokerPlanningCharge{charge}, obs, source, opt, time.Minute)
	if err != nil || len(next.Grants) != 0 || len(plan.CoveredByGrant) != 1 {
		t.Fatalf("duplicate after charged commit: %+v %+v %v", plan, next, err)
	}
}

// This crosses the planner-to-real-SQLite issuance boundary. Profile enrollment
// itself is covered by schema20 tests; this fixture supplies the validated plan
// input explicitly so concurrent durable batch behavior is isolated.
func TestCycleBatchRealLedgerConcurrentReplayAndRestart(t *testing.T) {
	state, obs, source, opt := enrolledCycleFixture(t)
	path := filepath.Join(t.TempDir(), "ledger.db")
	store, err := admission.OpenSQLite(path, state.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	policy := state.Policy
	policy.Revision = 0
	if err = store.ReplaceBrokerPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding := admission.HostBinding{Host: "host", ServerKeyFingerprint: strings.Repeat("a", 64), OriginFingerprint: strings.Repeat("b", 64), ScopeFingerprint: strings.Repeat("c", 64), ServerName: "host", ServerVersion: "6.0.4", ObservedAt: time.Now().UTC()}
	if err = store.BindHostIdentities(ctx, fence, []admission.HostBinding{binding}); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteReconciliation(ctx, fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host", ObservedAt: time.Now().UTC(), Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}}); err != nil {
		t.Fatal(err)
	}
	actual, charges, err := store.ReadBrokerPlanningState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profile := state.PoolProfiles[pool]
	profile.Authority.ClientFingerprint = strings.Repeat("f", 64)
	profile.Execution = admission.ExecutionImage{Controller: "controller", Flavor: "small", Image: "image", Fingerprint: strings.Repeat("a", 64), Type: "container", Profiles: []string{"runner"}, Arch: "amd64"}
	profile.Domains[0].HostBindingSequence = actual.HostBindings["host"].Sequence
	actual.PoolProfiles = map[string]admission.BrokerPoolProfile{pool: profile}
	opt.Now = time.Now().UTC()
	_, batch, err := enrolledCycle(actual, charges, obs, source, opt, time.Minute)
	if err != nil || len(batch.Grants) != 1 {
		t.Fatalf("plan %v", err)
	}
	// Short but comfortably future TTL allows real-clock expiry after replay.
	batch.Grants[0].ExpiresAt = time.Now().Add(time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := store.IssueBrokerGrants(ctx, batch); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	got, err := store.ReadBrokerGrantState(ctx)
	if err != nil || len(got.Grants) != 1 || got.Allocated.Global.Runners != 1 {
		t.Fatalf("duplicate charge %+v %v", got, err)
	}
	altered := batch
	altered.Grants = append([]admission.BrokerGrantSpec(nil), batch.Grants...)
	altered.Grants[0].ExpiresAt = altered.Grants[0].ExpiresAt.Add(time.Second)
	if _, err = store.IssueBrokerGrants(ctx, altered); err == nil {
		t.Fatal("same cycle accepted changed payload")
	}
	stale := batch
	stale.ID = strings.Repeat("e", 64)
	if _, err = store.IssueBrokerGrants(ctx, stale); err == nil {
		t.Fatal("stale competing cycle issued")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	retry, err := reopened.IssueBrokerGrants(ctx, batch)
	if err != nil || len(retry) != 1 || retry[0].ID != got.Grants[0].ID {
		t.Fatalf("restart replay %v", err)
	}
	after, err := reopened.ReadBrokerGrantState(ctx)
	if err != nil || after.Allocated.Global.Runners != 1 {
		t.Fatal("restart lost charge", err)
	}
	time.Sleep(time.Until(batch.Grants[0].ExpiresAt) + time.Millisecond)
	expiryErrors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			expiryErrors <- retireExpiredCycleGrants(ctx, reopened, after.Grants, time.Now())
		}()
	}
	wg.Wait()
	close(expiryErrors)
	for e := range expiryErrors {
		if e != nil {
			t.Fatal(e)
		}
	}
	expired, e := reopened.ReadBrokerGrantState(ctx)
	if e != nil || expired.Allocated.Global.Runners != 0 || expired.Grants[0].State != "expired" {
		t.Fatal("expiry did not persist", e)
	}
	replay, e := reopened.IssueBrokerGrants(ctx, batch)
	if e != nil || len(replay) != 1 || replay[0].State != "expired" {
		t.Fatal("expired replay resurrected", e)
	}
	if e = reopened.Close(); e != nil {
		t.Fatal(e)
	}
	recovered, e := admission.OpenSQLite(path, admission.Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer recovered.Close()
	stable, e := recovered.ReadBrokerGrantState(ctx)
	if e != nil || stable.Allocated.Global.Runners != 0 || stable.Grants[0].State != "expired" {
		t.Fatal("restart lost terminal expiry", e)
	}
}
