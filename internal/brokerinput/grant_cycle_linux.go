package brokerinput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// GrantCycleResult describes only durable capacity bookkeeping. Neither its
// grants nor its advisory job correlations authorize a native create or GARM
// settings mutation. An error returns no apparently successful result; after an
// uncertain commit the next cycle rereads the canonical charged grant history.
type GrantCycleResult struct {
	BatchID string
	Plan    capacitybroker.JobPlan
	Grants  []admission.BrokerGrant
}

// GrantCycle observes the real scoped stock API, reads one coherent ledger
// revision, plans using enrolled profiles and commits a single revision-CAS
// batch. No external callback, caller-built observation or profile map can mint
// source identity. It does not mutate pool settings, retire physical reservations, or activate
// native execution. The existing grant batch is the durable cycle intent.
func GrantCycle(ctx context.Context, store *admission.SQLiteStore, source *garmapi.DemandClient, opt capacitybroker.Options, ttl time.Duration) (GrantCycleResult, error) {
	if ctx == nil || ctx.Err() != nil || store == nil || source == nil || ttl < time.Minute || ttl > time.Hour {
		return GrantCycleResult{}, capacitybroker.ErrSnapshot
	}
	observation, err := source.Observe(ctx)
	if err != nil {
		return GrantCycleResult{}, err
	}
	// Managed-source identity is minted by the first verified response, not
	// configuration. Capture it only after observation and before ANY ledger
	// maintenance or issuance; a serialized/caller-built observation cannot
	// provide the private matching source identity.
	sourceIdentity := source.SourceIdentity()
	if sourceIdentity == "" || observation.SourceIdentity() != sourceIdentity {
		return GrantCycleResult{}, capacitybroker.ErrSnapshot
	}
	// The clock is sampled after transport completion, not before its latency.
	opt.Now = time.Now().UTC()
	state, charges, err := store.ReadBrokerPlanningState(ctx)
	if err != nil {
		return GrantCycleResult{}, err
	}
	// Validate source/profile/freshness before any maintenance write.
	if _, _, err = enrolledCycle(state, charges, observation, sourceIdentity, opt, ttl); err != nil {
		return GrantCycleResult{}, err
	}
	// Only the canonical UNUSED→EXPIRED transition can release these charges.
	// A raced consumption aborts this cycle; no reservation is ever retired.
	if err = retireExpiredCycleGrants(ctx, store, state.Grants, opt.Now); err != nil {
		return GrantCycleResult{}, err
	}
	// Maintenance may append audit entries and change capacity. Never issue using
	// the pre-maintenance revision, even if another worker performed the expiry.
	state, charges, err = store.ReadBrokerPlanningState(ctx)
	if err != nil {
		return GrantCycleResult{}, err
	}
	opt.Now = time.Now().UTC()
	plan, batch, err := enrolledCycle(state, charges, observation, sourceIdentity, opt, ttl)
	if err != nil {
		return GrantCycleResult{}, err
	}
	if len(batch.Grants) == 0 {
		return GrantCycleResult{Plan: plan}, nil
	}
	grants, err := store.IssueBrokerGrants(ctx, batch)
	if err != nil {
		return GrantCycleResult{}, err
	}
	return GrantCycleResult{BatchID: batch.ID, Plan: plan, Grants: grants}, nil
}

// enrolledPlanningSnapshot validates the exact stock/profile/ledger join without
// creating a grant batch or changing any capacity accounting.
func enrolledPlanningSnapshot(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, observation garmapi.DemandObservation, source string, opt capacitybroker.Options) (capacitybroker.Snapshot, map[string]map[string]admission.BrokerProfileDomain, error) {
	snapshot, domains, err := enrolledPlanningSnapshotWithReadiness(state, charges, observation, source, opt)
	if err != nil {
		return capacitybroker.Snapshot{}, nil, capacitybroker.ErrSnapshot
	}
	return snapshot, domains, nil
}

// Only the authenticated read-only preview consumes the more specific failure.
// Other callers retain the same generic planning refusal.
func enrolledPlanningSnapshotWithReadiness(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, observation garmapi.DemandObservation, source string, opt capacitybroker.Options) (capacitybroker.Snapshot, map[string]map[string]admission.BrokerProfileDomain, error) {
	fail := func() (capacitybroker.Snapshot, map[string]map[string]admission.BrokerProfileDomain, error) {
		return capacitybroker.Snapshot{}, nil, capacitybroker.ErrSnapshot
	}
	if source == "" || observation.SourceIdentity() != source || len(state.PoolProfiles) != len(state.Config.Pools) {
		return fail()
	}
	observed := map[string]garmapi.DiscoveredPool{}
	for _, p := range observation.Pools {
		if _, exists := observed[p.ID]; exists {
			return fail()
		}
		observed[p.ID] = p
	}
	profiles := map[string]LedgerProfile{}
	domains := map[string]map[string]admission.BrokerProfileDomain{}
	for id, pool := range state.Config.Pools {
		p, ok := state.PoolProfiles[id]
		remote, seen := observed[id]
		if !ok || !seen || p.Sequence <= 0 || p.SourceIdentity != source || remote.OwnershipFingerprint != p.PoolFingerprint || p.Pool != id || p.Authority.PoolID != id || p.Repository != remote.RepositoryID || p.Execution.Name != "" {
			return fail()
		}
		domains[id] = map[string]admission.BrokerProfileDomain{}
		for _, d := range p.Domains {
			if _, exists := domains[id][d.Host]; exists {
				return fail()
			}
			domains[id][d.Host] = d
		}
		for _, host := range pool.Hosts {
			d, ok := domains[id][host]
			binding, exists := state.HostBindings[host]
			if !ok || !exists || d.HostBindingSequence != binding.Sequence {
				return fail()
			}
		}
		profiles[id] = LedgerProfile{Repository: p.Repository, Size: p.Resources, Authority: p.Authority, Execution: p.Execution}
	}
	snapshot, err := ledgerSnapshot(state, charges, profiles, opt)
	if err != nil {
		return fail()
	}
	if err = matchStockInflight(&snapshot, state, observation); err != nil {
		return capacitybroker.Snapshot{}, nil, err
	}
	return snapshot, domains, nil
}

func enrolledCycle(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, observation garmapi.DemandObservation, source string, opt capacitybroker.Options, ttl time.Duration) (capacitybroker.JobPlan, admission.BrokerGrantBatch, error) {
	fail := func() (capacitybroker.JobPlan, admission.BrokerGrantBatch, error) {
		return capacitybroker.JobPlan{}, admission.BrokerGrantBatch{}, capacitybroker.ErrSnapshot
	}
	if ttl < time.Minute || ttl > time.Hour {
		return fail()
	}
	snapshot, domains, err := enrolledPlanningSnapshot(state, charges, observation, source, opt)
	if err != nil {
		return fail()
	}
	plan, err := Stock(snapshot, state.Policy, observation, opt)
	if err != nil {
		return fail()
	}
	batch := admission.BrokerGrantBatch{ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision}
	// A single durable identity for this source/ledger revision makes competing
	// workers conflict rather than creating a second logical cycle. The complete
	// canonical batch payload is separately checked by IssueBrokerGrants replay.
	key, _ := json.Marshal(struct {
		Version, Source  string
		Sequence, Policy int64
	}{"broker-cycle-v1", source, state.Sequence, state.Policy.Revision})
	hash := sha256.Sum256(key)
	batch.ID = hex.EncodeToString(hash[:])
	for _, a := range plan.Assignments {
		p, ok := state.PoolProfiles[a.Pool]
		d, exists := domains[a.Pool][a.Host]
		if !ok || !exists || resources(p.Resources) != a.Resources {
			return fail()
		}
		batch.Grants = append(batch.Grants, admission.BrokerGrantSpec{Authority: p.Authority, Host: a.Host, HostBindingSequence: d.HostBindingSequence, Resources: p.Resources, Execution: p.Execution, ExpiresAt: opt.Now.Add(ttl)})
	}
	return plan, batch, nil
}

// RunGrantCycles serializes complete observe/read/issue cycles. It never retries
// an uncertain batch blindly: every next attempt obtains fresh observations and
// canonical ledger charges. Failed cycles report only their error, never stale
// plans or counts. The caller owns the concrete client/store lifecycle; there is
// intentionally no production startup wiring until native authority is ready.
func RunGrantCycles(ctx context.Context, store *admission.SQLiteStore, source *garmapi.DemandClient, opt capacitybroker.Options, ttl, interval time.Duration, report func(GrantCycleResult, error)) error {
	if ctx == nil || store == nil || source == nil || report == nil || interval < time.Minute || interval > time.Hour || ttl < time.Minute || ttl > time.Hour {
		return capacitybroker.ErrSnapshot
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cycleCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		result, err := GrantCycle(cycleCtx, store, source, opt, ttl)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		report(result, err)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Fixed work budget per cycle. Larger backlogs remain charged and are processed
// across later cycles; no scan/incomplete pass makes an unretired grant free.
const cycleExpiryLimit = 64

func cycleExpiryCandidates(grants []admission.BrokerGrant, now time.Time) []string {
	ids := make([]string, 0, cycleExpiryLimit)
	for _, g := range grants {
		if g.State == "unused" && !g.Spec.ExpiresAt.IsZero() && !g.Spec.ExpiresAt.After(now) {
			ids = append(ids, g.ID)
			if len(ids) == cycleExpiryLimit {
				break
			}
		}
	}
	return ids
}
func retireExpiredCycleGrants(ctx context.Context, store *admission.SQLiteStore, grants []admission.BrokerGrant, now time.Time) error {
	for _, id := range cycleExpiryCandidates(grants, now) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store.RetireUnusedBrokerGrant(ctx, id, true); err != nil {
			return err
		}
	}
	return nil
}
