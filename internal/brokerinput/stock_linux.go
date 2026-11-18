// Package brokerinput joins saved policy and stock-GARM observations for capacity
// planning. Its output is not an atomic admission snapshot or a provider permit.
package brokerinput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// Stock plans from caller-supplied complete, deduplicated ledger charges and
// profile/host mappings. Pool IDs must be stock UUIDs. It never replaces historical
// charge sizes with current profile sizes or drops unmanaged pool charges.
// Stock idle observations may cover queue demand, not authorize physical release.
func Stock(capacity capacitybroker.Snapshot, policy admission.BrokerPolicy, observation garmapi.DemandObservation, opt capacitybroker.Options) (capacitybroker.JobPlan, error) {
	fail := func() (capacitybroker.JobPlan, error) { return capacitybroker.JobPlan{}, capacitybroker.ErrSnapshot }
	if capacity.ObservedAt.IsZero() || capacity.ObservedAt.After(opt.Now) || capacity.Revision == "" || len(capacity.Charges) > 100000 || admission.ValidateBrokerPolicy(policy) != nil || observation.Digest == "" || observation.StartedAt.IsZero() || observation.FinishedAt.Before(observation.StartedAt) || observation.FinishedAt.After(opt.Now) || len(observation.Pools) > 1000 || len(observation.Jobs) > 1000 || len(observation.Supply) > 1000 {
		return fail()
	}
	pools := map[string]garmapi.DiscoveredPool{}
	for _, p := range observation.Pools {
		if p.ID == "" {
			return fail()
		}
		if _, ok := pools[p.ID]; ok {
			return fail()
		}
		pools[p.ID] = p
	}
	supply := map[string]garmapi.RunnerSupply{}
	for _, p := range observation.Supply {
		if _, ok := pools[p.PoolID]; !ok {
			return fail()
		}
		if _, ok := supply[p.PoolID]; ok {
			return fail()
		}
		if p.Idle < 0 || p.Creating < 0 || p.Busy < 0 || p.Other < 0 || p.Idle > 1000 || p.Creating > 100000 || p.Busy > 100000 || p.Other > 100000 {
			return fail()
		}
		supply[p.PoolID] = p
	}
	// Freeze copied policy inputs. Never mutate a shared ledger snapshot.
	cp := map[string]capacitybroker.Pool{}
	cr := map[string]capacitybroker.Repository{}
	for id, p := range capacity.Pools {
		if p.Budget != nil {
			budget := *p.Budget
			p.Budget = &budget
		}
		p.Waiting = 0
		p.OldestWaiting = capacitybroker.Pool{}.OldestWaiting
		p.MaxRunners = 0
		p.WarmIdle = 0
		p.AvailableIdle = 0
		cp[id] = p
	}
	for id, r := range capacity.Repositories {
		r.MaxRunners = 0
		cr[id] = r
	}
	enrolled := map[string]string{}
	for _, r := range policy.Repositories {
		if _, ok := cr[r.ID]; !ok {
			return fail()
		}
		cr[r.ID] = capacitybroker.Repository{Weight: r.Weight, MaxRunners: r.MaxRunners}
		for _, desired := range r.Pools {
			p, ok := cp[desired.ID]
			if !ok || p.Repository != r.ID {
				return fail()
			}
			observed, ok := pools[desired.ID]
			if !ok || observed.OwnerKind != "repository" || observed.OwnerID != r.ID || observed.RepositoryID != r.ID {
				return fail()
			}
			current, ok := supply[desired.ID]
			if !ok {
				return fail()
			}
			charged := int64(0)
			for _, c := range capacity.Charges {
				if c.Pool == desired.ID {
					if c.Resources[3] < 0 || c.Resources[3] > 100000 || charged > 100000-c.Resources[3] {
						return fail()
					}
					charged += c.Resources[3]
				}
			}
			if current.Idle+current.Creating+current.Busy+current.Other > charged {
				return fail()
			}
			if observed.Enabled {
				p.MaxRunners = desired.MaxRunners
				p.WarmIdle = desired.WarmIdle
				enrolled[desired.ID] = r.ID
			}
			p.AvailableIdle = current.Idle
			cp[desired.ID] = p
		}
	}
	jobs := make([]capacitybroker.QueuedDemand, 0, len(observation.Jobs))
	for _, j := range observation.Jobs {
		if j.WorkflowJobID <= 0 {
			return fail()
		}
		candidates := []string{}
		seen := map[string]bool{}
		for _, id := range j.CandidatePoolIDs {
			if seen[id] {
				return fail()
			}
			seen[id] = true
			p, ok := pools[id]
			if !ok || p.RepositoryID != j.RepositoryID {
				return fail()
			}
			if repo, ok := enrolled[id]; ok {
				if repo != j.RepositoryID {
					return fail()
				}
				candidates = append(candidates, id)
			}
		}
		jobs = append(jobs, capacitybroker.QueuedDemand{ID: j.RepositoryID + ":" + strconv.FormatInt(j.WorkflowJobID, 10), Candidates: candidates, CreatedAt: j.CreatedAt})
	}
	capacity.Pools = cp
	capacity.Repositories = cr
	if observation.StartedAt.Before(capacity.ObservedAt) {
		capacity.ObservedAt = observation.StartedAt
	}
	raw, e := json.Marshal(struct {
		Capacity string
		Policy   int64
		Demand   string
	}{capacity.Revision, policy.Revision, observation.Digest})
	if e != nil {
		return fail()
	}
	hash := sha256.Sum256(raw)
	capacity.Revision = hex.EncodeToString(hash[:])
	return capacitybroker.BuildForJobs(capacity, opt, jobs)
}
