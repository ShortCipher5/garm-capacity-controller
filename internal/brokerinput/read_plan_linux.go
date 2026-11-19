package brokerinput

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"sort"
	"sync"
	"time"
)

// ReadPlan is a bounded advisory comparison, not an allocation capability.
// Stock reads are non-atomic; exact source, ownership and ledger revision are
// checked, and the capture interval is explicit rather than called a live total.
type ReadPlan struct {
	Source         string           `json:"source"`
	SourceIdentity string           `json:"source_identity"`
	DemandDigest   string           `json:"demand_digest"`
	LedgerSequence int64            `json:"ledger_sequence"`
	PolicyRevision int64            `json:"policy_revision"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
	CapturedAt     time.Time        `json:"captured_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
	Atomic         bool             `json:"atomic"`
	Pools          []ReadPlanPool   `json:"pools"`
	RemainingJobs  int              `json:"remaining_jobs"`
	Pressure       []PressureStatus `json:"pressure,omitempty"`
}
type ReadPlanPool struct {
	Pool                 string                `json:"pool"`
	Repository           string                `json:"repository"`
	Supply               *garmapi.RunnerSupply `json:"supply"`
	Charged              admission.Resources   `json:"charged"`
	Cap                  int64                 `json:"cap"`
	WarmIdle             int64                 `json:"warm_idle"`
	Additional           int64                 `json:"additional"`
	SuppressedByPressure int64                 `json:"suppressed_by_pressure"`
	AdditionalWarm       int64                 `json:"additional_warm"`
	CoveredByIdle        int64                 `json:"covered_by_idle"`
	CoveredByGrant       int64                 `json:"covered_by_grant"`
	DeferredByInflight   int64                 `json:"deferred_by_inflight"`
}
type planReader struct {
	mu       sync.Mutex
	store    *admission.SQLiteStore
	source   *garmapi.DemandClient
	opt      capacitybroker.Options
	pressure bool
}

func (s *Service) ReadPlan(ctx context.Context) (ReadPlan, error) {
	if s == nil || s.reader == nil || ctx == nil || ctx.Err() != nil {
		return ReadPlan{}, capacitybroker.ErrSnapshot
	}
	return s.reader.read(ctx)
}
func (p *planReader) read(ctx context.Context) (ReadPlan, error) {
	if p.source == nil || !p.mu.TryLock() {
		return ReadPlan{}, capacitybroker.ErrSnapshot
	}
	defer p.mu.Unlock()
	observed, err := p.source.Observe(ctx)
	if err != nil {
		return ReadPlan{}, unavailable("observation_unavailable", err)
	}
	identity := p.source.SourceIdentity()
	if identity == "" || observed.SourceIdentity() != identity {
		return ReadPlan{}, capacitybroker.ErrSnapshot
	}
	state, charges, err := p.store.ReadBrokerPlanningState(ctx)
	if err != nil {
		return ReadPlan{}, unavailable("ledger_unavailable", err)
	}
	opt := p.opt
	opt.Now = time.Now().UTC()
	// These diagnostic checks only explain already-required joins. They confer
	// no authority and do not replace the canonical snapshot validation below.
	for id, pool := range state.Config.Pools {
		profile, ok := state.PoolProfiles[id]
		if !ok {
			return ReadPlan{}, unavailable("pool_mapping_missing", capacitybroker.ErrSnapshot)
		}
		for _, host := range pool.Hosts {
			binding, exists := state.HostBindings[host]
			matched := false
			for _, domain := range profile.Domains {
				if domain.Host == host && exists && domain.HostBindingSequence == binding.Sequence {
					matched = true
				}
			}
			if !matched {
				return ReadPlan{}, unavailable("host_binding_unavailable", capacitybroker.ErrSnapshot)
			}
		}
	}
	snapshot, _, err := enrolledPlanningSnapshot(state, charges, observed, identity, opt)
	if err != nil {
		return ReadPlan{}, unavailable("enrollment_or_evidence_unavailable", err)
	}
	// Read-only advice alone includes disabled-pool existing idle edges.
	plan, err := stock(snapshot, state.Policy, observed, opt, true)
	baseline := plan
	var pressure []PressureStatus
	if err == nil && p.pressure {
		var blocked map[string]bool
		pressure, blocked, err = p.readPressure(ctx, state, opt.Now)
		if err == nil {
			plan, err = advisoryStock(snapshot, state.Policy, observed, opt, blocked)
		}
	}
	if err != nil {
		return ReadPlan{}, unavailable("planning_unavailable", err)
	}
	// Reject a ledger revision changed during the read/plan window. This is not
	// a cross-system transaction and does not stop future changes after response.
	current, err := p.store.ReadBrokerGrantState(ctx)
	now := time.Now().UTC()
	if err != nil || ctx.Err() != nil || !current.Ready || current.Sequence != state.Sequence || current.Policy.Revision != state.Policy.Revision || p.source.SourceIdentity() != identity || now.Sub(observed.StartedAt) > opt.MaxAge {
		return ReadPlan{}, unavailable("snapshot_changed_or_expired", capacitybroker.ErrSnapshot)
	}
	result := projectReadPlan(state, observed, plan, now, opt.MaxAge)
	result.Pressure = pressure
	if p.pressure {
		// Net additional proposals withheld compared with the same snapshot without
		// the monitoring veto, per pool. Same-pool alternate-host placements are
		// not counted as suppressed; this is not physical runner relocation.
		before := projectReadPlan(state, observed, baseline, now, opt.MaxAge)
		for i := range result.Pools {
			difference := before.Pools[i].Additional - result.Pools[i].Additional
			if difference > 0 {
				result.Pools[i].SuppressedByPressure = difference
			}
		}
	}
	if !result.ExpiresAt.After(now) {
		return ReadPlan{}, unavailable("snapshot_changed_or_expired", capacitybroker.ErrSnapshot)
	}
	return result, nil
}
func projectReadPlan(state admission.BrokerGrantState, observed garmapi.DemandObservation, plan capacitybroker.JobPlan, now time.Time, maxAge time.Duration) ReadPlan {
	out := ReadPlan{Source: "scoped-stock-and-coherent-ledger", SourceIdentity: observed.SourceIdentity(), DemandDigest: observed.Digest, LedgerSequence: state.Sequence, PolicyRevision: state.Policy.Revision, StartedAt: observed.StartedAt, FinishedAt: observed.FinishedAt, CapturedAt: now, ExpiresAt: observed.StartedAt.Add(maxAge), Pools: []ReadPlanPool{}, RemainingJobs: len(plan.RemainingJobs)}
	// The comparison expires with its oldest participating host evidence, not
	// merely the newest stock read. This is stricter than either input alone.
	hostAge := maxAge
	if state.Config.MaxObservationAge < hostAge {
		hostAge = state.Config.MaxObservationAge
	}
	for _, host := range state.Config.Hosts {
		expiry := host.ObservedAt.Add(hostAge)
		if expiry.Before(out.ExpiresAt) {
			out.ExpiresAt = expiry
		}
	}
	supply := map[string]garmapi.RunnerSupply{}
	for _, v := range observed.Supply {
		supply[v.PoolID] = v
	}
	rows := map[string]*ReadPlanPool{}
	for _, repo := range state.Policy.Repositories {
		for _, p := range repo.Pools {
			row := &ReadPlanPool{Pool: p.ID, Repository: repo.ID, Charged: state.Allocated.Pools[p.ID], Cap: p.MaxRunners, WarmIdle: p.WarmIdle}
			if v, ok := supply[p.ID]; ok {
				copy := v
				row.Supply = &copy
			}
			rows[p.ID] = row
		}
	}
	// Unused grants are provisional queued/warm supply, not permanent runners.
	// Conservatively expire advice at any still-unused grant deadline in a
	// participating pool, even if that particular grant was not matched. Use the
	// observation START, not completion: expiry during planning must reject the
	// response rather than extend stale coverage. Already-expired grants remain
	// charged, but cannot constrain a new projection indefinitely.
	for _, grant := range state.Grants {
		if _, participating := rows[grant.Spec.Authority.PoolID]; participating &&
			grant.State == "unused" && grant.Spec.ExpiresAt.After(observed.StartedAt) &&
			grant.Spec.ExpiresAt.Before(out.ExpiresAt) {
			out.ExpiresAt = grant.Spec.ExpiresAt
		}
	}
	for _, a := range plan.Assignments {
		if p := rows[a.Pool]; p != nil {
			p.Additional++
			if a.Warm {
				p.AdditionalWarm++
			}
		}
	}
	for _, id := range plan.CoveredByIdle {
		if p := rows[id]; p != nil {
			p.CoveredByIdle++
		}
	}
	for _, id := range plan.CoveredByGrant {
		if p := rows[id]; p != nil {
			p.CoveredByGrant++
		}
	}
	for _, id := range plan.DeferredByInflight {
		if p := rows[id]; p != nil {
			p.DeferredByInflight++
		}
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out.Pools = append(out.Pools, *rows[id])
	}
	return out
}
