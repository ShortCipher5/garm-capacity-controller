package admission

import (
	"database/sql"
	"reflect"
)

// A saved policy restricts new ledger commitments. Its repository IDs are never
// ownership evidence: resolve each candidate and every retained pool charge via
// the observed, immutable profile history in the same write transaction.
type sharedAdmission struct {
	groups     *groupIndex
	profiles   map[string]BrokerPoolProfile
	repoLimits map[string]int64
	poolLimits map[string]int64
	runners    map[string]int64
}

func readSharedAdmission(tx *sql.Tx, policy BrokerPolicy, allocated Allocations) (*sharedAdmission, error) {
	// Revision zero predates the first operator policy. Replacing policy with
	// an empty document advances the revision and therefore denies all growth.
	if policy.Revision == 0 && len(policy.Repositories) == 0 {
		return nil, nil
	}
	ambiguous := map[string]bool{}
	profiles, err := readBrokerPoolProfiles(tx, ambiguous)
	if err != nil {
		return nil, err
	}
	s := &sharedAdmission{profiles: profiles, repoLimits: map[string]int64{}, poolLimits: map[string]int64{}, runners: map[string]int64{}}
	s.groups, err = indexGroups(policy)
	if err != nil {
		return nil, err
	}
	for _, repo := range policy.Repositories {
		s.repoLimits[repo.ID] = repo.MaxRunners
		for _, pool := range repo.Pools {
			if profiles[pool.ID].Repository != repo.ID || ambiguous[pool.ID] {
				return nil, ErrBrokerGrant
			}
			s.poolLimits[pool.ID] = pool.MaxRunners
		}
	}
	// A repository UUID observed at different stock authorities is ambiguous.
	// Its saved ceiling cannot safely combine those namespaces.
	owners := map[string]BrokerPoolProfile{}
	for _, p := range profiles {
		if _, applies := s.repoLimits[p.Repository]; !applies {
			continue
		}
		prev, ok := owners[p.Repository]
		if ok && (prev.SourceIdentity != p.SourceIdentity || prev.Authority.Controller != p.Authority.Controller) {
			return nil, ErrBrokerGrant
		}
		owners[p.Repository] = p
	}
	for id, charge := range allocated.Pools {
		if charge.Runners == 0 {
			continue
		}
		p, ok := profiles[id]
		if !ok || ambiguous[id] {
			return nil, ErrBrokerGrant
		}
		// These nonnegative pool subtotals are disjoint parts of the already
		// overflow-checked global runner count, including unlisted siblings.
		s.runners[p.Repository] += charge.Runners
	}
	return s, nil
}

func (s *sharedAdmission) permitsCharge(pool string, size Resources, additional, poolRunners int64) bool {
	if s == nil {
		return true
	}
	p := s.profiles[pool]
	limit, enrolled := s.poolLimits[pool]
	return enrolled && p.Resources == size &&
		fitsBounded(poolRunners, additional, limit) &&
		fitsBounded(s.runners[p.Repository], additional, s.repoLimits[p.Repository])
}

func (s *sharedAdmission) permitsExecution(pool string, execution ExecutionImage, authority *StockProviderAuthority) bool {
	if s == nil {
		return true
	}
	p := s.profiles[pool]
	execution.Name = ""
	return reflect.DeepEqual(p.Execution, execution) && (authority == nil || p.Authority == *authority)
}

func (s *sharedAdmission) permitsGroup(pool, host string, a Allocations, delta Resources) bool {
	return s == nil || s.groups.allows(host, pool, a, delta)
}
func (s *sharedAdmission) permitsHost(pool, host string, sequence int64, c Config) bool {
	if s == nil {
		return true
	}
	// The historical ledger permits a zero (unbudgeted) disk limit. An
	// enrolled shared claim needs an explicit bound at every resource edge.
	if c.Global.Disk == 0 || c.Pools[pool].Budget.Disk == 0 || c.Hosts[host].Budget.Disk == 0 {
		return false
	}
	for _, d := range s.profiles[pool].Domains {
		if d.Host == host && d.HostBindingSequence == sequence {
			return true
		}
	}
	return false
}
