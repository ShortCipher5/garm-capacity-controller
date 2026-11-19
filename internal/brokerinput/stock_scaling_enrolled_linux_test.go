package brokerinput

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestSampleStockScalingEnrolledSourceRespectsWeights(t *testing.T) {
	const repoB = "33333333-3333-4333-8333-333333333333"
	const poolB = "44444444-4444-4444-8444-444444444444"
	created := time.Now().UTC().Add(-time.Minute)
	stock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("expected only authenticated stock reads")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/api/v1/pools":
			pools := []map[string]any{}
			for _, p := range [][2]string{{pool, repo}, {poolB, repoB}} {
				pools = append(pools, map[string]any{"id": p[0], "repo_id": p[1], "provider_name": "capacity", "generation": 1, "max_runners": 1, "enabled": true, "tags": []map[string]string{{"name": "linux"}}})
			}
			json.NewEncoder(w).Encode(pools)
		case "/api/v1/jobs":
			jobs := []map[string]any{}
			for i, r := range []string{repo, repoB} {
				jobs = append(jobs, map[string]any{"workflow_job_id": i + 1, "repo_id": r, "status": "queued", "labels": []string{"linux"}, "created_at": created, "locked_by": "00000000-0000-0000-0000-000000000000"})
			}
			json.NewEncoder(w).Encode(jobs)
		case "/api/v1/instances":
			json.NewEncoder(w).Encode([]any{})
		default:
			t.Errorf("unexpected stock route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer stock.Close()
	roots := x509.NewCertPool()
	roots.AddCert(stock.Certificate())
	source, err := garmapi.NewDemand(stock.URL, "test-token", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, []string{"capacity"}, []string{repo, repoB})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	// Two authentic, separately captured observations through the same source.
	// Move the first interval back on a simulated clock for the 15s hysteresis
	// test, without making tests sleep or substituting a fabricated observation.
	prior, err := source.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err := source.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prior.StartedAt = prior.StartedAt.Add(-20 * time.Second)
	prior.FinishedAt = prior.FinishedAt.Add(-20 * time.Second)
	for _, tc := range []struct {
		name    string
		weight  uint32
		healthy bool
		want    []StockMaximumProposal
	}{
		{"equal", 1, true, []StockMaximumProposal{{pool, repo, 1, 2}}},
		{"weighted", 3, true, []StockMaximumProposal{{poolB, repoB, 1, 2}}},
		{"host_not_accepting", 3, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := func(obs garmapi.DemandObservation) StockScalingSample {
				t.Helper()
				state, _, opt := ledgerFixture()
				opt.Now = obs.FinishedAt
				budget := admission.Resources{CPU: 3000, RAM: 3000, Disk: 3000, Runners: 3}
				size := admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1}
				state.Config.Global = budget
				state.Config.Hosts["host"] = admission.Host{Budget: budget, Healthy: tc.healthy, Mode: "enabled", ObservedAt: opt.Now}
				state.Config.Pools[poolB] = state.Config.Pools[pool]
				state.Policy.Repositories = append(state.Policy.Repositories, admission.BrokerRepositoryPolicy{ID: repoB, Weight: tc.weight, MaxRunners: 2, Pools: []admission.BrokerPoolPolicy{{ID: poolB, MaxRunners: 2}}})
				state.HostBindings = map[string]admission.HostBinding{"host": {Sequence: 4}}
				state.PoolProfiles = map[string]admission.BrokerPoolProfile{}
				for _, p := range obs.Pools {
					state.PoolProfiles[p.ID] = admission.BrokerPoolProfile{Sequence: 10, Pool: p.ID, Repository: p.RepositoryID, SourceIdentity: source.SourceIdentity(), PoolFingerprint: p.OwnershipFingerprint, Resources: size, Authority: admission.StockProviderAuthority{PoolID: p.ID, Controller: "controller", ProviderName: "capacity"}, Domains: []admission.BrokerProfileDomain{{Host: "host", HostBindingSequence: 4, Sequence: 8}}}
				}
				charged := admission.Resources{CPU: 2000, RAM: 2000, Disk: 2000, Runners: 2}
				state.Allocated = admission.Allocations{Global: charged, Hosts: map[string]admission.Resources{"host": charged}, Pools: map[string]admission.Resources{pool: size, poolB: size}}
				charges := []admission.BrokerPlanningCharge{{ID: "existing-a", Host: "host", Pool: pool, Resources: size}, {ID: "existing-b", Host: "host", Pool: poolB, Resources: size}}
				out, err := SampleStockScaling(state, charges, obs, source.SourceIdentity(), opt)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			got := ProposeStockMaximums(sample(prior), sample(current), current.FinishedAt)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
