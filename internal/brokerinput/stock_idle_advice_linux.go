package brokerinput

import (
	"crypto/sha256"
	"reflect"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// Private, unwired advice only. A classified stock queue is not complete GitHub
// coverage, a future empty queue, cleanup proof, or reusable physical capacity.
type stockIdleAdvice struct {
	PoolID, RepositoryID                           string
	Observed, Suggested, UnchangedMaximum          int64
	ExpiresAt                                      time.Time
	Actionable, CompleteCoverage, CapacityReleased bool
}
type stockIdleSample struct {
	observation                                garmapi.DemandObservation
	scope                                      [sha256.Size]byte
	source, pool, repo                         string
	sequence, revision, minimum, maximum, warm int64
	expiry                                     time.Time
	maxAge                                     time.Duration
	repos                                      []string
	settings                                   map[string]string
}

func sampleStockIdleAdvice(state admission.BrokerGrantState, charges []admission.BrokerPlanningCharge, observation garmapi.DemandObservation, source, poolID string, opt capacitybroker.Options) (stockIdleSample, error) {
	fail := func() (stockIdleSample, error) { return stockIdleSample{}, capacitybroker.ErrSnapshot }
	if opt.Now.IsZero() || opt.MaxAge <= 0 || opt.MaxAge > time.Minute {
		return fail()
	}
	snapshot, _, err := enrolledPlanningSnapshot(state, charges, observation, source, opt)
	if err != nil {
		return fail()
	}
	scope, err := scalePreviewScope(state, snapshot)
	if err != nil || scope == ([sha256.Size]byte{}) {
		return fail()
	}
	out := stockIdleSample{observation: observation, scope: scope, source: source, pool: poolID, sequence: state.Sequence, revision: state.Policy.Revision, maxAge: opt.MaxAge, settings: map[string]string{}}
	found := false
	for _, r := range state.Policy.Repositories {
		out.repos = append(out.repos, r.ID)
		for _, p := range r.Pools {
			if p.ID == poolID {
				if found {
					return fail()
				}
				found = true
				out.repo = r.ID
				out.warm = p.WarmIdle
			}
		}
	}
	if !found || out.warm < 0 {
		return fail()
	}
	remoteFound := false
	for _, p := range observation.Pools {
		if p.DiscoveryFingerprint == "" {
			return fail()
		}
		out.settings[p.ID] = p.DiscoveryFingerprint
		if p.ID == poolID {
			if remoteFound || p.RepositoryID != out.repo || !p.Enabled || p.MaxRunners == 0 || p.MaxRunners > 1000000 || p.MinIdleRunners == 0 || p.MinIdleRunners > p.MaxRunners {
				return fail()
			}
			remoteFound = true
			out.minimum = int64(p.MinIdleRunners)
			out.maximum = int64(p.MaxRunners)
		}
	}
	if !remoteFound || out.minimum <= out.warm {
		return fail()
	}
	if !stockIdleVisibleSupply(observation.Supply, poolID) {
		return fail()
	}
	out.expiry = projectReadPlan(state, observation, capacitybroker.JobPlan{}, opt.Now, opt.MaxAge).ExpiresAt
	if !out.idleScopeValid(opt.Now) {
		return fail()
	}
	return out, nil
}

// Visible idle is a prerequisite for this recommendation, never release proof.
// Busy rows stay accounted; a creating/unknown row or ambiguous/missing sample
// vetoes rather than guessing that a lower configured floor can free capacity.
func stockIdleVisibleSupply(rows []garmapi.RunnerSupply, pool string) bool {
	count := 0
	for _, r := range rows {
		if r.PoolID == pool {
			count++
			if r.Idle <= 0 || r.Creating != 0 || r.Other != 0 || r.Busy < 0 {
				return false
			}
		}
	}
	return count == 1
}

func (s stockIdleSample) idleScopeValid(now time.Time) bool {
	if !stockIdleVisibleSupply(s.observation.Supply, s.pool) || s.source == "" || s.source != s.observation.SourceIdentity() || !s.expiry.After(now) || s.minimum <= 0 || s.minimum <= s.warm {
		return false
	}
	// Revalidate the complete sealed observation once, not once per repository.
	// Never cache this map across later calls: exported observation slices may
	// have changed and both samples must be fresh at the actual comparison.
	scopes := s.observation.RepositoryIdleScopes(s.repos, now, s.maxAge)
	if len(s.repos) == 0 || len(scopes) != len(s.repos) {
		return false
	}
	for _, repo := range s.repos {
		if !scopes[repo].Classified() {
			return false
		}
	}
	return !stockIdleDemandTouches(s.observation.Jobs, s.repo, s.pool)
}

// Scan every edge irrespective of ordering, not just its first/assigned
// candidate. Repository-local demand vetoes even if presently unmatched.
func stockIdleDemandTouches(jobs []garmapi.QueuedJob, repo, pool string) bool {
	for _, j := range jobs {
		if j.RepositoryID == repo {
			return true
		}
		for _, id := range j.CandidatePoolIDs {
			if id == pool {
				return true
			}
		}
		for _, id := range j.IdleOnlyPoolIDs {
			if id == pool {
				return true
			}
		}
	}
	return false
}

func proposeStockIdleDecrease(previous, current stockIdleSample, now time.Time) (stockIdleAdvice, bool) {
	if !previous.idleScopeValid(now) || !current.idleScopeValid(now) || !current.observation.StartedAt.After(previous.observation.FinishedAt) || previous.scope != current.scope || previous.source != current.source || previous.pool != current.pool || previous.repo != current.repo || previous.revision != current.revision || previous.sequence > current.sequence || previous.minimum != current.minimum || previous.maximum != current.maximum || previous.warm != current.warm || !reflect.DeepEqual(previous.settings, current.settings) {
		return stockIdleAdvice{}, false
	}
	expiry := current.expiry
	if previous.expiry.Before(expiry) {
		expiry = previous.expiry
	}
	return stockIdleAdvice{PoolID: current.pool, RepositoryID: current.repo, Observed: current.minimum, Suggested: current.minimum - 1, UnchangedMaximum: current.maximum, ExpiresAt: expiry}, true
}
