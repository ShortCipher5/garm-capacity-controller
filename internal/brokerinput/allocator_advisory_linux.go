package brokerinput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// Allocator award and decision reason vocabulary. These fixed codes are the
// entire operator-facing vocabulary; consumers must not parse free text.
const (
	allocatorReasonQueuedDemand = "queued_demand"
	allocatorReasonWarmFloor    = "warm_floor"

	allocatorDecisionProposed         = "proposed"
	allocatorDecisionPartial          = "partial"
	allocatorDecisionCovered          = "covered"
	allocatorDecisionQuiet            = "quiet"
	allocatorDecisionDeferredInflight = "deferred_inflight"
	allocatorDecisionPoolDisabled     = "pool_disabled"
	allocatorDecisionBlockedPoolMax   = "blocked_pool_max"
	allocatorDecisionBlockedRepoMax   = "blocked_repository_max"
	allocatorDecisionBlockedGlobal    = "blocked_global_budget"
	allocatorDecisionBlockedPressure  = "blocked_host_pressure"
	allocatorDecisionBlockedHostUnfit = "blocked_host_unfit"
	allocatorDecisionBlockedUnfit     = "blocked_unfit"

	allocatorRiskNonAtomicObservation     = "non_atomic_observation"
	allocatorRiskMissedWebhookCoverage    = "missed_webhook_coverage"
	allocatorRiskDestinationUnestablished = "destination_unestablished"
	allocatorRiskHostPressureSuppressed   = "host_pressure_suppressed"
)

// AllocatorInput is the normalized observe-only allocator input assembled from
// one authorized demand observation and one coherent ledger planning read.
// Discovered capacity, user-configured budgets, measured usage vetoes and
// enforceable provider limits stay distinct: budgets and caps arrive through
// Snapshot and Policy, measured usage vetoes through Blocked, and outstanding
// durable commitments through Commitments. Proposed awards never reduce or
// replace any of these retained charges.
type AllocatorInput struct {
	// Source is the authenticated managed stock source identity that stamped
	// Observation. A detached or restamped observation fails closed.
	Source         string
	LedgerSequence int64
	CapturedAt     time.Time
	Snapshot       capacitybroker.Snapshot
	Policy         admission.BrokerPolicy
	Observation    garmapi.DemandObservation
	Options        capacitybroker.Options
	// Weights carries effective per-repository weights normalized from the
	// revisioned broker_policy document; missing repositories default to one.
	Weights map[string]uint32
	// Blocked hosts carry fresh high measured usage and may not fund an increase.
	Blocked map[string]bool
	// Commitments is the number of outstanding unused durable grants already
	// persisted in the ledger. They are held capacity, never free capacity.
	Commitments int64

	destinationsEstablished bool
	routes                  []VerifiedStockProviderRoute
}

// AllocatorHeldCapacity reports retained physical charges aggregated from the
// canonical ledger charge list that produced the plan. A lower configured
// maximum, an unknown runner state or a missing observation never reduces it.
type AllocatorHeldCapacity struct {
	Global       admission.Resources            `json:"global"`
	Repositories map[string]admission.Resources `json:"repositories"`
	Pools        map[string]admission.Resources `json:"pools"`
	// Commitments counts outstanding unused durable grants included in the
	// charges above. Persisted commitments are accounted before any award.
	Commitments int64 `json:"commitments"`
}

// AllocatorAward proposes one additional runner for a pool at one exact host
// destination. It is advice only: not a reservation, provider permit, remote
// write, job binding or release evidence. Resources is the complete budgeted
// runner profile; a disk margin warning never shrinks it into a quota.
type AllocatorAward struct {
	Pool       string              `json:"pool"`
	Repository string              `json:"repository"`
	Host       string              `json:"host"`
	Warm       bool                `json:"warm"`
	JobID      string              `json:"job_id,omitempty"`
	Resources  admission.Resources `json:"resources"`
	Reason     string              `json:"reason"`
	// DestinationEstablished is true only when an independently verified stock
	// provider route narrowed this award to the exact host. An eligible host
	// list alone never establishes a destination.
	DestinationEstablished bool `json:"destination_established"`
}

// AllocatorPoolDecision explains one policy-enrolled pool's outcome for this
// tick. RemainingQueued counts still-unmet jobs listing this pool as a
// candidate; candidate pools overlap across jobs, so these counts are not an
// additive queue size. Demand signals stay separate from held charges.
type AllocatorPoolDecision struct {
	Pool               string `json:"pool"`
	Repository         string `json:"repository"`
	RepositoryWeight   uint32 `json:"repository_weight"`
	ChargedRunners     int64  `json:"charged_runners"`
	Additional         int64  `json:"additional"`
	AdditionalWarm     int64  `json:"additional_warm"`
	CoveredByIdle      int64  `json:"covered_by_idle"`
	CoveredByGrant     int64  `json:"covered_by_grant"`
	DeferredByInflight int64  `json:"deferred_by_inflight"`
	RemainingQueued    int64  `json:"remaining_queued"`
	Reason             string `json:"reason"`
}

// AllocatorProposal is the bounded explainable observe-only cross-repository
// allocation result. It contains no writer state: overlapping ticks and
// restarts recompute it deterministically from durable inputs alone, so two
// repositories can never be awarded the same retained slot.
type AllocatorProposal struct {
	Source         string `json:"source"`
	SourceIdentity string `json:"source_identity"`
	// State is verified only when every award's destination was narrowed by
	// independently verified stock provider routes. Without established
	// destinations the same growth is hypothetical: it explains what would be
	// requested, never verified placement or enforceable capacity.
	State          string `json:"state"`
	DemandDigest   string `json:"demand_digest"`
	LedgerSequence int64  `json:"ledger_sequence"`
	PolicyRevision int64  `json:"policy_revision"`
	// InputRevision binds the plan's own input rebind plus the verified route
	// evidence and measured-usage pressure evidence; route or pressure changes
	// always produce a different revision.
	InputRevision string                  `json:"input_revision"`
	StartedAt     time.Time               `json:"started_at"`
	FinishedAt    time.Time               `json:"finished_at"`
	CapturedAt    time.Time               `json:"captured_at"`
	ExpiresAt     time.Time               `json:"expires_at"`
	Held          AllocatorHeldCapacity   `json:"held"`
	Awards        []AllocatorAward        `json:"awards"`
	Decisions     []AllocatorPoolDecision `json:"decisions"`
	Risks         []string                `json:"risks,omitempty"`
}

const (
	allocatorStateVerified     = "verified"
	allocatorStateHypothetical = "hypothetical"
)

// PlanAllocatorProposal runs the bounded observe-only stock cycle planner over
// normalized inputs and translates the result. It polls nothing and writes
// nothing; stale, unreachable, paused, ambiguous or measured-usage-vetoed
// hosts never fund an increase.
func PlanAllocatorProposal(in AllocatorInput) (AllocatorProposal, error) {
	fail := func() (AllocatorProposal, error) { return AllocatorProposal{}, capacitybroker.ErrSnapshot }
	if in.Source == "" || in.Observation.SourceIdentity() != in.Source {
		return fail()
	}
	plan, err := stock(allocatorVetoHosts(in.Snapshot, in.Blocked), in.Policy, in.Observation, in.Options, true)
	if err != nil {
		return fail()
	}
	expiry := in.Observation.StartedAt.Add(in.Options.MaxAge)
	if deadline := in.Snapshot.ObservedAt.Add(in.Options.MaxAge); deadline.Before(expiry) {
		expiry = deadline
	}
	return ExplainAllocatorPlan(in, plan, expiry)
}

// PlanRoutedAllocatorProposal narrows placement to independently verified
// stock provider routes before planning. Every award it produces carries an
// established exact destination; wrong-source, stale, duplicated, conflicting
// or incomplete route/profile evidence awards nothing at all. The measured
// usage veto and freshness rules are the same ones the unrouted path applies:
// a vetoed route host never funds an increase either.
func PlanRoutedAllocatorProposal(state admission.BrokerGrantState, in AllocatorInput, routes []VerifiedStockProviderRoute) (AllocatorProposal, error) {
	fail := func() (AllocatorProposal, error) { return AllocatorProposal{}, capacitybroker.ErrSnapshot }
	if in.Source == "" || in.Observation.SourceIdentity() != in.Source {
		return fail()
	}
	plan, expiry, err := planRoutedStockPreview(state, allocatorVetoHosts(in.Snapshot, in.Blocked), in.Observation, in.Source, in.Options, routes)
	if err != nil {
		return fail()
	}
	in.destinationsEstablished = true
	in.routes = routes
	return ExplainAllocatorPlan(in, plan, expiry)
}

// allocatorVetoHosts copies the snapshot and marks hosts with fresh high
// measured usage as not accepting new placements. It cannot enable any host
// and never moves or discounts existing charges. The same rule guards routed
// and unrouted proposals, so a vetoed host is never selected for growth on
// either path.
func allocatorVetoHosts(snapshot capacitybroker.Snapshot, blocked map[string]bool) capacitybroker.Snapshot {
	if len(blocked) == 0 {
		return snapshot
	}
	hosts := make(map[string]capacitybroker.Host, len(snapshot.Hosts))
	for id, h := range snapshot.Hosts {
		if blocked[id] {
			h.AcceptNew = false
		}
		hosts[id] = h
	}
	snapshot.Hosts = hosts
	return snapshot
}

// allocatorInputRevision rebinds the exact planner inputs the way stockPlan
// does, so a proposal can prove it translated the plan computed from these
// very inputs rather than a crossed or stale plan.
func allocatorInputRevision(capacityRevision string, policyRevision int64, demandDigest string) string {
	raw, err := json.Marshal(struct {
		Capacity string
		Policy   int64
		Demand   string
	}{capacityRevision, policyRevision, demandDigest})
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// allocatorEvidenceRevision binds the published proposal revision to every
// input class that can change a placement decision: the plan's own input
// rebind, the verified route evidence (nil on the unrouted path) and the
// measured-usage pressure evidence. Identical plans with different routes or
// different vetoes therefore never share a revision.
func allocatorEvidenceRevision(in AllocatorInput, planRevision string) string {
	var routes []VerifiedStockProviderRoute
	if in.destinationsEstablished {
		routes = append([]VerifiedStockProviderRoute(nil), in.routes...)
		sort.Slice(routes, func(i, j int) bool {
			if routes[i].Provider != routes[j].Provider {
				return routes[i].Provider < routes[j].Provider
			}
			return routes[i].Host < routes[j].Host
		})
	}
	var blocked []string
	for host := range in.Blocked {
		blocked = append(blocked, host)
	}
	sort.Strings(blocked)
	raw, err := json.Marshal(struct {
		Version  string
		Plan     string
		Routes   []VerifiedStockProviderRoute
		Pressure []string
	}{"allocator-proposal-v1", planRevision, routes, blocked})
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// ExplainAllocatorPlan translates one completed stock cycle plan into the
// bounded explainable proposal. The plan must have been computed from in's
// snapshot, policy and observation; anything else fails closed.
func ExplainAllocatorPlan(in AllocatorInput, plan capacitybroker.JobPlan, expiry time.Time) (AllocatorProposal, error) {
	fail := func() (AllocatorProposal, error) { return AllocatorProposal{}, capacitybroker.ErrSnapshot }
	opt := in.Options
	if in.Source == "" || in.Observation.SourceIdentity() != in.Source || in.Observation.Digest == "" ||
		in.Observation.StartedAt.IsZero() || in.Observation.FinishedAt.Before(in.Observation.StartedAt) ||
		in.Snapshot.Revision == "" || !in.Snapshot.Complete || in.Snapshot.ObservedAt.IsZero() ||
		in.Commitments < 0 ||
		admission.ValidateBrokerPolicy(in.Policy) != nil || opt.Now.IsZero() || opt.MaxAge <= 0 || opt.MaxAssignments < 1 ||
		expiry.IsZero() || !expiry.After(opt.Now) || plan.Revision == "" ||
		plan.Revision != allocatorInputRevision(in.Snapshot.Revision, in.Policy.Revision, in.Observation.Digest) ||
		len(plan.Assignments) > opt.MaxAssignments {
		return fail()
	}
	observed := make(map[string]garmapi.DiscoveredPool, len(in.Observation.Pools))
	for _, remote := range in.Observation.Pools {
		if _, duplicate := observed[remote.ID]; duplicate {
			return fail()
		}
		observed[remote.ID] = remote
	}
	held, err := allocatorHeld(in.Snapshot)
	if err != nil {
		return fail()
	}
	out := AllocatorProposal{
		Source:         "scoped-stock-and-coherent-ledger",
		SourceIdentity: in.Observation.SourceIdentity(),
		State:          allocatorStateHypothetical,
		DemandDigest:   in.Observation.Digest,
		LedgerSequence: in.LedgerSequence,
		PolicyRevision: in.Policy.Revision,
		InputRevision:  allocatorEvidenceRevision(in, plan.Revision),
		StartedAt:      in.Observation.StartedAt,
		FinishedAt:     in.Observation.FinishedAt,
		CapturedAt:     in.CapturedAt,
		ExpiresAt:      expiry,
		Held: AllocatorHeldCapacity{
			Global:       heldResources(held.global),
			Repositories: map[string]admission.Resources{},
			Pools:        map[string]admission.Resources{},
			Commitments:  in.Commitments,
		},
		Awards:    []AllocatorAward{},
		Decisions: []AllocatorPoolDecision{},
	}
	for id, used := range held.pools {
		out.Held.Pools[id] = heldResources(used)
	}
	for id, used := range held.repos {
		out.Held.Repositories[id] = heldResources(used)
	}
	counts := map[string]int64{}
	for _, a := range plan.Assignments {
		pool, ok := in.Snapshot.Pools[a.Pool]
		if !ok || a.Host == "" {
			return fail()
		}
		if _, ok := in.Snapshot.Hosts[a.Host]; !ok {
			return fail()
		}
		counts[a.Pool]++
		reason := allocatorReasonQueuedDemand
		if a.Warm {
			reason = allocatorReasonWarmFloor
		}
		out.Awards = append(out.Awards, AllocatorAward{
			Pool: a.Pool, Repository: pool.Repository, Host: a.Host, Warm: a.Warm, JobID: a.JobID,
			Resources: heldResources(a.Resources), Reason: reason,
			DestinationEstablished: in.destinationsEstablished,
		})
	}
	coveredIdle := allocatorPoolCounts(plan.CoveredByIdle)
	coveredGrant := allocatorPoolCounts(plan.CoveredByGrant)
	deferred := allocatorPoolCounts(plan.DeferredByInflight)
	remaining := map[string]int64{}
	for _, job := range plan.RemainingJobs {
		seen := map[string]bool{}
		for _, id := range job.Candidates {
			if !seen[id] {
				seen[id] = true
				remaining[id]++
			}
		}
	}
	// Blocker reasons must reflect the plan itself: capacity consumed by this
	// proposal's own awards is no longer available to competing pools. Held
	// charges alone never shrink, and no proposed reduction ever funds growth.
	planned, err := allocatorPlanned(in.Snapshot, plan, held)
	if err != nil {
		return fail()
	}
	repoIDs := make([]string, 0, len(in.Policy.Repositories))
	byRepo := make(map[string]admission.BrokerRepositoryPolicy, len(in.Policy.Repositories))
	for _, r := range in.Policy.Repositories {
		if _, duplicate := byRepo[r.ID]; duplicate {
			return fail()
		}
		byRepo[r.ID] = r
		repoIDs = append(repoIDs, r.ID)
	}
	sort.Strings(repoIDs)
	for _, repoID := range repoIDs {
		repo := byRepo[repoID]
		poolIDs := make([]string, 0, len(repo.Pools))
		byPool := make(map[string]admission.BrokerPoolPolicy, len(repo.Pools))
		for _, p := range repo.Pools {
			if _, duplicate := byPool[p.ID]; duplicate {
				return fail()
			}
			byPool[p.ID] = p
			poolIDs = append(poolIDs, p.ID)
		}
		sort.Strings(poolIDs)
		for _, poolID := range poolIDs {
			pool, ok := in.Snapshot.Pools[poolID]
			if !ok || pool.Repository != repoID {
				return fail()
			}
			remote, seen := observed[poolID]
			if !seen {
				return fail()
			}
			decision := AllocatorPoolDecision{
				Pool:               poolID,
				Repository:         repoID,
				RepositoryWeight:   allocatorRepositoryWeight(in.Weights, repoID),
				ChargedRunners:     held.pools[poolID][3],
				Additional:         counts[poolID],
				AdditionalWarm:     allocatorWarmCount(plan, poolID),
				CoveredByIdle:      coveredIdle[poolID],
				CoveredByGrant:     coveredGrant[poolID],
				DeferredByInflight: deferred[poolID],
				RemainingQueued:    remaining[poolID],
			}
			decision.Reason = allocatorDecisionReason(in.Snapshot, planned, pool, remote, in.Blocked, decision)
			out.Decisions = append(out.Decisions, decision)
		}
	}
	if !in.Observation.Atomic {
		out.Risks = append(out.Risks, allocatorRiskNonAtomicObservation)
	}
	if in.Observation.MissedWebhookCoverage {
		out.Risks = append(out.Risks, allocatorRiskMissedWebhookCoverage)
	}
	if in.destinationsEstablished {
		out.State = allocatorStateVerified
	} else if len(out.Awards) > 0 {
		out.Risks = append(out.Risks, allocatorRiskDestinationUnestablished)
	}
	if len(in.Blocked) > 0 {
		out.Risks = append(out.Risks, allocatorRiskHostPressureSuppressed)
	}
	if len(out.Risks) > 8 {
		return fail()
	}
	return out, nil
}

type allocatorHeldView struct {
	global capacitybroker.Resources
	hosts  map[string]capacitybroker.Resources
	pools  map[string]capacitybroker.Resources
	repos  map[string]capacitybroker.Resources
}

// allocatorHeld aggregates the canonical charge list into per-host, per-pool
// and per-repository retention with checked arithmetic. Every charge remains
// retained until authoritative reconciliation proves absence.
func allocatorHeld(s capacitybroker.Snapshot) (allocatorHeldView, error) {
	out := allocatorHeldView{
		hosts: map[string]capacitybroker.Resources{},
		pools: map[string]capacitybroker.Resources{},
		repos: map[string]capacitybroker.Resources{},
	}
	for _, c := range s.Charges {
		if c.ID == "" {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if _, ok := s.Hosts[c.Host]; !ok {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		var ok bool
		if out.global, ok = allocatorAdd(out.global, c.Resources); !ok {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if out.hosts[c.Host], ok = allocatorAdd(out.hosts[c.Host], c.Resources); !ok {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if c.Pool == "" {
			continue
		}
		pool, exists := s.Pools[c.Pool]
		if !exists {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if out.pools[c.Pool], ok = allocatorAdd(out.pools[c.Pool], c.Resources); !ok {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if out.repos[pool.Repository], ok = allocatorAdd(out.repos[pool.Repository], c.Resources); !ok {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
	}
	return out, nil
}

func allocatorAdd(a, b capacitybroker.Resources) (capacitybroker.Resources, bool) {
	for i := range a {
		if b[i] < 0 || b[i] > math.MaxInt64-a[i] {
			return capacitybroker.Resources{}, false
		}
		a[i] += b[i]
	}
	return a, true
}

func heldResources(v capacitybroker.Resources) admission.Resources {
	return admission.Resources{CPU: v[0], RAM: v[1], Disk: v[2], Runners: v[3]}
}

func allocatorPoolCounts(values map[string]string) map[string]int64 {
	out := map[string]int64{}
	for _, pool := range values {
		out[pool]++
	}
	return out
}

func allocatorWarmCount(plan capacitybroker.JobPlan, pool string) int64 {
	var n int64
	for _, a := range plan.Assignments {
		if a.Warm && a.Pool == pool {
			n++
		}
	}
	return n
}

// allocatorPlanned overlays one plan's own awards on the retained charges.
// Awards are additions only: nothing here releases, moves or discounts held
// capacity, so a proposed reduction can never fund a scale-up.
func allocatorPlanned(s capacitybroker.Snapshot, plan capacitybroker.JobPlan, held allocatorHeldView) (allocatorHeldView, error) {
	out := allocatorHeldView{
		global: held.global,
		hosts:  map[string]capacitybroker.Resources{},
		pools:  map[string]capacitybroker.Resources{},
		repos:  map[string]capacitybroker.Resources{},
	}
	for host, used := range held.hosts {
		out.hosts[host] = used
	}
	for pool, used := range held.pools {
		out.pools[pool] = used
	}
	for repo, used := range held.repos {
		out.repos[repo] = used
	}
	for _, a := range plan.Assignments {
		pool, ok := s.Pools[a.Pool]
		if !ok {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if _, ok := s.Hosts[a.Host]; !ok {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		var okAdd bool
		if out.global, okAdd = allocatorAdd(out.global, a.Resources); !okAdd {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if out.hosts[a.Host], okAdd = allocatorAdd(out.hosts[a.Host], a.Resources); !okAdd {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if out.pools[a.Pool], okAdd = allocatorAdd(out.pools[a.Pool], a.Resources); !okAdd {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
		if out.repos[pool.Repository], okAdd = allocatorAdd(out.repos[pool.Repository], a.Resources); !okAdd {
			return allocatorHeldView{}, capacitybroker.ErrSnapshot
		}
	}
	return out, nil
}

// allocatorDecisionReason picks one deterministic explanation for a pool. The
// checks run in a fixed order; the first that applies wins.
func allocatorDecisionReason(s capacitybroker.Snapshot, planned allocatorHeldView, pool capacitybroker.Pool, remote garmapi.DiscoveredPool, blocked map[string]bool, d AllocatorPoolDecision) string {
	if !remote.Enabled {
		return allocatorDecisionPoolDisabled
	}
	if d.Additional > 0 && d.RemainingQueued == 0 {
		return allocatorDecisionProposed
	}
	if d.Additional > 0 {
		return allocatorDecisionPartial
	}
	if d.DeferredByInflight > 0 {
		return allocatorDecisionDeferredInflight
	}
	if d.RemainingQueued == 0 {
		if d.CoveredByIdle > 0 || d.CoveredByGrant > 0 {
			return allocatorDecisionCovered
		}
		return allocatorDecisionQuiet
	}
	if d.ChargedRunners+d.Additional >= pool.MaxRunners {
		return allocatorDecisionBlockedPoolMax
	}
	repo := s.Repositories[pool.Repository]
	if planned.repos[pool.Repository][3] >= repo.MaxRunners {
		return allocatorDecisionBlockedRepoMax
	}
	if !allocatorFits(planned.global, pool.Size, s.Global) {
		return allocatorDecisionBlockedGlobal
	}
	eligiblyFit, pressureFit := 0, 0
	for _, host := range pool.Hosts {
		h, ok := s.Hosts[host]
		if !ok || !h.AcceptNew {
			continue // Paused, unhealthy or stale hosts never fund an increase.
		}
		if !allocatorFits(planned.hosts[host], pool.Size, h.Budget) {
			continue
		}
		if blocked[host] {
			// Fresh high measured usage: the host could otherwise fund the
			// increase, so the veto is what blocks it.
			pressureFit++
			continue
		}
		eligiblyFit++
	}
	if pressureFit > 0 && eligiblyFit == 0 {
		return allocatorDecisionBlockedPressure
	}
	if eligiblyFit == 0 {
		return allocatorDecisionBlockedHostUnfit
	}
	return allocatorDecisionBlockedUnfit
}

func allocatorFits(used, request, budget capacitybroker.Resources) bool {
	for i := range used {
		if used[i] > budget[i] || request[i] > budget[i]-used[i] {
			return false
		}
	}
	return true
}

// allocatorSection translates the tick's already-validated advisory plan into
// the observe-only cross-repository proposal. Weights are read directly from
// the revisioned broker_policy document and must match the coherent planning
// revision; the section adds no writer, poller or grant lifecycle.
func (p *planReader) allocatorSection(ctx context.Context, state admission.BrokerGrantState, snapshot capacitybroker.Snapshot, observed garmapi.DemandObservation, plan capacitybroker.JobPlan, identity string, opt capacitybroker.Options, blocked map[string]bool, capturedAt, expiry time.Time) (AllocatorProposal, error) {
	weights, err := allocatorWeights(ctx, p.store, state.Policy.Revision)
	if err != nil {
		return AllocatorProposal{}, unavailable("allocator_weights_unavailable", capacitybroker.ErrSnapshot)
	}
	commitments := int64(0)
	for _, grant := range state.Grants {
		if grant.State == "unused" {
			commitments++
		}
	}
	proposal, err := ExplainAllocatorPlan(AllocatorInput{
		Source: identity, LedgerSequence: state.Sequence, CapturedAt: capturedAt,
		Snapshot: snapshot, Policy: state.Policy, Observation: observed, Options: opt,
		Weights: weights, Blocked: blocked, Commitments: commitments,
	}, plan, expiry)
	if err != nil {
		return AllocatorProposal{}, unavailable("allocator_unavailable", err)
	}
	return proposal, nil
}

// allocatorWeights reads per-repository weights from the revisioned SQLite
// broker_policy document. A revision different from the coherent planning
// read fails closed: weights must belong to the same policy revision that
// governed the snapshot and charges.
func allocatorWeights(ctx context.Context, store *admission.SQLiteStore, revision int64) (map[string]uint32, error) {
	if store == nil || revision < 0 {
		return nil, capacitybroker.ErrSnapshot
	}
	policy, err := store.BrokerPolicy(ctx)
	if err != nil || policy.Revision != revision {
		return nil, capacitybroker.ErrSnapshot
	}
	weights := make(map[string]uint32, len(policy.Repositories))
	for _, r := range policy.Repositories {
		weights[r.ID] = r.Weight
	}
	return weights, nil
}

// allocatorRepositoryWeight returns the configured weight for a repository,
// defaulting to equal share (one) when the policy document has no entry.
func allocatorRepositoryWeight(weights map[string]uint32, repository string) uint32 {
	if w := weights[repository]; w > 0 {
		return w
	}
	return 1
}
