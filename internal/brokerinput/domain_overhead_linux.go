package brokerinput

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"math"
)

// Domain overhead consumes host/global space, never a repository slot or pool
// runner. Whole-capsule ceilings remain charged even when guest charges overlap.
// The ledger authenticates evidence; this detached projection validates its shape and totals.
func overheadBudgets(state admission.BrokerGrantState) (capacitybroker.Resources, map[string]capacitybroker.Resources, error) {
	fail := func() (capacitybroker.Resources, map[string]capacitybroker.Resources, error) {
		return capacitybroker.Resources{}, nil, capacitybroker.ErrSnapshot
	}
	if len(state.DomainOverheads) > 1000 || len(state.ArtifactOverheads) > 1000 {
		return fail()
	}
	global := capacitybroker.Resources{}
	byHost := map[string]capacitybroker.Resources{}
	seen := map[int64]bool{}
	sequences := map[int64]bool{}
	domains := map[string]bool{}
	for _, entry := range state.DomainOverheads {
		if admission.ValidateNativeDomainOverhead(entry) != nil || seen[entry.DomainBindingSequence] || sequences[entry.Sequence] || domains[entry.DomainID] || entry.Sequence > state.Sequence {
			return fail()
		}
		seen[entry.DomainBindingSequence] = true
		sequences[entry.Sequence] = true
		domains[entry.DomainID] = true
		if _, ok := state.Config.Hosts[entry.Host]; !ok {
			return fail()
		}
		r := resources(entry.Resources)
		h := byHost[entry.Host]
		for i, n := range r {
			if n < 0 || global[i] > math.MaxInt64-n || h[i] > math.MaxInt64-n {
				return fail()
			}
			global[i] += n
			h[i] += n
		}
		byHost[entry.Host] = h
	}
	if global != resources(state.Allocated.DomainOverhead) {
		return fail()
	}
	artifacts := capacitybroker.Resources{}
	artifactIDs := map[string]bool{}
	roots := map[string]bool{}
	for _, entry := range state.ArtifactOverheads {
		root := entry.PoolGUID + "\x00" + entry.RootGUID
		if admission.ValidateNativeArtifactOverhead(entry) != nil || sequences[entry.Sequence] || entry.Sequence > state.Sequence || artifactIDs[entry.ArtifactID] || roots[root] {
			return fail()
		}
		if _, ok := state.Config.Hosts[entry.Host]; !ok {
			return fail()
		}
		sequences[entry.Sequence] = true
		artifactIDs[entry.ArtifactID] = true
		roots[root] = true
		h := byHost[entry.Host]
		r := resources(entry.Resources)
		for i, n := range r {
			if n < 0 || artifacts[i] > math.MaxInt64-n || global[i] > math.MaxInt64-n || h[i] > math.MaxInt64-n {
				return fail()
			}
			artifacts[i] += n
			global[i] += n
			h[i] += n
		}
		byHost[entry.Host] = h
	}
	if artifacts != resources(state.Allocated.ArtifactOverhead) {
		return fail()
	}
	canonical := resources(state.Allocated.Global)
	for i, n := range global {
		if canonical[i] < 0 || n > canonical[i] {
			return fail()
		}
	}
	subtract := func(budget, overhead capacitybroker.Resources) (capacitybroker.Resources, bool) {
		for i, n := range budget {
			if n < 0 {
				return capacitybroker.Resources{}, false
			}
			if overhead[i] >= n {
				budget[i] = 0
			} else {
				budget[i] -= overhead[i]
			}
		}
		return budget, true
	}
	budget, ok := subtract(resources(state.Config.Global), global)
	if !ok {
		return fail()
	}
	hosts := map[string]capacitybroker.Resources{}
	for id, h := range state.Config.Hosts {
		overhead := byHost[id]
		allocated := resources(state.Allocated.Hosts[id])
		for i, n := range overhead {
			if allocated[i] < 0 || n > allocated[i] {
				return fail()
			}
		}
		b, ok := subtract(resources(h.Budget), overhead)
		if !ok {
			return fail()
		}
		hosts[id] = b
	}
	return budget, hosts, nil
}
