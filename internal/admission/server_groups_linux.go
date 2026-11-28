package admission

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Group IDs are stable across renames. Host and pool sets are partitions, not
// additional capacity: every group total is derived from physical host charges.
type ServerGroup struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Hosts        []string  `json:"hosts"`
	Repositories []string  `json:"repositories"`
	Pools        []string  `json:"pools"`
	Limit        Resources `json:"limit"`
}

var ErrGroupMembership = errors.New("membership change needs fresh, uncharged members")

type groupIndex struct {
	byHost, byPool map[string]string
	groups         map[string]ServerGroup
}

func indexGroups(p BrokerPolicy) (*groupIndex, error) {
	x := &groupIndex{map[string]string{}, map[string]string{}, map[string]ServerGroup{}}
	repos := map[string][]BrokerPoolPolicy{}
	pools := map[string]bool{}
	repoOwners := map[string]string{}
	for _, r := range p.Repositories {
		repos[r.ID] = r.Pools
		for _, v := range r.Pools {
			pools[v.ID] = true
		}
	}
	if len(p.Groups) > 128 {
		return nil, ErrDenied
	}
	for _, g := range p.Groups {
		if !routeUUID(g.ID) || x.groups[g.ID].ID != "" || !validHostID(g.Name) || len(g.Name) > 80 || strings.TrimSpace(g.Name) != g.Name || g.Repositories == nil || g.Pools == nil || len(g.Hosts) == 0 || len(g.Hosts) > 256 || len(g.Repositories)+len(g.Pools) > 1000 || g.Limit.Disk != 0 || g.Limit.CPU < 0 || g.Limit.RAM < 0 || g.Limit.Runners < 0 || g.Limit.Runners > 1000000 || !validStoredMagnitude(g.Limit) {
			return nil, ErrDenied
		}
		x.groups[g.ID] = g
		for _, h := range g.Hosts {
			if !validHostID(h) || x.byHost[h] != "" {
				return nil, ErrDenied
			}
			x.byHost[h] = g.ID
		}
		for _, r := range g.Repositories {
			members, ok := repos[r]
			if !ok || repoOwners[r] != "" {
				return nil, ErrDenied
			}
			repoOwners[r] = g.ID
			for _, v := range members {
				if x.byPool[v.ID] != "" {
					return nil, ErrDenied
				}
				x.byPool[v.ID] = g.ID
			}
		}
		for _, v := range g.Pools {
			if !pools[v] || x.byPool[v] != "" {
				return nil, ErrDenied
			}
			x.byPool[v] = g.ID
		}
	}
	return x, nil
}
func (g ServerGroup) Charged(a Allocations) (Resources, bool) {
	var used Resources
	for _, h := range g.Hosts {
		if !accumulate(&used, a.Hosts[h]) {
			return Resources{}, false
		}
	}
	return used, true
}
func (x *groupIndex) allows(host, pool string, ad Allocations, delta Resources) bool {
	if x == nil {
		return true
	}
	id := x.byHost[host]
	if x.byPool[pool] != id {
		return false
	}
	if id == "" {
		return true
	}
	g := x.groups[id]
	used, ok := g.Charged(ad)
	return ok && fits(used, delta, g.Limit)
}

// checkGroupEdit is called under the same IMMEDIATE write lock as policy CAS
// and admission. Membership never moves a retained charge, including grants
// and unresolved inventory. Cap reductions do not drain or change old charges.
func checkGroupEdit(tx *sql.Tx, old, new BrokerPolicy, now time.Time) error {
	if len(old.Groups)+len(new.Groups) == 0 {
		return nil
	}
	a, e := indexGroups(old)
	if e != nil {
		return e
	}
	b, e := indexGroups(new)
	if e != nil {
		return e
	}
	c, ready, _, _, e := sqliteMetadata(tx)
	if e != nil {
		return e
	}
	// A host ID must exist locally; pool identities come from saved repo policy
	// and still need independently observed profiles at admission.
	for h := range b.byHost {
		if _, ok := c.Hosts[h]; !ok {
			return ErrDenied
		}
	}
	moved := func(from, to map[string]string) map[string]bool {
		out := map[string]bool{}
		for k, v := range from {
			if to[k] != v {
				out[k] = true
			}
		}
		for k, v := range to {
			if from[k] != v {
				out[k] = true
			}
		}
		return out
	}
	hosts, pools := moved(a.byHost, b.byHost), moved(a.byPool, b.byPool)
	if len(hosts)+len(pools) == 0 {
		return nil
	}
	if !ready || !sqliteObservationsFresh(c, now) {
		return ErrGroupMembership
	}
	r, e := sqliteReservations(tx)
	if e != nil {
		return e
	}
	i, e := sqliteInventory(tx)
	if e != nil {
		return e
	}
	released, e := sqliteReleasedKeys(tx)
	if e != nil {
		return e
	}
	alloc, e := sqliteAllocationsWithGrants(tx, c, r, i, released)
	if e != nil {
		return e
	}
	for h := range hosts {
		if alloc.Hosts[h] != (Resources{}) {
			return ErrGroupMembership
		}
	}
	for p := range pools {
		if alloc.Pools[p] != (Resources{}) {
			return ErrGroupMembership
		}
	}
	return nil
}
