package brokerinput

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

const (
	allocatorRepoB = "33333333-3333-4333-8333-333333333333"
	allocatorPoolB = "44444444-4444-4444-8444-444444444444"
)

// allocatorFixture builds normalized observe-only inputs from the real
// enrolled-cycle fixture: a live local stock transport stamps the observation,
// and the ledger snapshot joins profiles, bindings and charges coherently.
func allocatorFixture(t *testing.T) (AllocatorInput, admission.BrokerGrantState) {
	t.Helper()
	state, observation, source, opt := enrolledCycleFixture(t)
	snapshot, _, err := enrolledPlanningSnapshot(state, nil, observation, source, opt)
	if err != nil {
		t.Fatal(err)
	}
	return AllocatorInput{
		Source:         source,
		LedgerSequence: state.Sequence,
		CapturedAt:     opt.Now,
		Snapshot:       snapshot,
		Policy:         state.Policy,
		Observation:    observation,
		Options:        opt,
		Weights:        map[string]uint32{repo: 1},
	}, state
}

func allocatorRepoFor(poolID string) string {
	if poolID == pool {
		return repo
	}
	return allocatorRepoB
}

// allocatorEnrollPool adds a repository/pool to every input view, mirroring
// how a managed observation reports an already-enrolled stock pool.
func allocatorEnrollPool(in *AllocatorInput, state *admission.BrokerGrantState, repoID, poolID, fingerprint string, weight uint32, size capacitybroker.Resources, repoMax, poolMax, warm int64) {
	cfg := state.Config.Pools[pool]
	cfg.Hosts = []string{"host"}
	cfg.Budget = admission.Resources{CPU: size[0] * poolMax, RAM: size[1] * poolMax, Disk: size[2] * poolMax, Runners: poolMax}
	state.Config.Pools[poolID] = cfg
	profile := state.PoolProfiles[pool]
	profile.Pool = poolID
	profile.Repository = repoID
	profile.PoolFingerprint = fingerprint
	profile.Authority.PoolID = poolID
	profile.Resources = admission.Resources{CPU: size[0], RAM: size[1], Disk: size[2], Runners: 1}
	state.PoolProfiles[poolID] = profile
	policy := in.Policy
	policy.Repositories = append(policy.Repositories, admission.BrokerRepositoryPolicy{
		ID: repoID, Weight: weight, MaxRunners: repoMax,
		Pools: []admission.BrokerPoolPolicy{{ID: poolID, MaxRunners: poolMax, WarmIdle: warm}},
	})
	in.Policy, state.Policy = policy, policy
	in.Observation.Pools = append(in.Observation.Pools, garmapi.DiscoveredPool{
		ManagedPool:          garmapi.ManagedPool{ID: poolID, RepositoryID: repoID, Enabled: true, ProviderName: "capacity"},
		OwnerKind:            "repository",
		OwnerID:              repoID,
		OwnershipFingerprint: fingerprint,
	})
	in.Observation.Supply = append(in.Observation.Supply, garmapi.RunnerSupply{PoolID: poolID})
	if in.Weights == nil {
		in.Weights = map[string]uint32{}
	}
	in.Weights[repoID] = weight
}

func allocatorQueueJob(in *AllocatorInput, repoID, poolID string, jobID int64, createdAt time.Time) {
	in.Observation.Jobs = append(in.Observation.Jobs, garmapi.QueuedJob{
		WorkflowJobID:    jobID,
		RepositoryID:     repoID,
		CreatedAt:        createdAt,
		CandidatePoolIDs: []string{poolID},
	})
}

func allocatorCharge(id, host, poolID string, r capacitybroker.Resources) admission.BrokerPlanningCharge {
	return admission.BrokerPlanningCharge{ID: id, Host: host, Pool: poolID,
		Resources: admission.Resources{CPU: r[0], RAM: r[1], Disk: r[2], Runners: r[3]}}
}

// allocatorObserveIdleRunner models one verified idle runner exactly the way
// the canonical ledger does: a consumed grant bound to a reservation and
// execution plan, an observed stock row, and the retained charge. Observed
// runners without this backing cannot pass the stock join at all.
func allocatorObserveIdleRunner(in *AllocatorInput, state *admission.BrokerGrantState, poolID, rowID, name string, r capacitybroker.Resources) admission.BrokerPlanningCharge {
	key := "key-" + rowID
	if state.Reservations == nil {
		state.Reservations = map[string]admission.Reservation{}
	}
	if state.ExecutionPlans == nil {
		state.ExecutionPlans = map[string]admission.ExecutionPlan{}
	}
	req := admission.Request{Key: key, Pool: poolID, Host: "host", Resources: heldResources(r)}
	state.Reservations[key] = admission.Reservation{Request: req, State: "active", CreatedAt: in.Options.Now}
	state.ExecutionPlans[key] = admission.ExecutionPlan{Request: req, HostBindingSequence: 4,
		Execution: admission.ExecutionImage{Controller: "controller", Name: name, Flavor: "small", Image: "image", Type: "container", Arch: "amd64"}}
	state.Grants = append(state.Grants, admission.BrokerGrant{ID: "grant-" + rowID, State: "consumed", RequestKey: key,
		Spec: admission.BrokerGrantSpec{
			Authority:           admission.StockProviderAuthority{Controller: "controller", PoolID: poolID, ProviderName: "capacity"},
			Host:                "host",
			HostBindingSequence: 4,
			Resources:           heldResources(r),
			ExpiresAt:           in.Options.Now.Add(time.Minute),
		}})
	in.Observation.Runners = append(in.Observation.Runners, garmapi.ObservedRunner{RowID: rowID, Name: name, ProviderName: "capacity", PoolID: poolID, Kind: "idle"})
	for i := range in.Observation.Supply {
		if in.Observation.Supply[i].PoolID == poolID {
			in.Observation.Supply[i].Idle++
		}
	}
	return allocatorCharge("reservation:"+key, "host", poolID, r)
}

// allocatorRebuild recomputes the normalized snapshot from durable state,
// exactly what a restart or the next tick does. Awards themselves are not
// durable state: the caller translates them into retained charges first.
func allocatorRebuild(t *testing.T, state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, in *AllocatorInput) {
	t.Helper()
	snapshot, _, err := enrolledPlanningSnapshot(state, charges, in.Observation, in.Source, in.Options)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot = snapshot
}

func allocatorDecision(proposal AllocatorProposal, poolID string) AllocatorPoolDecision {
	for _, d := range proposal.Decisions {
		if d.Pool == poolID {
			return d
		}
	}
	return AllocatorPoolDecision{}
}

func allocatorSetCaps(in *AllocatorInput, state *admission.BrokerGrantState, repoMax, poolMax, warm int64) {
	for i := range in.Policy.Repositories {
		if in.Policy.Repositories[i].ID == repo {
			in.Policy.Repositories[i].MaxRunners = repoMax
			in.Policy.Repositories[i].Pools[0].MaxRunners = poolMax
			in.Policy.Repositories[i].Pools[0].WarmIdle = warm
		}
	}
	state.Policy = in.Policy
}

func allocatorSetBudgets(state *admission.BrokerGrantState, cpu, ram, disk, runners int64) {
	set := func(r *admission.Resources) {
		r.CPU, r.RAM, r.Disk, r.Runners = cpu, ram, disk, runners
	}
	set(&state.Config.Global)
	host := state.Config.Hosts["host"]
	set(&host.Budget)
	state.Config.Hosts["host"] = host
	for id, pool := range state.Config.Pools {
		set(&pool.Budget)
		state.Config.Pools[id] = pool
	}
}

func allocatorRemoveJobsFor(in *AllocatorInput, poolID string) {
	kept := in.Observation.Jobs[:0]
	for _, job := range in.Observation.Jobs {
		candidate := false
		for _, id := range job.CandidatePoolIDs {
			if id == poolID {
				candidate = true
			}
		}
		if !candidate {
			kept = append(kept, job)
		}
	}
	in.Observation.Jobs = kept
}

func TestAllocatorProposalExplainsQueuedAward(t *testing.T) {
	in, _ := allocatorFixture(t)
	proposal, err := PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.SourceIdentity != in.Source || proposal.LedgerSequence != in.LedgerSequence ||
		proposal.PolicyRevision != in.Policy.Revision || proposal.DemandDigest != in.Observation.Digest {
		t.Fatalf("input version not bound: %+v", proposal)
	}
	if proposal.InputRevision != allocatorEvidenceRevision(in, allocatorInputRevision(in.Snapshot.Revision, in.Policy.Revision, in.Observation.Digest)) {
		t.Fatal("input revision does not rebind snapshot, policy, demand, route and pressure evidence")
	}
	if proposal.State != allocatorStateHypothetical {
		t.Fatalf("unestablished growth must stay hypothetical: %+v", proposal)
	}
	if !proposal.ExpiresAt.Equal(in.Observation.StartedAt.Add(in.Options.MaxAge)) {
		t.Fatalf("expiry %v; want demand-start bounded freshness", proposal.ExpiresAt)
	}
	if len(proposal.Awards) != 1 {
		t.Fatalf("awards %+v", proposal.Awards)
	}
	award := proposal.Awards[0]
	if award.Pool != pool || award.Repository != repo || award.Host != "host" || award.Warm ||
		award.Reason != allocatorReasonQueuedDemand || award.DestinationEstablished {
		t.Fatalf("award %+v", award)
	}
	if award.Resources.CPU != 1000 || award.Resources.RAM != 1000 || award.Resources.Disk != 1000 || award.Resources.Runners != 1 {
		t.Fatalf("budgeted runner profile not preserved: %+v", award.Resources)
	}
	if proposal.Held.Global.Runners != 0 || proposal.Held.Commitments != 0 || len(proposal.Held.Pools) != 0 {
		t.Fatalf("held %+v", proposal.Held)
	}
	decision := allocatorDecision(proposal, pool)
	if decision.Reason != allocatorDecisionProposed || decision.RepositoryWeight != 1 ||
		decision.ChargedRunners != 0 || decision.RemainingQueued != 0 || decision.Additional != 1 {
		t.Fatalf("decision %+v", decision)
	}
	risks := map[string]bool{}
	for _, risk := range proposal.Risks {
		risks[risk] = true
	}
	if !risks[allocatorRiskNonAtomicObservation] || !risks[allocatorRiskDestinationUnestablished] {
		t.Fatalf("risks %+v", proposal.Risks)
	}
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip AllocatorProposal
	if json.Unmarshal(raw, &roundTrip) != nil || !reflect.DeepEqual(proposal, roundTrip) {
		t.Fatal("proposal not stable through its operator JSON encoding")
	}
}

func TestAllocatorSingleSlotTwoRepositoriesExclusive(t *testing.T) {
	in, state := allocatorFixture(t)
	size := in.Snapshot.Pools[pool].Size
	allocatorEnrollPool(&in, &state, allocatorRepoB, allocatorPoolB, "fingerprint-b", 1, size, 1, 1, 0)
	allocatorSetCaps(&in, &state, 1, 1, 0)
	allocatorSetBudgets(&state, size[0], size[1], size[2], 1)
	allocatorQueueJob(&in, allocatorRepoB, allocatorPoolB, 2, in.Options.Now)
	charges := []admission.BrokerPlanningCharge{}
	allocatorRebuild(t, state, charges, &in)

	proposal, err := PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Awards) != 1 || proposal.Awards[0].Resources.Runners != 1 {
		t.Fatalf("one retained slot awarded more than once: %+v", proposal.Awards)
	}
	winner := proposal.Awards[0]
	loser := allocatorPoolB
	if winner.Pool != pool {
		loser = pool
	}
	if got := allocatorDecision(proposal, loser); got.RemainingQueued != 1 || got.Reason != allocatorDecisionBlockedGlobal {
		t.Fatalf("loser must see the consumed slot: %+v", got)
	}
	first := proposal

	// Overlapping ticks: any number of concurrent recomputations from the same
	// durable inputs must agree exactly, so no pair of ticks can hand the same
	// slot to different repositories.
	var wg sync.WaitGroup
	results := make([]AllocatorProposal, 16)
	errs := make([]error, 16)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = PlanAllocatorProposal(in)
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || !reflect.DeepEqual(first, results[i]) {
			t.Fatalf("overlapping tick %d disagreed: %+v %v", i, results[i], errs[i])
		}
	}

	// Restart: rebuilding normalized inputs from the same durable state yields
	// the identical binding proposal.
	allocatorRebuild(t, state, charges, &in)
	restarted, err := PlanAllocatorProposal(in)
	if err != nil || !reflect.DeepEqual(first, restarted) {
		t.Fatalf("restart changed the proposal: %+v %v", restarted, err)
	}

	// The award is applied by the stock lifecycle as a retained physical
	// charge. While it is retained the competitor cannot receive the slot;
	// only authoritative reconciliation releases it, and then the competitor
	// is served. Both repositories never hold the same slot.
	charges = append(charges, allocatorCharge("applied:"+winner.Pool, winner.Host, winner.Pool, size))
	allocatorRemoveJobsFor(&in, winner.Pool)
	allocatorRebuild(t, state, charges, &in)
	retained, err := PlanAllocatorProposal(in)
	if err != nil || len(retained.Awards) != 0 {
		t.Fatalf("retained charge funded the competitor: %+v %v", retained.Awards, err)
	}
	if got := allocatorDecision(retained, loser); got.Reason != allocatorDecisionBlockedGlobal {
		t.Fatalf("retention not explained: %+v", got)
	}
	if retained.Held.Global.Runners != 1 || retained.Held.Pools[winner.Pool].Runners != 1 {
		t.Fatalf("applied award not held: %+v", retained.Held)
	}
	charges = charges[:0]
	allocatorRebuild(t, state, charges, &in)
	second, err := PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Awards) != 1 || second.Awards[0].Pool != loser {
		t.Fatalf("competitor did not receive the released slot: %+v", second.Awards)
	}
}

func TestAllocatorFairServiceAlternatesAtEqualWeights(t *testing.T) {
	in, state := allocatorFixture(t)
	size := in.Snapshot.Pools[pool].Size
	allocatorEnrollPool(&in, &state, allocatorRepoB, allocatorPoolB, "fingerprint-b", 1, size, 1, 1, 0)
	allocatorSetCaps(&in, &state, 1, 1, 0)
	allocatorSetBudgets(&state, size[0], size[1], size[2], 1)
	charges := []admission.BrokerPlanningCharge{}
	allocatorRebuild(t, state, charges, &in)

	// Deterministic tie-breaking: the older queued job wins the single slot;
	// reversing ages flips the winner.
	allocatorQueueJob(&in, allocatorRepoB, allocatorPoolB, 2, in.Options.Now)
	proposal, err := PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Pool != pool {
		t.Fatalf("oldest waiting must win the tie: %+v %v", proposal.Awards, err)
	}
	in.Observation.Jobs[1].CreatedAt = in.Observation.Jobs[0].CreatedAt.Add(-time.Second)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Pool != allocatorPoolB {
		t.Fatalf("age reversal must flip the winner deterministically: %+v %v", proposal.Awards, err)
	}

	// Sustained demand receives fair service: A, then B, then A again. Each
	// award becomes a retained busy charge that funds nothing new; only the
	// fixture's authoritative reconciliation (never a timeout or a missing
	// list entry) releases it, and then the competitor is served.
	in.Observation.Jobs = nil
	allocatorQueueJob(&in, repo, pool, 1, in.Options.Now.Add(-time.Second))
	allocatorQueueJob(&in, allocatorRepoB, allocatorPoolB, 2, in.Options.Now)
	order := []string{}
	other := func(winner string) (string, string) {
		if winner == pool {
			return allocatorRepoB, allocatorPoolB
		}
		return repo, pool
	}
	for round := 0; round < 3; round++ {
		proposal, err = PlanAllocatorProposal(in)
		if err != nil || len(proposal.Awards) != 1 {
			t.Fatalf("round %d: %+v %v", round, proposal.Awards, err)
		}
		winner := proposal.Awards[0]
		order = append(order, winner.Repository)
		otherRepo, otherPool := other(winner.Pool)
		// The award is applied: a retained busy charge, the covered job leaves
		// the queue, and fresh demand arrives for the other repository.
		charges = append(charges, allocatorCharge("applied-"+winner.Pool, winner.Host, winner.Pool, size))
		allocatorRemoveJobsFor(&in, winner.Pool)
		allocatorQueueJob(&in, otherRepo, otherPool, int64(10+round), in.Options.Now.Add(-time.Second))
		allocatorRebuild(t, state, charges, &in)
		// The busy runner is charged: the remaining demand gets nothing.
		blocked, err := PlanAllocatorProposal(in)
		if err != nil || len(blocked.Awards) != 0 {
			t.Fatalf("round %d: busy charge funded a second award: %+v %v", round, blocked.Awards, err)
		}
		if got := allocatorDecision(blocked, otherPool); got.Reason != allocatorDecisionBlockedGlobal {
			t.Fatalf("round %d: retained charge not explained: %+v", round, got)
		}
		// Authoritative reconciliation (never a timeout or a missing list
		// entry) releases the completed runner before the next tick.
		charges = charges[:0]
		allocatorRebuild(t, state, charges, &in)
	}
	if !reflect.DeepEqual(order, []string{repo, allocatorRepoB, repo}) {
		t.Fatalf("fair service order %v", order)
	}
}

func TestAllocatorWeightsShiftPreferenceWithinHardCaps(t *testing.T) {
	in, state := allocatorFixture(t)
	size := in.Snapshot.Pools[pool].Size
	allocatorEnrollPool(&in, &state, allocatorRepoB, allocatorPoolB, "fingerprint-b", 3, size, 2, 2, 0)
	allocatorSetCaps(&in, &state, 2, 2, 0)
	allocatorSetBudgets(&state, 3*size[0], 3*size[1], 3*size[2], 3)
	allocatorQueueJob(&in, repo, pool, 2, in.Options.Now.Add(-time.Second))
	allocatorQueueJob(&in, allocatorRepoB, allocatorPoolB, 3, in.Options.Now)
	charges := []admission.BrokerPlanningCharge{
		allocatorCharge("existing-a", "host", pool, size),
		allocatorCharge("existing-b", "host", allocatorPoolB, size),
	}
	allocatorRebuild(t, state, charges, &in)

	// Equal retained usage: the weight-3 repository has the lower dominant
	// share and receives the last slot.
	proposal, err := PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Pool != allocatorPoolB {
		t.Fatalf("weight did not shift preference: %+v %v", proposal.Awards, err)
	}
	if got := allocatorDecision(proposal, allocatorPoolB); got.RepositoryWeight != 3 {
		t.Fatalf("configured weight not explained: %+v", got)
	}

	// Hard caps are not bypassed: capping the heavy repository at its retained
	// usage gives the slot to the light repository instead.
	for i := range in.Policy.Repositories {
		if in.Policy.Repositories[i].ID == allocatorRepoB {
			in.Policy.Repositories[i].MaxRunners = 1
			in.Policy.Repositories[i].Pools[0].MaxRunners = 1
		}
	}
	state.Policy = in.Policy
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Pool != pool {
		t.Fatalf("hard cap bypassed by weight: %+v %v", proposal.Awards, err)
	}
	if got := allocatorDecision(proposal, allocatorPoolB); got.Reason != allocatorDecisionBlockedPoolMax {
		t.Fatalf("capped repository reason: %+v", got)
	}

	// Both repositories at their cap: no award at all, with inspectable codes.
	for i := range in.Policy.Repositories {
		in.Policy.Repositories[i].MaxRunners = 1
		in.Policy.Repositories[i].Pools[0].MaxRunners = 1
	}
	state.Policy = in.Policy
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 0 {
		t.Fatalf("awards over hard caps: %+v %v", proposal.Awards, err)
	}
	if got := allocatorDecision(proposal, pool); got.Reason != allocatorDecisionBlockedPoolMax {
		t.Fatalf("capped pool reason: %+v", got)
	}
	if got := allocatorDecision(proposal, allocatorPoolB); got.Reason != allocatorDecisionBlockedPoolMax {
		t.Fatalf("capped pool reason: %+v", got)
	}
}

func TestAllocatorRejectsDetachedStaleOrCrossedInputs(t *testing.T) {
	in, _ := allocatorFixture(t)
	if _, err := PlanAllocatorProposal(in); err != nil {
		t.Fatal(err)
	}

	// Wrong source: the observation was stamped by a different stock source.
	wrong := in
	wrong.Source = "another-source"
	if _, err := PlanAllocatorProposal(wrong); err != capacitybroker.ErrSnapshot {
		t.Fatal("wrong-source observation awarded growth", err)
	}

	// Detached observation: identical public fields without the private stamp.
	detached := in
	detached.Observation = garmapi.DemandObservation{
		StartedAt:  in.Observation.StartedAt,
		FinishedAt: in.Observation.FinishedAt,
		Digest:     in.Observation.Digest,
		Pools:      in.Observation.Pools,
		Supply:     in.Observation.Supply,
		Jobs:       in.Observation.Jobs,
	}
	if _, err := PlanAllocatorProposal(detached); err != capacitybroker.ErrSnapshot {
		t.Fatal("detached demand awarded growth", err)
	}

	// Stale freshness: the capacity sample outlived MaxAge.
	stale := in
	opt := stale.Options
	opt.Now = opt.Now.Add(2 * stale.Options.MaxAge)
	stale.Options = opt
	if _, err := PlanAllocatorProposal(stale); err != capacitybroker.ErrSnapshot {
		t.Fatal("stale snapshot awarded growth", err)
	}

	// Crossed plan: computed from a different demand digest than the input.
	tampered := in
	tampered.Observation.Digest = "changed-after-planning"
	plan, err := advisoryStock(in.Snapshot, in.Policy, tampered.Observation, in.Options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExplainAllocatorPlan(in, plan, in.Observation.StartedAt.Add(in.Options.MaxAge)); err != capacitybroker.ErrSnapshot {
		t.Fatal("crossed plan accepted against foreign inputs", err)
	}

	// Expired advice must not be explained as if still fresh.
	if _, err := ExplainAllocatorPlan(in, planFor(t, in), in.Options.Now.Add(-time.Second)); err != capacitybroker.ErrSnapshot {
		t.Fatal("expired plan accepted", err)
	}
}

func planFor(t *testing.T, in AllocatorInput) capacitybroker.JobPlan {
	t.Helper()
	plan, err := advisoryStock(in.Snapshot, in.Policy, in.Observation, in.Options, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestAllocatorRoutedDestinationEstablished(t *testing.T) {
	state, snapshot, observation, opt, routes := routedStockFixture(t)
	routes[0].Host, routes[0].HostBindingSequence = "b", 4
	in := AllocatorInput{
		Source:      observation.SourceIdentity(),
		Snapshot:    snapshot,
		Policy:      state.Policy,
		Observation: observation,
		Options:     opt,
		CapturedAt:  opt.Now,
		Weights:     map[string]uint32{repo: 1},
	}
	proposal, err := PlanRoutedAllocatorProposal(state, in, routes)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.State != allocatorStateVerified {
		t.Fatalf("routed growth must be verified: %+v", proposal)
	}
	if !proposal.ExpiresAt.Equal(routes[0].ObservedAt.Add(opt.MaxAge)) {
		t.Fatalf("route freshness not bound: %v", proposal.ExpiresAt)
	}
	if len(proposal.Awards) != 1 || proposal.Awards[0].Host != "b" || !proposal.Awards[0].DestinationEstablished {
		t.Fatalf("verified destination not established: %+v", proposal.Awards)
	}
	for _, risk := range proposal.Risks {
		if risk == allocatorRiskDestinationUnestablished {
			t.Fatal("established destinations reported unestablished")
		}
	}

	// An eligible host list alone never establishes placement: the ordinary
	// planner may name a fitting host, but the award stays explicitly
	// hypothetical and carries the matching risk.
	unrouted, err := PlanAllocatorProposal(in)
	if err != nil || len(unrouted.Awards) != 1 || unrouted.Awards[0].Host != "b" || unrouted.Awards[0].DestinationEstablished {
		t.Fatalf("eligible host list established placement: %+v %v", unrouted.Awards, err)
	}
	if unrouted.State != allocatorStateHypothetical {
		t.Fatalf("unestablished growth must stay hypothetical: %+v", unrouted)
	}
	found := false
	for _, risk := range unrouted.Risks {
		if risk == allocatorRiskDestinationUnestablished {
			found = true
		}
	}
	if !found {
		t.Fatalf("unestablished risk missing: %+v", unrouted.Risks)
	}

	// Conflicting, stale or incomplete route evidence awards nothing at all.
	conflictRoutes := append([]VerifiedStockProviderRoute(nil), routes...)
	conflictRoutes[0].Host = "a"
	if _, err := PlanRoutedAllocatorProposal(state, in, conflictRoutes); err != capacitybroker.ErrSnapshot {
		t.Fatal("conflicting route awarded growth", err)
	}
	if _, err := PlanRoutedAllocatorProposal(state, in, nil); err != capacitybroker.ErrSnapshot {
		t.Fatal("missing route awarded growth", err)
	}
	expiredRoutes := append([]VerifiedStockProviderRoute(nil), routes...)
	expiredRoutes[0].ObservedAt = opt.Now.Add(-opt.MaxAge)
	if _, err := PlanRoutedAllocatorProposal(state, in, expiredRoutes); err != capacitybroker.ErrSnapshot {
		t.Fatal("stale route awarded growth", err)
	}
}

func TestAllocatorMixedSizesFitBudgetsIndependently(t *testing.T) {
	in, state := allocatorFixture(t)
	light := in.Snapshot.Pools[pool].Size
	heavy := capacitybroker.Resources{1000, 7000, 1000, 1}
	allocatorEnrollPool(&in, &state, allocatorRepoB, allocatorPoolB, "fingerprint-b", 1, heavy, 2, 2, 0)
	allocatorSetBudgets(&state, 4000, 8000, 4000, 2)
	allocatorQueueJob(&in, allocatorRepoB, allocatorPoolB, 2, in.Options.Now)
	charges := []admission.BrokerPlanningCharge{}
	allocatorRebuild(t, state, charges, &in)

	// Both the light and the custom heavy profile fit their own dimensions.
	proposal, err := PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 2 {
		t.Fatalf("mixed sizes must fit independently: %+v %v", proposal.Awards, err)
	}
	byPool := map[string]admission.Resources{}
	for _, award := range proposal.Awards {
		byPool[award.Pool] = award.Resources
	}
	if byPool[pool] != (admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1}) ||
		byPool[allocatorPoolB] != (admission.Resources{CPU: 1000, RAM: 7000, Disk: 1000, Runners: 1}) {
		t.Fatalf("budgeted profiles not preserved per pool: %+v", byPool)
	}

	// A RAM ceiling stops only the heavy profile; the light one still fits.
	allocatorSetBudgets(&state, 4000, 6000, 4000, 2)
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Pool != pool {
		t.Fatalf("RAM ceiling must not stop the fitting profile: %+v %v", proposal.Awards, err)
	}
	if got := allocatorDecision(proposal, allocatorPoolB); got.Reason != allocatorDecisionBlockedGlobal {
		t.Fatalf("heavy profile reason: %+v", got)
	}

	// A disk margin warning (measured-usage veto) is not a per-runner disk
	// quota: it blocks new placements on the host while every awarded profile
	// keeps its full configured disk, and it never relaxes disk fitting.
	in.Blocked = map[string]bool{"host": true}
	allocatorSetBudgets(&state, 4000, 8000, 4000, 2)
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 0 {
		t.Fatalf("vetoed host funded an increase: %+v %v", proposal.Awards, err)
	}
	if got := allocatorDecision(proposal, allocatorPoolB); got.Reason != allocatorDecisionBlockedPressure {
		t.Fatalf("pressure reason: %+v", got)
	}
	risks := map[string]bool{}
	for _, risk := range proposal.Risks {
		risks[risk] = true
	}
	if !risks[allocatorRiskHostPressureSuppressed] {
		t.Fatalf("pressure risk missing: %+v", proposal.Risks)
	}
	in.Blocked = nil
	heavyPool := in.Snapshot.Pools[allocatorPoolB]
	heavyPool.Size = capacitybroker.Resources{1000, 1000, 9000, 1}
	in.Snapshot.Pools[allocatorPoolB] = heavyPool
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Resources.Disk != 1000 {
		t.Fatalf("per-runner disk quota invented or profile shrunk: %+v %v", proposal.Awards, err)
	}
	if got := allocatorDecision(proposal, allocatorPoolB); got.Reason != allocatorDecisionBlockedGlobal {
		t.Fatalf("over-quota disk profile must not fit: %+v", got)
	}
	_ = light
}

func TestAllocatorZeroIdleAndWarmFloorsPredictable(t *testing.T) {
	in, state := allocatorFixture(t)
	size := in.Snapshot.Pools[pool].Size
	charges := []admission.BrokerPlanningCharge{}

	// A quiet pool targets zero idle by default.
	in.Observation.Jobs = nil
	allocatorRebuild(t, state, charges, &in)
	proposal, err := PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 0 {
		t.Fatalf("quiet zero-idle pool grew: %+v %v", proposal.Awards, err)
	}
	if got := allocatorDecision(proposal, pool); got.Reason != allocatorDecisionQuiet || got.AdditionalWarm != 0 {
		t.Fatalf("quiet decision: %+v", got)
	}

	// A configured warm floor proposes exactly the missing spare count.
	allocatorSetCaps(&in, &state, 4, 4, 2)
	allocatorSetBudgets(&state, 4*size[0], 4*size[1], 4*size[2], 4)
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 2 {
		t.Fatalf("warm floor: %+v %v", proposal.Awards, err)
	}
	for _, award := range proposal.Awards {
		if !award.Warm || award.Reason != allocatorReasonWarmFloor {
			t.Fatalf("warm award: %+v", award)
		}
	}
	if got := allocatorDecision(proposal, pool); got.Reason != allocatorDecisionProposed || got.AdditionalWarm != 2 {
		t.Fatalf("warm decision: %+v", got)
	}

	// Zero idle with demand: exactly the queued jobs, never warm extras.
	allocatorSetCaps(&in, &state, 2, 2, 0)
	allocatorSetBudgets(&state, 2*size[0], 2*size[1], 2*size[2], 2)
	allocatorQueueJob(&in, repo, pool, 7, in.Options.Now.Add(-time.Second))
	allocatorQueueJob(&in, repo, pool, 8, in.Options.Now)
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 2 {
		t.Fatalf("zero-idle demand: %+v %v", proposal.Awards, err)
	}
	for _, award := range proposal.Awards {
		if award.Warm || award.Reason != allocatorReasonQueuedDemand {
			t.Fatalf("unexpected warm extra: %+v", award)
		}
	}

	// Existing verified idle supply satisfies a quiet warm floor: no new award
	// is funded while the spare idle count already meets the floor.
	in, state = allocatorFixture(t)
	allocatorSetCaps(&in, &state, 2, 2, 1)
	in.Observation.Jobs = nil
	charges = []admission.BrokerPlanningCharge{allocatorObserveIdleRunner(&in, &state, pool, "row-1", "runner-1", size)}
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Awards) != 0 {
		t.Fatalf("existing idle must satisfy the floor: %+v", proposal.Awards)
	}
	if got := allocatorDecision(proposal, pool); got.AdditionalWarm != 0 || got.Reason != allocatorDecisionQuiet {
		t.Fatalf("floor satisfaction not explained: %+v", got)
	}

	// With queued demand, the idle runner covers the job and the floor is
	// backfilled with exactly one warm award: predictable, never more.
	in, state = allocatorFixture(t)
	allocatorSetCaps(&in, &state, 2, 2, 1)
	charges = []admission.BrokerPlanningCharge{allocatorObserveIdleRunner(&in, &state, pool, "row-2", "runner-2", size)}
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Awards) != 1 || !proposal.Awards[0].Warm || proposal.Awards[0].Reason != allocatorReasonWarmFloor {
		t.Fatalf("floor backfill: %+v", proposal.Awards)
	}
	if got := allocatorDecision(proposal, pool); got.CoveredByIdle != 1 || got.AdditionalWarm != 1 {
		t.Fatalf("idle coverage not explained: %+v", got)
	}
}

func TestAllocatorExplainsUnmetWarmFloor(t *testing.T) {
	t.Run("competing queue takes the last slot", func(t *testing.T) {
		in, state := allocatorFixture(t)
		size := in.Snapshot.Pools[pool].Size
		in.Observation.Jobs = nil
		allocatorEnrollPool(&in, &state, allocatorRepoB, allocatorPoolB, "fingerprint-b", 1, size, 2, 2, 0)
		allocatorSetCaps(&in, &state, 2, 2, 1)
		allocatorSetBudgets(&state, size[0], size[1], size[2], 1)
		allocatorQueueJob(&in, allocatorRepoB, allocatorPoolB, 2, in.Options.Now)
		allocatorRebuild(t, state, nil, &in)
		proposal, err := PlanAllocatorProposal(in)
		if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Pool != allocatorPoolB || proposal.Awards[0].Warm {
			t.Fatalf("queued job must get the last slot before the quiet floor: %+v %v", proposal.Awards, err)
		}
		if got := allocatorDecision(proposal, pool); got.RemainingQueued != 0 || got.AdditionalWarm != 0 || got.Reason != allocatorDecisionBlockedGlobal {
			t.Fatalf("unfunded floor needs a budget explanation: %+v", got)
		}
	})
	t.Run("floor partly funded", func(t *testing.T) {
		in, state := allocatorFixture(t)
		size := in.Snapshot.Pools[pool].Size
		in.Observation.Jobs = nil
		allocatorSetCaps(&in, &state, 2, 2, 2)
		allocatorSetBudgets(&state, size[0], size[1], size[2], 1)
		allocatorRebuild(t, state, nil, &in)
		proposal, err := PlanAllocatorProposal(in)
		if err != nil || len(proposal.Awards) != 1 || !proposal.Awards[0].Warm {
			t.Fatalf("expected one funded warm slot only: %+v %v", proposal.Awards, err)
		}
		if got := allocatorDecision(proposal, pool); got.AdditionalWarm != 1 || got.Reason != allocatorDecisionPartial {
			t.Fatalf("partial floor must not look fully funded: %+v", got)
		}
	})
	t.Run("idle already spent on a job", func(t *testing.T) {
		in, state := allocatorFixture(t)
		size := in.Snapshot.Pools[pool].Size
		allocatorSetCaps(&in, &state, 2, 2, 1)
		allocatorSetBudgets(&state, size[0], size[1], size[2], 1)
		charges := []admission.BrokerPlanningCharge{allocatorObserveIdleRunner(&in, &state, pool, "row-1", "runner-1", size)}
		allocatorRebuild(t, state, charges, &in)
		proposal, err := PlanAllocatorProposal(in)
		if err != nil || len(proposal.Awards) != 0 {
			t.Fatalf("the assigned idle runner already takes the only slot: %+v %v", proposal.Awards, err)
		}
		if got := allocatorDecision(proposal, pool); got.CoveredByIdle != 1 || got.RemainingQueued != 0 || got.Reason != allocatorDecisionBlockedGlobal {
			t.Fatalf("the same runner cannot cover the queued job and the spare floor: %+v", got)
		}
	})
}

func TestAllocatorGrowthNeverTreatsLowerMaxUnknownOrMissingAsFree(t *testing.T) {
	in, state := allocatorFixture(t)
	size := in.Snapshot.Pools[pool].Size

	// A lowered pool maximum never removes retained unknown charges.
	charges := []admission.BrokerPlanningCharge{
		allocatorCharge("unknown-1", "host", pool, size),
		allocatorCharge("unknown-2", "host", pool, size),
	}
	allocatorSetCaps(&in, &state, 2, 1, 0)
	allocatorRebuild(t, state, charges, &in)
	proposal, err := PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 0 {
		t.Fatalf("lowered maximum erased charges: %+v %v", proposal.Awards, err)
	}
	if proposal.Held.Global.Runners != 2 || proposal.Held.Pools[pool].Runners != 2 {
		t.Fatalf("retention lost: %+v", proposal.Held)
	}

	// Unknown occupancy of the only slot funds no increase.
	allocatorSetCaps(&in, &state, 2, 2, 0)
	allocatorSetBudgets(&state, size[0], size[1], size[2], 1)
	allocatorQueueJob(&in, repo, pool, 9, in.Options.Now)
	allocatorRebuild(t, state, charges[:1], &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 0 {
		t.Fatalf("unknown state funded growth: %+v %v", proposal.Awards, err)
	}
	if got := allocatorDecision(proposal, pool); got.Reason != allocatorDecisionBlockedGlobal {
		t.Fatalf("unknown occupancy reason: %+v", got)
	}

	// A missing pool observation fails closed instead of reading as empty: the
	// normalized input join refuses it before any planning runs.
	in.Observation.Pools = in.Observation.Pools[:0]
	if _, _, err := enrolledPlanningSnapshot(state, charges[:1], in.Observation, in.Source, in.Options); err != capacitybroker.ErrSnapshot {
		t.Fatal("missing observation joined as empty capacity", err)
	}
	if _, err := PlanAllocatorProposal(in); err != capacitybroker.ErrSnapshot {
		t.Fatal("missing observation awarded growth", err)
	}

	// A conflicting duplicate observation fails closed as well.
	in, state = allocatorFixture(t)
	allocatorRebuild(t, state, nil, &in)
	in.Observation.Pools = append(in.Observation.Pools, in.Observation.Pools[0])
	if _, err := PlanAllocatorProposal(in); err != capacitybroker.ErrSnapshot {
		t.Fatal("conflicting observation awarded growth", err)
	}

	// Paused and unhealthy hosts never fund an increase.
	in, state = allocatorFixture(t)
	host := state.Config.Hosts["host"]
	host.Mode = "paused"
	state.Config.Hosts["host"] = host
	allocatorRebuild(t, state, nil, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil || len(proposal.Awards) != 0 {
		t.Fatalf("paused host funded growth: %+v %v", proposal.Awards, err)
	}
	in, state = allocatorFixture(t)
	host = state.Config.Hosts["host"]
	host.Healthy = false
	state.Config.Hosts["host"] = host
	allocatorRebuild(t, state, nil, &in)
	if proposal, err = PlanAllocatorProposal(in); err != nil || len(proposal.Awards) != 0 {
		t.Fatalf("unhealthy host funded growth: %+v %v", proposal.Awards, err)
	}
}

func TestAllocatorOutstandingCommitmentsHeldBeforeAwards(t *testing.T) {
	in, _ := allocatorFixture(t)
	size := in.Snapshot.Pools[pool].Size
	in.Snapshot.Charges = append(in.Snapshot.Charges, capacitybroker.Charge{ID: "grant-1", Host: "host", Pool: pool, Resources: size})
	poolState := in.Snapshot.Pools[pool]
	poolState.PendingWarm = 1
	poolState.OutstandingGrants = 1
	in.Snapshot.Pools[pool] = poolState
	in.Commitments = 1

	// The outstanding durable commitment covers the queued demand; no second
	// award is funded on top of it and demand signals stay separate from the
	// retained charge.
	proposal, err := PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Awards) != 0 {
		t.Fatalf("commitment double-funded: %+v", proposal.Awards)
	}
	if proposal.Held.Commitments != 1 || proposal.Held.Global.Runners != 1 {
		t.Fatalf("commitments not held: %+v", proposal.Held)
	}
	decision := allocatorDecision(proposal, pool)
	if decision.CoveredByGrant != 1 || decision.Additional != 0 || decision.Reason != allocatorDecisionCovered {
		t.Fatalf("commitment coverage not explained: %+v", decision)
	}

	// An unresolved creating runner defers demand instead of funding a second
	// concurrent award for the same job.
	in, _ = allocatorFixture(t)
	in.Snapshot.Charges = append(in.Snapshot.Charges, capacitybroker.Charge{ID: "creating-1", Host: "host", Pool: pool, Resources: size})
	poolState = in.Snapshot.Pools[pool]
	poolState.PendingWarm = 1
	poolState.Inflight = 1
	in.Snapshot.Pools[pool] = poolState
	proposal, err = PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Awards) != 0 {
		t.Fatalf("inflight double-funded: %+v", proposal.Awards)
	}
	if decision := allocatorDecision(proposal, pool); decision.DeferredByInflight != 1 ||
		decision.Reason != allocatorDecisionDeferredInflight {
		t.Fatalf("inflight deferral not explained: %+v", decision)
	}
}

func TestAllocatorExplainabilityVocabularyAndBounds(t *testing.T) {
	in, state := allocatorFixture(t)
	size := in.Snapshot.Pools[pool].Size
	allocatorEnrollPool(&in, &state, allocatorRepoB, allocatorPoolB, "fingerprint-b", 2, size, 1, 1, 0)
	allocatorSetCaps(&in, &state, 1, 1, 0)
	allocatorSetBudgets(&state, size[0], size[1], size[2], 1)
	allocatorQueueJob(&in, allocatorRepoB, allocatorPoolB, 2, in.Options.Now)
	charges := []admission.BrokerPlanningCharge{allocatorCharge("held-1", "host", pool, size)}
	allocatorRebuild(t, state, charges, &in)

	// A missing weights entry defaults to equal share.
	delete(in.Weights, repo)
	proposal, err := PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	awardReasons := map[string]bool{
		allocatorReasonQueuedDemand: true,
		allocatorReasonWarmFloor:    true,
	}
	decisionReasons := map[string]bool{
		allocatorDecisionProposed:         true,
		allocatorDecisionPartial:          true,
		allocatorDecisionCovered:          true,
		allocatorDecisionQuiet:            true,
		allocatorDecisionDeferredInflight: true,
		allocatorDecisionPoolDisabled:     true,
		allocatorDecisionBlockedPoolMax:   true,
		allocatorDecisionBlockedRepoMax:   true,
		allocatorDecisionBlockedGlobal:    true,
		allocatorDecisionBlockedPressure:  true,
		allocatorDecisionBlockedHostUnfit: true,
		allocatorDecisionBlockedUnfit:     true,
	}
	for _, award := range proposal.Awards {
		if !awardReasons[award.Reason] {
			t.Fatalf("unknown award reason %q", award.Reason)
		}
	}
	previous := ""
	for _, decision := range proposal.Decisions {
		if !decisionReasons[decision.Reason] {
			t.Fatalf("unknown decision reason %q", decision.Reason)
		}
		if previous != "" && decision.Pool < previous {
			t.Fatal("decisions not in deterministic pool order")
		}
		previous = decision.Pool
		if decision.RepositoryWeight == 0 {
			t.Fatal("weight must default to one")
		}
	}
	if allocatorDecision(proposal, pool).RepositoryWeight != 1 || allocatorDecision(proposal, allocatorPoolB).RepositoryWeight != 2 {
		t.Fatal("weights not explained per repository")
	}
	if proposal.Held.Global.Runners != 1 || proposal.Held.Pools[pool].Runners != 1 ||
		proposal.Held.Repositories[repo].Runners != 1 {
		t.Fatalf("held aggregation wrong: %+v", proposal.Held)
	}

	// Overflowing or negative charges fail closed instead of wrapping.
	in.Snapshot.Charges = append(in.Snapshot.Charges,
		capacitybroker.Charge{ID: "max-1", Host: "host", Pool: pool, Resources: capacitybroker.Resources{math.MaxInt64, 0, 0, 0}},
		capacitybroker.Charge{ID: "max-2", Host: "host", Pool: pool, Resources: capacitybroker.Resources{math.MaxInt64, 0, 0, 0}})
	if _, err := PlanAllocatorProposal(in); err != capacitybroker.ErrSnapshot {
		t.Fatal("overflowing charges planned", err)
	}
	in.Snapshot.Charges = in.Snapshot.Charges[:len(in.Snapshot.Charges)-2]
	in.Snapshot.Charges = append(in.Snapshot.Charges, capacitybroker.Charge{ID: "negative", Host: "host", Pool: pool, Resources: capacitybroker.Resources{-1, 0, 0, 0}})
	if _, err := PlanAllocatorProposal(in); err != capacitybroker.ErrSnapshot {
		t.Fatal("negative charges planned", err)
	}

	// A disabled pool keeps its identity and stays inert, with coverage shown.
	in, state = allocatorFixture(t)
	charges = []admission.BrokerPlanningCharge{allocatorObserveIdleRunner(&in, &state, pool, "row-s", "runner-s", size)}
	in.Observation.Pools[0].Enabled = false
	in.Observation.Jobs = []garmapi.QueuedJob{{
		WorkflowJobID:   11,
		RepositoryID:    repo,
		CreatedAt:       in.Options.Now,
		IdleOnlyPoolIDs: []string{pool},
	}}
	allocatorRebuild(t, state, charges, &in)
	proposal, err = PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Awards) != 0 {
		t.Fatalf("disabled pool grew: %+v", proposal.Awards)
	}
	if got := allocatorDecision(proposal, pool); got.Reason != allocatorDecisionPoolDisabled || got.CoveredByIdle != 1 {
		t.Fatalf("disabled pool decision: %+v", got)
	}
}

func TestAllocatorWeightsReadFromRevisionedBrokerPolicy(t *testing.T) {
	ctx := context.Background()
	state, _, _ := ledgerFixture()
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), state.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	desired := state.Policy
	desired.Revision = 0
	desired.Repositories[0].Weight = 5
	if err := store.ReplaceBrokerPolicy(ctx, desired); err != nil {
		t.Fatal(err)
	}
	weights, err := allocatorWeights(ctx, store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if weights[repo] != 5 {
		t.Fatalf("weights not read from broker_policy document: %+v", weights)
	}
	if allocatorRepositoryWeight(weights, allocatorRepoB) != 1 {
		t.Fatal("missing repository must default to weight one")
	}
	if allocatorRepositoryWeight(nil, repo) != 1 {
		t.Fatal("nil weights must default to one")
	}
	// The weights must belong to the same revision that governed the snapshot.
	if _, err := allocatorWeights(ctx, store, 0); err != capacitybroker.ErrSnapshot {
		t.Fatal("stale policy revision accepted", err)
	}
	if _, err := allocatorWeights(ctx, store, 2); err != capacitybroker.ErrSnapshot {
		t.Fatal("future policy revision accepted", err)
	}
	if _, err := allocatorWeights(ctx, nil, 1); err != capacitybroker.ErrSnapshot {
		t.Fatal("nil store accepted", err)
	}
}

func TestAllocatorAdvisoryLoopServesAndIsolatesProposal(t *testing.T) {
	proposal := AllocatorProposal{
		SourceIdentity: "source",
		InputRevision:  "revision",
		Held:           AllocatorHeldCapacity{Repositories: map[string]admission.Resources{}, Pools: map[string]admission.Resources{}},
		Awards:         []AllocatorAward{{Pool: pool, Repository: repo, Host: "host", Reason: allocatorReasonQueuedDemand}},
		Decisions:      []AllocatorPoolDecision{{Pool: pool, Repository: repo, Reason: allocatorDecisionProposed}},
		Risks:          []string{allocatorRiskDestinationUnestablished},
	}
	entered := make(chan struct{})
	loop := startAdvisoryLoop(context.Background(), func(ctx context.Context) (ReadPlan, error) {
		close(entered)
		return ReadPlan{ExpiresAt: time.Now().Add(time.Minute), Allocator: &proposal}, nil
	}, time.Hour, 12*time.Second)
	<-entered
	deadline := time.Now().Add(time.Second)
	for {
		got, err := loop.ReadPlan(context.Background())
		if err == nil {
			got.Allocator.Awards[0].Host = "mutated"
			got.Allocator.Held.Pools[pool] = admission.Resources{CPU: 9}
			got.Allocator.Decisions[0].Reason = "mutated"
			got.Allocator.Risks[0] = "mutated"
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("allocator proposal not served")
		}
		time.Sleep(time.Millisecond)
	}
	got, err := loop.ReadPlan(context.Background())
	if err != nil || got.Allocator == nil {
		t.Fatal(err)
	}
	if got.Allocator.Awards[0].Host != "host" || got.Allocator.Held.Pools[pool].CPU == 9 ||
		got.Allocator.Decisions[0].Reason != allocatorDecisionProposed || got.Allocator.Risks[0] != allocatorRiskDestinationUnestablished {
		t.Fatal("caller mutation leaked into the cached allocator proposal")
	}
	loop.Close()
	if _, err := loop.ReadPlan(context.Background()); err == nil {
		t.Fatal("closed loop retained allocator proposal")
	}
}

func TestAllocatorRepoForPool(t *testing.T) {
	if allocatorRepoFor(pool) != repo || allocatorRepoFor(allocatorPoolB) != allocatorRepoB {
		t.Fatal("repository lookup helper broken")
	}
}

// TestAllocatorRoutedPressureVetoNeverSelectsHost proves the measured-usage
// veto and freshness rules apply identically on both planning paths: a vetoed
// host is never selected for growth, routed or unrouted, while unrelated
// hosts keep planning unaffected.
func TestAllocatorRoutedPressureVetoNeverSelectsHost(t *testing.T) {
	state, snapshot, observation, opt, routes := routedStockFixture(t)
	routes[0].Host, routes[0].HostBindingSequence = "b", 4
	in := AllocatorInput{
		Source:      observation.SourceIdentity(),
		Snapshot:    snapshot,
		Policy:      state.Policy,
		Observation: observation,
		Options:     opt,
		CapturedAt:  opt.Now,
		Weights:     map[string]uint32{repo: 1},
	}

	// Baseline without veto: the verified route selects b and awards growth.
	proposal, err := PlanRoutedAllocatorProposal(state, in, routes)
	if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Host != "b" {
		t.Fatalf("fixture did not award on the verified route: %+v %v", proposal.Awards, err)
	}

	// A veto on the verified route host stops the routed award entirely; the
	// vetoed host is never selected. Retained charges are untouched.
	vetoed := in
	vetoed.Blocked = map[string]bool{"b": true}
	vetoed.RoutedPressureValidUntil = opt.Now.Add(time.Second)
	proposal, err = PlanRoutedAllocatorProposal(state, vetoed, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Awards) != 0 {
		t.Fatalf("vetoed route host selected for growth: %+v", proposal.Awards)
	}
	if got := allocatorDecision(proposal, pool); got.Reason != allocatorDecisionBlockedPressure {
		t.Fatalf("routed pressure reason: %+v", got)
	}
	if proposal.Held.Pools[pool].Runners != 1 || proposal.Held.Global.Runners != 1 {
		t.Fatalf("veto changed retained accounting: %+v", proposal.Held)
	}
	unrouted, err := PlanAllocatorProposal(vetoed)
	if err != nil || len(unrouted.Awards) != 0 {
		t.Fatalf("unrouted veto parity lost: %+v %v", unrouted.Awards, err)
	}

	// A veto on an unrelated host does not disable the verified destination.
	unrelated := in
	unrelated.Blocked = map[string]bool{"a": true}
	unrelated.RoutedPressureValidUntil = opt.Now.Add(time.Second)
	proposal, err = PlanRoutedAllocatorProposal(state, unrelated, routes)
	if err != nil || len(proposal.Awards) != 1 || proposal.Awards[0].Host != "b" {
		t.Fatalf("unrelated veto suppressed the verified route: %+v %v", proposal.Awards, err)
	}

	// The same veto evidence must change the published proposal revision on
	// both paths: pressure evidence is part of the input identity.
	baseline, err := PlanAllocatorProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.InputRevision == unrouted.InputRevision {
		t.Fatal("pressure evidence not bound into the proposal revision")
	}
}

// TestAllocatorEvidenceRevisionDistinguishesRouteAndPressure proves the
// published revision binds route evidence and pressure evidence: identical
// plans with different verified routes, or different vetoes, never share a
// revision, while identical inputs recompute identically.
func TestAllocatorEvidenceRevisionDistinguishesRouteAndPressure(t *testing.T) {
	state, snapshot, observation, opt, routes := routedStockFixture(t)
	routes[0].Host, routes[0].HostBindingSequence = "b", 4
	base := AllocatorInput{
		Source:      observation.SourceIdentity(),
		Snapshot:    snapshot,
		Policy:      state.Policy,
		Observation: observation,
		Options:     opt,
		CapturedAt:  opt.Now,
		Weights:     map[string]uint32{repo: 1},
	}
	first, err := PlanRoutedAllocatorProposal(state, base, routes)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanRoutedAllocatorProposal(state, base, routes)
	if err != nil || first.InputRevision != second.InputRevision {
		t.Fatalf("identical inputs must recompute one revision: %q vs %q", first.InputRevision, second.InputRevision)
	}

	// Different route evidence (host, binding sequence, freshness).
	changed := append([]VerifiedStockProviderRoute(nil), routes...)
	changed[0].HostBindingSequence = 3
	if _, err := PlanRoutedAllocatorProposal(state, base, changed); err != capacitybroker.ErrSnapshot {
		t.Fatal("stale route binding accepted", err)
	}
	fresh := append([]VerifiedStockProviderRoute(nil), routes...)
	fresh[0].ObservedAt = routes[0].ObservedAt.Add(-time.Second)
	proposal, err := PlanRoutedAllocatorProposal(state, base, fresh)
	if err != nil || proposal.InputRevision == first.InputRevision {
		t.Fatalf("route freshness not distinguished: %q vs %q %v", proposal.InputRevision, first.InputRevision, err)
	}

	// Routed and unrouted proposals never share a revision.
	unrouted, err := PlanAllocatorProposal(base)
	if err != nil || unrouted.InputRevision == first.InputRevision {
		t.Fatalf("route presence not distinguished: %q vs %q %v", unrouted.InputRevision, first.InputRevision, err)
	}

	// Pressure evidence distinguishes revisions on both paths.
	vetoed := base
	vetoed.Blocked = map[string]bool{"b": true}
	vetoed.RoutedPressureValidUntil = opt.Now.Add(time.Second)
	routedVetoed, err := PlanRoutedAllocatorProposal(state, vetoed, routes)
	if err != nil || routedVetoed.InputRevision == first.InputRevision {
		t.Fatal("routed pressure evidence not distinguished")
	}
	unroutedVetoed, err := PlanAllocatorProposal(vetoed)
	if err != nil || unroutedVetoed.InputRevision == unrouted.InputRevision {
		t.Fatal("unrouted pressure evidence not distinguished")
	}
	if unroutedVetoed.InputRevision == routedVetoed.InputRevision {
		t.Fatal("route and pressure evidence collapsed into one revision")
	}
}

func TestAllocatorRoutedPressureReasonIgnoresHealthyUnroutedSpare(t *testing.T) {
	state, snapshot, observation, opt, routes := routedStockFixture(t)
	routes[0].Host, routes[0].HostBindingSequence = "b", 4
	// Host a retains the original charge but also has room for another runner.
	// It would fund hypothetical growth, but it is not the verified route.
	a := snapshot.Hosts["a"]
	for dimension := range a.Budget {
		a.Budget[dimension] *= 2
	}
	snapshot.Hosts["a"] = a
	in := AllocatorInput{
		Source: observation.SourceIdentity(), Snapshot: snapshot, Policy: state.Policy,
		Observation: observation, Options: opt, CapturedAt: opt.Now,
		Blocked: map[string]bool{"b": true}, RoutedPressureValidUntil: opt.Now.Add(time.Second),
	}
	ordinary, err := PlanAllocatorProposal(in)
	if err != nil || len(ordinary.Awards) != 1 || ordinary.Awards[0].Host != "a" {
		t.Fatalf("fixture requires spare on unrouted host a: %+v %v", ordinary.Awards, err)
	}
	routed, err := PlanRoutedAllocatorProposal(state, in, routes)
	if err != nil || len(routed.Awards) != 0 || allocatorDecision(routed, pool).Reason != allocatorDecisionBlockedPressure {
		t.Fatalf("only the verified route should explain blocked growth: %+v %v", routed, err)
	}
	if routed.Held.Global.Runners != 1 || routed.Held.Pools[pool].Runners != 1 {
		t.Fatalf("routing changed retained charges: %+v", routed.Held)
	}
}

func TestAllocatorRoutedProposalPressureExpiryIsExclusiveAndBound(t *testing.T) {
	state, snapshot, observation, opt, routes := routedStockFixture(t)
	routes[0].Host, routes[0].HostBindingSequence = "b", 4
	in := AllocatorInput{
		Source: observation.SourceIdentity(), Snapshot: snapshot, Policy: state.Policy,
		Observation: observation, Options: opt, CapturedAt: opt.Now,
	}
	unblocked, err := PlanRoutedAllocatorProposal(state, in, routes)
	if err != nil || !unblocked.ExpiresAt.After(opt.Now.Add(2*time.Second)) {
		t.Fatalf("fixture requires longer route validity: %+v %v", unblocked, err)
	}
	a, err := PlanRoutedAllocatorProposal(state, in, routes)
	in.Blocked = map[string]bool{"b": false}
	b, errB := PlanRoutedAllocatorProposal(state, in, routes)
	if err != nil || errB != nil || a.InputRevision != b.InputRevision {
		t.Fatalf("inactive map entry changed pressure identity: %v %v", err, errB)
	}
	in.Blocked["b"] = true
	for _, deadline := range []time.Time{{}, opt.Now.Add(-time.Nanosecond), opt.Now, opt.Now.Add(pressureAge + time.Nanosecond)} {
		in.RoutedPressureValidUntil = deadline
		out, err := PlanRoutedAllocatorProposal(state, in, routes)
		if err != capacitybroker.ErrSnapshot || out.InputRevision != "" {
			t.Fatalf("unusable veto validity %v returned advice: %+v %v", deadline, out, err)
		}
	}
	in.RoutedPressureValidUntil = opt.Now.Add(time.Second)
	earlier, err := PlanRoutedAllocatorProposal(state, in, routes)
	if err != nil || !earlier.ExpiresAt.Equal(in.RoutedPressureValidUntil) || len(earlier.Awards) != 0 {
		t.Fatalf("pressure validity did not cap routed proposal: %+v %v", earlier, err)
	}
	in.RoutedPressureValidUntil = opt.Now.Add(2 * time.Second)
	later, err := PlanRoutedAllocatorProposal(state, in, routes)
	if err != nil || !later.ExpiresAt.Equal(in.RoutedPressureValidUntil) || earlier.InputRevision == later.InputRevision {
		t.Fatalf("different pressure validity did not change revision: %+v %v", later, err)
	}
	in.RoutedPressureValidUntil = opt.Now.Add(pressureAge)
	otherwiseBounded, err := PlanRoutedAllocatorProposal(state, in, routes)
	if err != nil || !otherwiseBounded.ExpiresAt.Equal(unblocked.ExpiresAt) {
		t.Fatalf("later pressure limit lengthened route/demand/capacity validity: %+v %v", otherwiseBounded, err)
	}
}
