package profilesnapshot

import (
	"encoding/json"
	"math"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// Accounting is a projection of the SAME read transaction as the profile map.
// Allocated already includes unused grants and physical/unknown charges. Never
// add UnusedGrants to it again. Counts describe ledger entries, not idle runners,
// usable grants, native execution permission or confirmed physical completion.
type Accounting struct {
	Sequence                      int64                  `json:"sequence"`
	ReconciliationEpoch           int64                  `json:"reconciliation_epoch"`
	LedgerReady                   bool                   `json:"ledger_ready"`
	Policy                        admission.BrokerPolicy `json:"policy"`
	Budgets                       admission.Allocations  `json:"budgets"`
	Allocated                     admission.Allocations  `json:"allocated"`
	UnusedGrants                  admission.Allocations  `json:"unused_grants"`
	UnusedGrantCounts             map[string]int64       `json:"unused_grant_counts"`
	ConsumedUnreleasedGrantCounts map[string]int64       `json:"consumed_unreleased_grant_counts"`
}

func AccountingDigest(a Accounting) string { b, _ := json.Marshal(a); return digestBytes(b) }
func emptyAllocations() admission.Allocations {
	return admission.Allocations{Hosts: map[string]admission.Resources{}, Pools: map[string]admission.Resources{}}
}
func addResources(a, b admission.Resources) (admission.Resources, bool) {
	x := []int64{a.CPU, a.RAM, a.Disk, a.Runners}
	y := []int64{b.CPU, b.RAM, b.Disk, b.Runners}
	for i := range x {
		if x[i] < 0 || y[i] < 0 || x[i] > math.MaxInt64-y[i] {
			return admission.Resources{}, false
		}
		x[i] += y[i]
	}
	return admission.Resources{CPU: x[0], RAM: x[1], Disk: x[2], Runners: x[3]}, true
}
func addAllocation(a *admission.Allocations, host, pool string, r admission.Resources) bool {
	global, ok := addResources(a.Global, r)
	if !ok {
		return false
	}
	h, ok := addResources(a.Hosts[host], r)
	if !ok {
		return false
	}
	p, ok := addResources(a.Pools[pool], r)
	if !ok {
		return false
	}
	a.Global = global
	a.Hosts[host] = h
	a.Pools[pool] = p
	return true
}
func nonnegative(r admission.Resources) bool {
	return r.CPU >= 0 && r.RAM >= 0 && r.Disk >= 0 && r.Runners >= 0
}
func within(a, b admission.Resources) bool {
	return a.CPU <= b.CPU && a.RAM <= b.RAM && a.Disk <= b.Disk && a.Runners <= b.Runners
}
func projectAccounting(s admission.BrokerGrantState) (Accounting, error) {
	a := Accounting{Sequence: s.Sequence, ReconciliationEpoch: s.Epoch, LedgerReady: s.Ready, Policy: s.Policy, Budgets: emptyAllocations(), Allocated: s.Allocated, UnusedGrants: emptyAllocations(), UnusedGrantCounts: map[string]int64{}, ConsumedUnreleasedGrantCounts: map[string]int64{}}
	a.Policy.Repositories = append([]admission.BrokerRepositoryPolicy{}, s.Policy.Repositories...)
	a.Budgets.Global = s.Config.Global
	for id, h := range s.Config.Hosts {
		a.Budgets.Hosts[id] = h.Budget
	}
	for id, p := range s.Config.Pools {
		a.Budgets.Pools[id] = p.Budget
		a.UnusedGrantCounts[id] = 0
		a.ConsumedUnreleasedGrantCounts[id] = 0
	}
	seen := map[string]bool{}
	if len(s.Grants) > 100000 {
		return Accounting{}, ErrSnapshot
	}
	for _, g := range s.Grants {
		if g.ID == "" || seen[g.ID] {
			return Accounting{}, ErrSnapshot
		}
		seen[g.ID] = true
		pool := g.Spec.Authority.PoolID
		if _, ok := s.Config.Pools[pool]; !ok {
			return Accounting{}, ErrSnapshot
		}
		if _, ok := s.Config.Hosts[g.Spec.Host]; !ok {
			return Accounting{}, ErrSnapshot
		}
		switch g.State {
		case "unused":
			if g.Spec.Resources.Runners != 1 {
				return Accounting{}, ErrSnapshot
			}
			// Timestamp expiry alone does not free the canonical charge.
			if !addAllocation(&a.UnusedGrants, g.Spec.Host, pool, g.Spec.Resources) {
				return Accounting{}, ErrSnapshot
			}
			a.UnusedGrantCounts[pool]++
		case "consumed":
			if r, ok := s.Reservations[g.RequestKey]; ok && !s.Released[g.RequestKey] {
				if r.Request.Key != g.RequestKey || r.Request.Pool != pool || r.Request.Host != g.Spec.Host {
					return Accounting{}, ErrSnapshot
				}
				a.ConsumedUnreleasedGrantCounts[pool]++
			}
		case "revoked", "expired":
		default:
			return Accounting{}, ErrSnapshot
		}
	}
	if validateAccounting(a, s.Sequence) != nil {
		return Accounting{}, ErrSnapshot
	}
	return a, nil
}
func validateAccounting(a Accounting, sequence int64) error {
	if a.Sequence != sequence || a.Sequence < 0 || a.ReconciliationEpoch < 0 || a.Policy.Revision > a.Sequence || admission.ValidateBrokerPolicy(a.Policy) != nil || len(a.Budgets.Hosts) > 1000 || len(a.Budgets.Pools) > 256 || !nonnegative(a.Budgets.Global) {
		return ErrSnapshot
	}
	for _, r := range a.Budgets.Hosts {
		if !nonnegative(r) {
			return ErrSnapshot
		}
	}
	for _, r := range a.Budgets.Pools {
		if !nonnegative(r) {
			return ErrSnapshot
		}
	}
	for _, v := range []admission.Allocations{a.Allocated, a.UnusedGrants} {
		if len(v.Hosts) > 1000 || len(v.Pools) > 256 || !nonnegative(v.Global) {
			return ErrSnapshot
		}
		hostTotal, poolTotal := admission.Resources{}, admission.Resources{}
		for id, r := range v.Hosts {
			if _, ok := a.Budgets.Hosts[id]; !ok {
				return ErrSnapshot
			}
			var ok bool
			hostTotal, ok = addResources(hostTotal, r)
			if !ok {
				return ErrSnapshot
			}
		}
		for id, r := range v.Pools {
			if _, ok := a.Budgets.Pools[id]; !ok {
				return ErrSnapshot
			}
			var ok bool
			poolTotal, ok = addResources(poolTotal, r)
			if !ok {
				return ErrSnapshot
			}
		}
		if hostTotal != v.Global || poolTotal != v.Global {
			return ErrSnapshot
		}
	}
	// Over-budget canonical allocations remain visible, never erased. Unused
	// subset must still be contained in canonical accounting in every dimension.
	if !within(a.UnusedGrants.Global, a.Allocated.Global) {
		return ErrSnapshot
	}
	for id, r := range a.UnusedGrants.Hosts {
		if !within(r, a.Allocated.Hosts[id]) {
			return ErrSnapshot
		}
	}
	for id, r := range a.UnusedGrants.Pools {
		if !within(r, a.Allocated.Pools[id]) {
			return ErrSnapshot
		}
	}
	for _, counts := range []map[string]int64{a.UnusedGrantCounts, a.ConsumedUnreleasedGrantCounts} {
		if len(counts) != len(a.Budgets.Pools) {
			return ErrSnapshot
		}
		for id, n := range counts {
			if _, ok := a.Budgets.Pools[id]; !ok || n < 0 || n > 100000 {
				return ErrSnapshot
			}
		}
	}
	for id, n := range a.UnusedGrantCounts {
		if n != a.UnusedGrants.Pools[id].Runners || a.ConsumedUnreleasedGrantCounts[id] > a.Allocated.Pools[id].Runners-n {
			return ErrSnapshot
		}
	}
	return nil
}
