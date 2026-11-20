package brokerinput

import (
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// verifiedStockProviderRoute is an input contract for an offline calculation,
// not a verifier or authorization token. Only an independent observer of the
// actual stock provider route can supply this evidence; configured pool host
// allowlists, a daemon binding and pressure samples do not prove that route.
// This intentionally represents only a provider with one verified destination.
type verifiedStockProviderRoute struct {
	source, provider, host string
	hostBindingSequence    int64
	observedAt             time.Time
}

// planRoutedStockPreview is deliberately not wired to runtime. It narrows
// only new placement candidates, leaves every existing charge on its original
// host, and returns the earliest route expiry for any future advisory caller.
func planRoutedStockPreview(state admission.BrokerGrantState, snapshot capacitybroker.Snapshot, observation garmapi.DemandObservation, source string, opt capacitybroker.Options, routes []verifiedStockProviderRoute) (capacitybroker.JobPlan, time.Time, error) {
	fail := func() (capacitybroker.JobPlan, time.Time, error) {
		return capacitybroker.JobPlan{}, time.Time{}, capacitybroker.ErrSnapshot
	}
	if source == "" || len(routes) == 0 || len(routes) > 1000 || len(snapshot.Pools) > 1000 ||
		len(observation.Pools) > 1000 || opt.Now.IsZero() || opt.MaxAge <= 0 || len(snapshot.Hosts) > 1000 {
		return fail()
	}
	byProvider := make(map[string]verifiedStockProviderRoute, len(routes))
	var expiry time.Time
	for _, route := range routes {
		binding, bound := state.HostBindings[route.host]
		_, known := snapshot.Hosts[route.host]
		deadline := route.observedAt.Add(opt.MaxAge)
		if route.source != source || route.provider == "" || route.host == "" || !bound ||
			!known || route.hostBindingSequence <= 0 || route.hostBindingSequence != binding.Sequence ||
			route.observedAt.IsZero() || route.observedAt.After(opt.Now) || !deadline.After(opt.Now) {
			return fail()
		}
		if _, duplicate := byProvider[route.provider]; duplicate {
			return fail() // Ambiguous or repeated provenance is not corroboration.
		}
		byProvider[route.provider] = route
		if expiry.IsZero() || deadline.Before(expiry) {
			expiry = deadline
		}
	}
	observed := make(map[string]garmapi.DiscoveredPool, len(observation.Pools))
	for _, remote := range observation.Pools {
		if _, duplicate := observed[remote.ID]; duplicate {
			return fail()
		}
		observed[remote.ID] = remote
	}
	used := map[string]bool{}
	pools := make(map[string]capacitybroker.Pool, len(snapshot.Pools))
	for id, p := range snapshot.Pools {
		remote, seen := observed[id]
		profile, enrolled := state.PoolProfiles[id]
		route, routed := byProvider[remote.ProviderName]
		if !seen || !enrolled || !routed || profile.SourceIdentity != source ||
			profile.Pool != id || profile.Repository != p.Repository || profile.Repository != remote.RepositoryID ||
			profile.Authority.PoolID != id || profile.Authority.ProviderName != remote.ProviderName ||
			profile.PoolFingerprint == "" || profile.PoolFingerprint != remote.OwnershipFingerprint {
			return fail()
		}
		// A current global host binding cannot refresh a stale pool enrollment.
		// Require the exact route destination to be enrolled at that binding.
		if len(profile.Domains) == 0 || len(profile.Domains) > 1000 {
			return fail()
		}
		matches := 0
		for _, domain := range profile.Domains {
			if domain.Host != route.host {
				continue
			}
			if domain.HostBindingSequence != route.hostBindingSequence {
				return fail()
			}
			matches++
		}
		if matches != 1 {
			return fail()
		}
		if len(p.Hosts) > len(snapshot.Hosts) {
			return fail()
		}
		allowed := make(map[string]bool, len(p.Hosts))
		for _, host := range p.Hosts {
			if _, known := snapshot.Hosts[host]; !known || allowed[host] {
				return fail()
			}
			allowed[host] = true
		}
		if !allowed[route.host] {
			return fail()
		}
		used[route.provider] = true
		p.Hosts = []string{route.host}
		pools[id] = p
	}
	if len(used) != len(byProvider) {
		return fail()
	}
	snapshot.Pools = pools
	plan, err := stockPlan(snapshot, state.Policy, observation, opt, true, true)
	if err != nil {
		return fail()
	}
	return plan, expiry, nil
}
