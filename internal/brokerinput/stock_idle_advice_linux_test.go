package brokerinput

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestStockIdleAdviceAuthenticCrossRepositoryQueue(t *testing.T) {
	const repoB = "33333333-3333-4333-8333-333333333333"
	const poolB = "44444444-4444-4444-8444-444444444444"
	// These are protocol fixtures, not claims about any live host or released slot.
	for _, mode := range []string{"preview", "B-only", "A-demand", "locked-B", "legacy", "not-enrolled", "floor", "zero", "settings-drift", "tampered", "expiry", "repeat", "unknown-supply", "detached", "scope-drift", "absent-idle", "busy-only", "creating-only"} {
		t.Run(mode, func(t *testing.T) {
			drifting := false
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("non-authenticated read")
					w.WriteHeader(403)
					return
				}
				switch r.URL.Path {
				case "/api/v1/pools":

					minimum := 1
					if mode == "zero" {
						minimum = 0
					}
					if mode == "settings-drift" && drifting {
						minimum = 2
					}
					json.NewEncoder(w).Encode([]any{map[string]any{"id": pool, "repo_id": repo, "provider_name": "capacity", "generation": 1, "max_runners": 3, "min_idle_runners": minimum, "enabled": true, "tags": []map[string]string{{"name": "linux"}}}, map[string]any{"id": poolB, "repo_id": repoB, "provider_name": "capacity", "generation": 1, "max_runners": 3, "enabled": true, "tags": []map[string]string{{"name": "linux"}}}})
				case "/api/v1/jobs":
					target := repoB
					if mode == "A-demand" {
						target = repo
					}
					locked := "00000000-0000-0000-0000-000000000000"
					if mode == "locked-B" {
						locked = poolB
					}
					json.NewEncoder(w).Encode([]any{map[string]any{"workflow_job_id": 1, "repo_id": target, "status": "queued", "labels": []string{"linux"}, "created_at": time.Now().Add(-time.Minute), "locked_by": locked}})
				case "/api/v1/instances":
					if mode == "absent-idle" {
						json.NewEncoder(w).Encode([]any{})
						return
					}
					runnerStatus := "idle"
					if mode == "busy-only" {
						runnerStatus = "active"
					}
					if mode == "creating-only" {
						runnerStatus = "pending"
					}
					if mode == "unknown-supply" {
						runnerStatus = "unknown"
					}
					json.NewEncoder(w).Encode([]any{map[string]any{"id": "55555555-5555-4555-8555-555555555555", "name": "warm-a", "provider_name": "capacity", "pool_id": pool, "status": "running", "runner_status": runnerStatus}})
				default:
					t.Error("unexpected route")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			client, err := garmapi.NewDemand(server.URL, "test-token", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, []string{"capacity"}, []string{repo, repoB})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			read := func() garmapi.DemandObservation {
				o, e := client.ObserveWithRepositoryIdleScope(context.Background())
				if mode == "legacy" {
					o, e = client.Observe(context.Background())
				}
				if e != nil {
					t.Fatal(e)
				}
				return o
			}
			first := read()
			if mode == "preview" {
				time.Sleep(15 * time.Second)
			} // Real capture gap; never rewrite sealed timestamps.
			drifting = true
			second := read()
			if mode == "detached" {
				raw, _ := json.Marshal(second)
				var detached garmapi.DemandObservation
				json.Unmarshal(raw, &detached)
				second = detached
			}
			if mode == "repeat" {
				second = first
			}
			if mode == "tampered" {
				second.Supply[0].Other++
			}
			state, _, opt := ledgerFixture()
			opt.Now = second.FinishedAt
			host := state.Config.Hosts["host"]
			host.ObservedAt = first.StartedAt
			state.Config.Hosts["host"] = host
			state.Config.Pools[poolB] = state.Config.Pools[pool]
			state.Policy.Repositories[0].MaxRunners = 3
			state.Policy.Repositories[0].Pools[0].MaxRunners = 3
			state.Policy.Repositories = append(state.Policy.Repositories, admission.BrokerRepositoryPolicy{ID: repoB, Weight: 1, MaxRunners: 3, Pools: []admission.BrokerPoolPolicy{{ID: poolB, MaxRunners: 3}}})
			if mode == "floor" {
				state.Policy.Repositories[0].Pools[0].WarmIdle = 1
			}
			size := admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1}
			req := admission.Request{Key: "warm-request", Pool: pool, Host: "host", Resources: size}
			state.Reservations = map[string]admission.Reservation{"warm-request": {Request: req, State: "creating"}}
			state.ExecutionPlans = map[string]admission.ExecutionPlan{"warm-request": {Request: req, Execution: admission.ExecutionImage{Controller: "controller", Name: "warm-a"}}}
			state.Grants = []admission.BrokerGrant{{State: "consumed", RequestKey: "warm-request", Spec: admission.BrokerGrantSpec{Authority: admission.StockProviderAuthority{Controller: "controller", PoolID: pool, ProviderName: "capacity"}}}}
			state.Allocated = admission.Allocations{Global: size, Hosts: map[string]admission.Resources{"host": size}, Pools: map[string]admission.Resources{pool: size}}
			charges := []admission.BrokerPlanningCharge{{ID: "reservation:warm-request", Pool: pool, Host: "host", Resources: size}}
			state.HostBindings = map[string]admission.HostBinding{"host": {Sequence: 4}}
			state.PoolProfiles = map[string]admission.BrokerPoolProfile{}
			for _, p := range first.Pools {
				state.PoolProfiles[p.ID] = admission.BrokerPoolProfile{Sequence: 10, Pool: p.ID, Repository: p.RepositoryID, SourceIdentity: client.SourceIdentity(), PoolFingerprint: p.OwnershipFingerprint, Resources: size, Authority: admission.StockProviderAuthority{PoolID: p.ID, Controller: "controller", ProviderName: "capacity"}, Domains: []admission.BrokerProfileDomain{{Host: "host", HostBindingSequence: 4, Sequence: 8}}}
			}
			if mode == "not-enrolled" {
				delete(state.PoolProfiles, pool)
			}
			if mode == "expiry" {
				opt.Now = first.StartedAt.Add(time.Minute)
			}
			if mode == "B-only" || mode == "preview" {
				if _, _, e := enrolledPlanningSnapshot(state, charges, first, client.SourceIdentity(), opt); e != nil {
					t.Fatalf("enrolled snapshot: %v; policy=%v", e, admission.ValidateBrokerPolicy(state.Policy))
				}
			}
			prior, e1 := sampleStockIdleAdvice(state, charges, first, client.SourceIdentity(), pool, opt)
			if mode == "scope-drift" {
				state.Policy.Revision++
			}
			current, e2 := sampleStockIdleAdvice(state, charges, second, client.SourceIdentity(), pool, opt)
			got, ok := proposeStockIdleDecrease(prior, current, opt.Now)
			if mode == "B-only" || mode == "preview" {
				if e1 != nil || e2 != nil || !ok || got.Observed != 1 || got.Suggested != 0 || got.UnchangedMaximum != 3 || got.Actionable || got.CompleteCoverage || got.CapacityReleased {
					t.Fatalf("%+v %v %v %v", got, ok, e1, e2)
				}
			} else if ok {
				t.Fatalf("unsafe suggestion %+v", got)
			}
			if mode == "preview" {
				var history stockIdleHistory
				one := history.accept(map[string]stockIdleSample{pool: prior}, nil, opt.Now, opt.Now.Add(time.Minute))
				if one.State != "no_confirmed_proposals" || len(one.Proposals) != 0 {
					t.Fatal(one)
				}
				two := history.accept(map[string]stockIdleSample{pool: current}, nil, opt.Now, opt.Now.Add(time.Minute))
				if two.State != "proposed" || two.HostEvidence != "configured_only" || len(two.Proposals) != 1 || two.Proposals[0].SuggestedMinIdle != 0 || two.Proposals[0].UnchangedMaximum != 3 {
					t.Fatal(two)
				}
				repeated := history.accept(map[string]stockIdleSample{pool: current}, nil, opt.Now, opt.Now.Add(time.Minute))
				if len(repeated.Proposals) != 0 {
					t.Fatal("same capture reconfirmed")
				}
			}

		})
	}
}

func TestStockIdleAdviceScansAllUnsortedEdges(t *testing.T) {
	for _, job := range []garmapi.QueuedJob{
		{RepositoryID: repo},
		{RepositoryID: "other", CandidatePoolIDs: []string{"z", pool, "a"}},
		{RepositoryID: "other", CandidatePoolIDs: []string{"z", "a", pool}},
		{RepositoryID: "other", IdleOnlyPoolIDs: []string{"z", pool, "a"}},
		{RepositoryID: "other", IdleOnlyPoolIDs: []string{"z", "a", pool}},
	} {
		if !stockIdleDemandTouches([]garmapi.QueuedJob{job}, repo, pool) {
			t.Fatal("missed demand edge")
		}
	}
	if stockIdleDemandTouches([]garmapi.QueuedJob{{RepositoryID: "other", CandidatePoolIDs: []string{"z", "a"}}}, repo, pool) {
		t.Fatal("unrelated demand incorrectly touches pool")
	}
}

func TestStockIdleAdviceRequiresUniqueVisibleIdleSupply(t *testing.T) {
	good := garmapi.RunnerSupply{PoolID: pool, Idle: 1, Busy: 2}
	if !stockIdleVisibleSupply([]garmapi.RunnerSupply{good}, pool) {
		t.Fatal("known busy alongside idle incorrectly removed eligibility")
	}
	for _, rows := range [][]garmapi.RunnerSupply{
		nil, {{PoolID: "other", Idle: 1}}, {good, good}, {{PoolID: pool, Busy: 1}}, {{PoolID: pool, Idle: 1, Creating: 1}}, {{PoolID: pool, Idle: 1, Other: 1}}, {{PoolID: pool, Idle: -1}},
	} {
		if stockIdleVisibleSupply(rows, pool) {
			t.Fatal("absent/ambiguous/nonidle supply accepted")
		}
	}
}
