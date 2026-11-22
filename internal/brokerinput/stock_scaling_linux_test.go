package brokerinput

import (
	"reflect"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestStockScalingProposesOnlyCanonicalHeadroom(t *testing.T) {
	build := func(offset time.Duration) StockScalingSample {
		t.Helper()
		state, _, _ := ledgerFixture()
		snapshot, _, observed, opt := fixture()
		opt.Now = opt.Now.Add(offset)
		observed.StartedAt, observed.FinishedAt = opt.Now, opt.Now
		p := snapshot.Pools[pool]
		p.Budget = &snapshot.Global
		snapshot.Pools[pool] = p
		observed.Pools[0].MaxRunners = 1
		observed.Pools[0].DiscoveryFingerprint = "checked-settings"
		observed.Jobs = append(observed.Jobs, garmapi.QueuedJob{
			WorkflowJobID: 2, RepositoryID: repo, CreatedAt: observed.StartedAt, CandidatePoolIDs: []string{pool},
		})
		plan, err := stockPlan(snapshot, state.Policy, observed, opt, true, true)
		if err != nil || len(plan.Assignments) != 2 {
			t.Fatalf("canonical plan = %+v, %v", plan, err)
		}
		sample, err := stockScalingSample(state, snapshot, observed, plan, "checked-source", opt)
		if err != nil {
			t.Fatal(err)
		}
		return sample
	}
	prior, current := build(0), build(20*time.Second)
	got := ProposeStockMaximums(prior, current, current.finish)
	// This synthetic demand snapshot does not carry wire evidence for idle.
	if want := []StockMaximumProposal{{pool, repo, 1, 2, nil}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestStockScalingWeightedOrderAndNoBorrowingProposedSavings(t *testing.T) {
	now := time.Unix(1700000000, 0)
	sample := func(start time.Time, budget int64) StockScalingSample {
		return StockScalingSample{
			source: "pinned", revision: 2, sequence: 3, budget: budget,
			start: start, finish: start.Add(time.Second), expires: start.Add(time.Minute),
			pools: map[string]scalingPool{
				"a":     {repo: "repo-a", enabled: true, current: 1, target: 2},
				"z":     {repo: "repo-z", enabled: true, current: 1, target: 2},
				"empty": {repo: "repo-a", enabled: true, current: 3, target: 1},
			},
			repoCaps:             map[string]int64{"repo-a": 5, "repo-z": 4},
			order:                []string{"z", "a", "z"},
			configuredMaximumFit: roomyMaximumFit("empty", "a", "z"),
		}
	}
	// These pure accounting samples omit explicit minimum-idle evidence.
	for _, tc := range []struct {
		budget int64
		want   []StockMaximumProposal
	}{
		{5, []StockMaximumProposal{{"empty", "repo-a", 3, 2, nil}}},
		{6, []StockMaximumProposal{{"empty", "repo-a", 3, 2, nil}, {"z", "repo-z", 1, 2, nil}}},
	} {
		prior, current := sample(now, tc.budget), sample(now.Add(20*time.Second), tc.budget)
		got := ProposeStockMaximums(prior, current, current.finish)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("budget %d: got %v, want %v", tc.budget, got, tc.want)
		}
	}
}

func TestStockScalingBlocksStaleDriftAndOscillation(t *testing.T) {
	now := time.Unix(1700000000, 0)
	sample := func(start time.Time) StockScalingSample {
		return StockScalingSample{
			source: "pinned", revision: 2, sequence: 3, budget: 4,
			start: start, finish: start.Add(time.Second), expires: start.Add(time.Minute),
			pools:    map[string]scalingPool{"p": {repo: "r", enabled: true, current: 2, target: 3}},
			repoCaps: map[string]int64{"r": 4}, order: []string{"p"},
			configuredMaximumFit: roomyMaximumFit("p"),
		}
	}
	for name, change := range map[string]func(*StockScalingSample, *StockScalingSample){
		"replayed cycle": func(a, b *StockScalingSample) { b.start = a.start; b.finish = a.finish },
		"expired prior":  func(a, b *StockScalingSample) { a.expires = b.finish },
		"new source":     func(a, b *StockScalingSample) { a.source = "other" },
		"policy edit":    func(a, b *StockScalingSample) { b.revision++ },
		"ledger rewind":  func(a, b *StockScalingSample) { b.sequence-- },
		"outside scope":  func(a, b *StockScalingSample) { b.incomplete = true },
		"repo ceiling":   func(a, b *StockScalingSample) { a.repoCaps["r"] = 2; b.repoCaps["r"] = 2 },
		"outside charge": func(a, b *StockScalingSample) { b.outside = 2 },
		"settings drift": func(a, b *StockScalingSample) { p := a.pools["p"]; p.fingerprint = "other"; a.pools["p"] = p },
		"reversal":       func(a, b *StockScalingSample) { p := a.pools["p"]; p.target = 1; a.pools["p"] = p },
	} {
		t.Run(name, func(t *testing.T) {
			prior, current := sample(now), sample(now.Add(20*time.Second))
			change(&prior, &current)
			if got := ProposeStockMaximums(prior, current, current.finish); len(got) != 0 {
				t.Fatalf("unsafe proposal %v", got)
			}
		})
	}
}

func TestStockScalingRetainsIdleAndCharges(t *testing.T) {
	state, _, opt := ledgerFixture()
	snapshot, _, observed, _ := fixture()
	p := snapshot.Pools[pool]
	p.Budget = &snapshot.Global
	snapshot.Pools[pool] = p
	state.Config.Global.Runners = 5
	state.Policy.Repositories[0].MaxRunners = 4
	state.Policy.Repositories[0].Pools[0].MaxRunners = 4
	state.Policy.Repositories[0].Pools[0].WarmIdle = 2
	state.Config.Pools[pool] = admission.Pool{Budget: state.Config.Global, Hosts: []string{"host"}}
	state.Allocated.Global.Runners = 3
	state.Allocated.Pools = map[string]admission.Resources{pool: {Runners: 2}}
	snapshot.Charges = []capacitybroker.Charge{
		{Pool: pool, Resources: capacitybroker.Resources{0, 0, 0, 2}},
		{Resources: capacitybroker.Resources{0, 0, 0, 1}},
	}
	observed.Pools[0].MaxRunners = 4
	observed.Pools[0].MinIdleRunners = 3
	observed.Pools[0].DiscoveryFingerprint = "checked-settings"
	observed.Jobs = nil
	for _, c := range []struct {
		name         string
		left, target int
		disable      bool
	}{
		{"floor", 0, 3, false},
		{"unknown queued", 1, 4, false},
		{"disabled", 0, 4, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := capacitybroker.JobPlan{RemainingJobs: make([]capacitybroker.QueuedDemand, c.left)}
			observed.Pools[0].Enabled = !c.disable
			// This direct projection fixture exercises the candidate cap/floor
			// calculation independently of physical placement constraints.
			budget := capacitybroker.Resources{8000, 8000, 8000, 5}
			p := snapshot.Pools[pool]
			p.Budget = &budget
			snapshot.Pools[pool] = p
			sample, err := stockScalingSample(state, snapshot, observed, plan, "checked-source", opt)
			if err != nil || sample.pools[pool].target != int64(c.target) || sample.outside != 1 {
				t.Fatalf("sample = %+v, %v", sample, err)
			}
		})
	}
}

func TestStockScalingRejectsDetachedAccountingAndUnverifiedSource(t *testing.T) {
	state, _, opt := ledgerFixture()
	snapshot, _, observed, _ := fixture()
	if _, err := SampleStockScaling(state, nil, observed, "caller-supplied-source", opt); err == nil {
		t.Fatal("accepted observation without trusted source identity")
	}
	state.Allocated.Global.Runners = 1
	if _, err := stockScalingSample(state, snapshot, observed, capacitybroker.JobPlan{}, "checked-source", opt); err == nil {
		t.Fatal("accepted detached charge list inconsistent with ledger")
	}
}

func TestStockMaximumGrowthNeedsHeadroomInBothSamples(t *testing.T) {
	start := time.Unix(1700000000, 0)
	sample := func(at time.Time) StockScalingSample {
		return StockScalingSample{
			source: "pinned", revision: 2, sequence: 3, budget: 6,
			start: at, finish: at.Add(time.Second), expires: at.Add(time.Minute),
			pools: map[string]scalingPool{
				"growing":  {repo: "repo", enabled: true, current: 1, target: 2},
				"disabled": {repo: "repo", current: 3, target: 3},
			},
			repoCaps:             map[string]int64{"repo": 4},
			order:                []string{"growing"},
			configuredMaximumFit: roomyMaximumFit("growing"),
		}
	}
	for _, tc := range []struct {
		name       string
		edit       func(*StockScalingSample)
		wantGrowth bool
	}{
		{"stable room", func(s *StockScalingSample) { s.outside = 4 }, true},
		{"prior global occupied", func(s *StockScalingSample) { s.outside = 5 }, false},
		{"prior repository occupied", func(s *StockScalingSample) { p := s.pools["disabled"]; p.used = 3; s.pools["disabled"] = p }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior, current := sample(start), sample(start.Add(20*time.Second))
			current.outside = 4
			tc.edit(&prior)
			wantCount := 0
			if tc.wantGrowth {
				wantCount = 1
			}
			if got := ProposeStockMaximums(prior, current, current.finish); len(got) != wantCount || (len(got) > 0 && (got[0].PoolID != "growing" || got[0].Suggested != 2)) {
				t.Fatalf("matching growth direction is insufficient without headroom in both captures: %+v", got)
			}
		})
	}
}

func TestStockAdvisoryBaseAndMaximumPreviewAcceptLargeSinglePoolQueue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		creating bool
	}{
		{"idle", false}, {"creating", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _, _ := ledgerFixture()
			snapshot, _, observed, opt := fixture()
			const slots = 32
			snapshot.Global = capacitybroker.Resources{32000, 32000, 32000, slots}
			snapshot.Hosts["host"] = capacitybroker.Host{Budget: snapshot.Global, AcceptNew: true}
			p := snapshot.Pools[pool]
			p.Budget = &snapshot.Global
			total := admission.Resources{CPU: 32000, RAM: 32000, Disk: 32000, Runners: slots}
			state.Allocated = admission.Allocations{Global: total, Hosts: map[string]admission.Resources{"host": total}, Pools: map[string]admission.Resources{pool: total}}
			state.Config.Global = total
			h := state.Config.Hosts["host"]
			h.Budget = total
			state.Config.Hosts["host"] = h
			configured := state.Config.Pools[pool]
			configured.Budget = total
			state.Config.Pools[pool] = configured
			state.Policy.Repositories[0].MaxRunners = slots
			state.Policy.Repositories[0].Pools[0].MaxRunners = slots
			snapshot.Charges = []capacitybroker.Charge{{ID: "retained", Host: "host", Pool: pool, Resources: snapshot.Global}}
			observed.Pools[0].MaxRunners = slots
			observed.Pools[0].DiscoveryFingerprint = "reviewed-settings"
			if tc.creating {
				p.Inflight, p.PendingWarm = slots, slots
				observed.Supply[0].Creating = slots
			} else {
				observed.Supply[0].Idle = slots
			}
			snapshot.Pools[pool] = p
			observed.Jobs = make([]garmapi.QueuedJob, 1000)
			for i := range observed.Jobs {
				observed.Jobs[i] = garmapi.QueuedJob{
					WorkflowJobID: int64(i + 1), RepositoryID: repo,
					CreatedAt: opt.Now.Add(-time.Duration(i) * time.Second), CandidatePoolIDs: []string{pool},
				}
			}

			// These are the two actual pure stock planning calls in
			// planReader.read, without opening a store/network connection.
			base, err := stock(snapshot, state.Policy, observed, opt, true)
			if err != nil || len(base.Assignments) != 0 || len(base.RemainingJobs) != 968 ||
				len(base.CoveredByIdle)+len(base.DeferredByInflight) != slots ||
				(tc.creating && len(base.DeferredByInflight) != slots) {
				t.Fatalf("base stock planning: idle=%d inflight=%d remaining=%d assigned=%d err=%v",
					len(base.CoveredByIdle), len(base.DeferredByInflight), len(base.RemainingJobs), len(base.Assignments), err)
			}
			pressure := []PressureStatus{{Host: "host", State: "partial_sample_not_headroom", SampledAt: opt.Now}}
			preview, err := sampleStockMaximumPreview(state, snapshot, observed, "fixture-source", opt, pressure)
			if err != nil || preview.incomplete || preview.pools[pool].used != slots || preview.pools[pool].target != slots {
				t.Fatalf("chronological maximum preview did not retain full charge: %+v, %v", preview, err)
			}
		})
	}
}
