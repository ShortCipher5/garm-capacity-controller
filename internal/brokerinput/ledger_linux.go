package brokerinput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
)

// LedgerProfile is trusted server-side planning configuration, not provider
// input. It is not enrollment or execution authority. Every ledger pool,
// including unmanaged historical pools, requires explicit ownership and size.
type LedgerProfile struct {
	Repository string
	Size       admission.Resources
	// Optional exact stock execution binding. Without it no outstanding grant
	// is treated as reusable supply; its charge is always retained.
	Authority admission.StockProviderAuthority
	Execution admission.ExecutionImage
}

// Ledger obtains one coherent policy/configuration/charge revision from SQLite.
// It does not manufacture a pool profile from a total pool budget. Callers must
// supply reviewed profiles and later use the returned Sequence for grant CAS.
// No permissions or native readiness are implied by this advisory snapshot.
func Ledger(ctx context.Context, store *admission.SQLiteStore, profiles map[string]LedgerProfile, opt capacitybroker.Options) (capacitybroker.Snapshot, admission.BrokerGrantState, error) {
	if store == nil {
		return capacitybroker.Snapshot{}, admission.BrokerGrantState{}, capacitybroker.ErrSnapshot
	}
	state, charges, err := store.ReadBrokerPlanningState(ctx)
	if err != nil {
		return capacitybroker.Snapshot{}, admission.BrokerGrantState{}, err
	}
	snapshot, err := ledgerSnapshot(state, charges, profiles, opt)
	if err != nil {
		return capacitybroker.Snapshot{}, admission.BrokerGrantState{}, err
	}
	return snapshot, state, nil
}

func resources(r admission.Resources) capacitybroker.Resources {
	return capacitybroker.Resources{r.CPU, r.RAM, r.Disk, r.Runners}
}
func withinBudget(used, budget admission.Resources) bool {
	u, b := resources(used), resources(budget)
	for i := range u {
		if u[i] < 0 || b[i] < 0 || u[i] > b[i] {
			return false
		}
	}
	return true
}
func ledgerSnapshot(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, profiles map[string]LedgerProfile, opt capacitybroker.Options) (capacitybroker.Snapshot, error) {
	fail := func() (capacitybroker.Snapshot, error) { return capacitybroker.Snapshot{}, capacitybroker.ErrSnapshot }
	if !state.Ready || state.Sequence < 0 || state.Config.MaxObservationAge <= 0 || len(profiles) != len(state.Config.Pools) || len(state.Config.Hosts) == 0 || admission.ValidateBrokerPolicy(state.Policy) != nil {
		return fail()
	}
	globalBudget, hostBudgets, err := overheadBudgets(state)
	if err != nil {
		return fail()
	}
	s := capacitybroker.Snapshot{Global: globalBudget, Hosts: map[string]capacitybroker.Host{}, Repositories: map[string]capacitybroker.Repository{}, Pools: map[string]capacitybroker.Pool{}, Complete: true}
	for id, h := range state.Config.Hosts {
		if h.ObservedAt.IsZero() || h.ObservedAt.After(opt.Now) || opt.Now.Sub(h.ObservedAt) > state.Config.MaxObservationAge {
			return fail()
		}
		if s.ObservedAt.IsZero() || h.ObservedAt.Before(s.ObservedAt) {
			s.ObservedAt = h.ObservedAt
		}
		s.Hosts[id] = capacitybroker.Host{Budget: hostBudgets[id], AcceptNew: h.Mode == "enabled" && h.Healthy}
	}
	for id, p := range state.Config.Pools {
		profile, ok := profiles[id]
		if !ok || profile.Repository == "" {
			return fail()
		}
		budget := resources(p.Budget)
		s.Pools[id] = capacitybroker.Pool{Budget: &budget, Repository: profile.Repository, Size: resources(profile.Size), Hosts: append([]string(nil), p.Hosts...)}
		s.Repositories[profile.Repository] = capacitybroker.Repository{}
	}
	for _, r := range state.Policy.Repositories {
		s.Repositories[r.ID] = capacitybroker.Repository{Weight: r.Weight, MaxRunners: r.MaxRunners}
		for _, p := range r.Pools {
			pool, ok := s.Pools[p.ID]
			if !ok || pool.Repository != r.ID {
				return fail()
			}
			pool.MaxRunners, pool.WarmIdle = p.MaxRunners, p.WarmIdle
			s.Pools[p.ID] = pool
		}
	}
	for _, c := range charges {
		s.Charges = append(s.Charges, capacitybroker.Charge{ID: c.ID, Host: c.Host, Pool: c.Pool, Resources: resources(c.Resources)})
	}
	// Grant schema does not bind a queued job. Reuse each unused pool grant
	// once as provisional planned supply and suppress extra warm issuance.
	// IDs and states remain durable in the ledger; no job-to-runner claim.
	for _, g := range state.Grants {
		if g.RedeemableAt(opt.Now) && (state.Policy.Revision == 0 || g.EvidenceBound) {
			profile, exists := profiles[g.Spec.Authority.PoolID]
			binding, bound := state.HostBindings[g.Spec.Host]
			host, hostKnown := s.Hosts[g.Spec.Host]
			eligible := false
			for _, id := range state.Config.Pools[g.Spec.Authority.PoolID].Hosts {
				if id == g.Spec.Host {
					eligible = true
				}
			}
			if !withinBudget(state.Allocated.Global, state.Config.Global) ||
				!withinBudget(state.Allocated.Hosts[g.Spec.Host], state.Config.Hosts[g.Spec.Host].Budget) ||
				!withinBudget(state.Allocated.Pools[g.Spec.Authority.PoolID], state.Config.Pools[g.Spec.Authority.PoolID].Budget) ||
				!exists || !bound || binding.Sequence != g.Spec.HostBindingSequence || !hostKnown || !host.AcceptNew || !eligible || g.Spec.Resources != profile.Size ||
				profile.Authority != g.Spec.Authority || !reflect.DeepEqual(profile.Execution, g.Spec.Execution) {
				// Expired or obsolete-profile grants stay charged, but cannot
				// represent demand service. Only durable retirement frees them.
				continue
			}
			p, ok := s.Pools[g.Spec.Authority.PoolID]
			if !ok {
				return fail()
			}
			p.PendingWarm += g.Spec.Resources.Runners
			p.OutstandingGrants += g.Spec.Resources.Runners
			s.Pools[g.Spec.Authority.PoolID] = p
		}
	}
	raw, err := json.Marshal(struct {
		Sequence int64
		Policy   admission.BrokerPolicy
		Profiles map[string]LedgerProfile
		Snapshot capacitybroker.Snapshot
	}{state.Sequence, state.Policy, profiles, s})
	if err != nil {
		return fail()
	}
	hash := sha256.Sum256(raw)
	s.Revision = hex.EncodeToString(hash[:])
	// Run the same bounded structural/freshness validation as the planner.
	// Discard the advisory assignments; this function never issues grants.
	if _, err := capacitybroker.Build(s, opt); err != nil {
		return fail()
	}
	return s, nil
}
