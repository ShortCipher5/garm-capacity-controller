package admission

import (
	"context"
	"errors"
	"reflect"
	"sort"
)

// BrokerPlanningCharge is an individual canonical ledger charge for advisory
// allocation. It is not a grant, enrollment proof or native execution authority.
type BrokerPlanningCharge struct {
	ID, Host, Pool string
	Resources      Resources
}

// ReadBrokerPlanningState reads policy, readiness, configuration, all charges and
// audit sequence in one coherent transaction. The returned source state remains
// authoritative for grant CAS; charges are detached advisory planning input.
// In particular, Ready proves only ledger freshness, not exclusive native access.
func (s *SQLiteStore) ReadBrokerPlanningState(ctx context.Context) (BrokerGrantState, []BrokerPlanningCharge, error) {
	state, err := s.ReadBrokerGrantState(ctx)
	if err != nil {
		return BrokerGrantState{}, nil, err
	}
	charges, err := brokerPlanningCharges(state)
	if err != nil {
		return BrokerGrantState{}, nil, err
	}
	// Compare the detached projection with the aggregate actually validated
	// in the same SQLite transaction, including grant-origin/history checks.
	total := Allocations{Hosts: map[string]Resources{}, Pools: map[string]Resources{}}
	for _, c := range charges {
		h, p := total.Hosts[c.Host], total.Pools[c.Pool]
		if !accumulate(&total.Global, c.Resources) || !accumulate(&h, c.Resources) || !accumulate(&p, c.Resources) {
			return BrokerGrantState{}, nil, errors.New("planning charge overflow")
		}
		total.Hosts[c.Host], total.Pools[c.Pool] = h, p
	}
	if err := addDomainOverheads(&total, state.Config, state.DomainOverheads); err != nil {
		return BrokerGrantState{}, nil, err
	}
	for h := range state.Allocated.Hosts {
		if _, ok := total.Hosts[h]; !ok {
			total.Hosts[h] = Resources{}
		}
	}
	for p := range state.Allocated.Pools {
		if _, ok := total.Pools[p]; !ok {
			total.Pools[p] = Resources{}
		}
	}
	if !reflect.DeepEqual(total, state.Allocated) {
		return BrokerGrantState{}, nil, errors.New("transactional planning accounting mismatch")
	}
	return state, charges, nil
}

func brokerPlanningCharges(s BrokerGrantState) ([]BrokerPlanningCharge, error) {
	expected, err := sqliteAllocations(s.Config, s.Reservations, s.Inventory, s.Released)
	if err != nil {
		return nil, err
	}
	charges := []BrokerPlanningCharge{}
	byKey := map[string]int{}
	for key, r := range s.Reservations {
		if key != r.Request.Key {
			return nil, errors.New("planning reservation identity mismatch")
		}
		if s.Released[key] {
			continue
		}
		byKey[key] = len(charges)
		charges = append(charges, BrokerPlanningCharge{"reservation:" + key, r.Request.Host, r.Request.Pool, r.Request.Resources})
	}
	for _, item := range s.Inventory {
		if index, linked := byKey[item.ReservationKey]; linked {
			charge := &charges[index]
			charge.Resources.CPU = max(charge.Resources.CPU, item.Resources.CPU)
			charge.Resources.RAM = max(charge.Resources.RAM, item.Resources.RAM)
			charge.Resources.Disk = max(charge.Resources.Disk, item.Resources.Disk)
			continue
		}
		// A reappearing resource with a released reservation stays charged in
		// full, exactly like an unlinked orphan. Never filter by observed state.
		charges = append(charges, BrokerPlanningCharge{"inventory:" + item.Host + ":" + item.ID, item.Host, item.Pool, item.Resources})
	}
	for _, grant := range s.Grants {
		if grant.State != "unused" {
			continue
		}
		// Expiry is not a release. Only the durable terminal transition removes
		// an unused grant; consumed grants are counted through reservations.
		g := grant.Spec
		charges = append(charges, BrokerPlanningCharge{"grant:" + grant.ID, g.Host, g.Authority.PoolID, g.Resources})
		h, p := expected.Hosts[g.Host], expected.Pools[g.Authority.PoolID]
		if !accumulate(&expected.Global, g.Resources) || !accumulate(&h, g.Resources) || !accumulate(&p, g.Resources) {
			return nil, errors.New("planning grant overflow")
		}
		expected.Hosts[g.Host], expected.Pools[g.Authority.PoolID] = h, p
	}
	actual := Allocations{Hosts: map[string]Resources{}, Pools: map[string]Resources{}}
	seen := map[string]bool{}
	for _, charge := range charges {
		if seen[charge.ID] {
			return nil, errors.New("planning charge identity conflict")
		}
		seen[charge.ID] = true
		if _, ok := s.Config.Hosts[charge.Host]; !ok {
			return nil, errors.New("planning charge host unavailable")
		}
		if _, ok := s.Config.Pools[charge.Pool]; !ok {
			return nil, errors.New("planning charge pool unavailable")
		}
		h, p := actual.Hosts[charge.Host], actual.Pools[charge.Pool]
		if !accumulate(&actual.Global, charge.Resources) || !accumulate(&h, charge.Resources) || !accumulate(&p, charge.Resources) {
			return nil, errors.New("planning charge overflow")
		}
		actual.Hosts[charge.Host], actual.Pools[charge.Pool] = h, p
	}
	if err := addDomainOverheads(&expected, s.Config, s.DomainOverheads); err != nil {
		return nil, err
	}
	if err := addDomainOverheads(&actual, s.Config, s.DomainOverheads); err != nil {
		return nil, err
	}
	// Canonical accounting may retain zero entries from linked resources.
	for host := range expected.Hosts {
		if _, ok := actual.Hosts[host]; !ok {
			actual.Hosts[host] = Resources{}
		}
	}
	for pool := range expected.Pools {
		if _, ok := actual.Pools[pool]; !ok {
			actual.Pools[pool] = Resources{}
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		return nil, errors.New("planning charge accounting mismatch")
	}
	sort.Slice(charges, func(i, j int) bool { return charges[i].ID < charges[j].ID })
	return charges, nil
}
