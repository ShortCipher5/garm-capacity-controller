package garmapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const RepositoryIdleScopeVersion = "normal-pool-idle-scope/v1"

// RepositoryIdleAssessment describes only the classifiability of this API's
// observed queue within one permitted repository. It is NOT an empty-queue,
// atomicity, GitHub missed-webhook, release or settings-write guarantee.
// The zero value is absent; JSON round trips cannot construct this evidence.
type RepositoryIdleAssessment struct {
	classified bool
	version    string
}

func (a RepositoryIdleAssessment) Version() string { return a.version }
func (a RepositoryIdleAssessment) Classified() bool {
	return a.version == RepositoryIdleScopeVersion && a.classified
}

type repositoryIdleScope struct {
	source, digest string
	start, finish  time.Time
	blocked        map[string]bool
}

// ObserveWithRepositoryIdleScope is explicit opt-in. It uses exactly Observe's
// existing bounded reads and queue projection, without additional permissions,
// requests, public fields or runtime callers. Legacy Observe mints no assessment.
func (c *DemandClient) ObserveWithRepositoryIdleScope(ctx context.Context) (DemandObservation, error) {
	return c.observe(ctx, true)
}

// RepositoryIdleScope refuses absent, stale, altered-identity and unknown-scope
// evidence. A classified queue may still contain demand, so callers must not
// interpret this result as an idle-reduction recommendation.
func (o DemandObservation) RepositoryIdleScope(repo string, now time.Time, maxAge time.Duration) RepositoryIdleAssessment {
	return o.RepositoryIdleScopes([]string{repo}, now, maxAge)[repo]
}

// RepositoryIdleScopes verifies the same complete observation once for a
// bounded batch. Nil means unavailable: empty, duplicate, unknown or more than
// 1000 requested repository IDs fail closed, as does any invalid observation.
// Successful entries can still be unclassified. The returned map owns its
// values; it is not a freshness cache or permission to reduce idle targets.
func (o DemandObservation) RepositoryIdleScopes(repositories []string, now time.Time, maxAge time.Duration) map[string]RepositoryIdleAssessment {
	s := o.idleScope
	if s == nil || s.source == "" || s.source != o.sourceIdentity || s.digest != o.Digest || s.start != o.StartedAt || s.finish != o.FinishedAt || now.IsZero() || maxAge <= 0 || maxAge > time.Minute || s.start.IsZero() || s.finish.Before(s.start) || s.finish.After(now) || !s.start.Add(maxAge).After(now) {
		return nil
	}
	// Validate the complete requested scope before hashing. All-or-nothing
	// refusal prevents a partial map being mistaken for full scope coverage.
	if len(repositories) == 0 || len(repositories) > 1000 {
		return nil
	}
	out := make(map[string]RepositoryIdleAssessment, len(repositories))
	for _, repo := range repositories {
		blocked, known := s.blocked[repo]
		if _, duplicate := out[repo]; !known || duplicate {
			return nil
		}
		out[repo] = RepositoryIdleAssessment{classified: !blocked, version: RepositoryIdleScopeVersion}
	}
	raw, err := json.Marshal(struct {
		Pools   []DiscoveredPool
		Jobs    []QueuedJob
		Supply  []RunnerSupply
		Runners []ObservedRunner
	}{o.Pools, o.Jobs, o.Supply, o.Runners})
	if err != nil || len(raw) > 4<<20 {
		return nil
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != s.digest {
		return nil
	}
	return out
}

func assessRepositoryIdleScope(o DemandObservation, blocked map[string]bool) *repositoryIdleScope {
	for _, j := range o.Jobs {
		if _, ok := blocked[j.RepositoryID]; !ok {
			continue
		}
		// Locked placement, missing eligible edges and disabled-only ambiguity may
		// not justify lowering any configured idle target. No charge is removed.
		if j.Locked || len(j.CandidatePoolIDs) == 0 || len(j.IdleOnlyPoolIDs) != 0 {
			blocked[j.RepositoryID] = true
		}
	}
	return &repositoryIdleScope{source: o.sourceIdentity, digest: o.Digest, start: o.StartedAt, finish: o.FinishedAt, blocked: blocked}
}
