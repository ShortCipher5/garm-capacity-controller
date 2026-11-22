package cihealthsource

import (
	"context"
	"slices"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

// An internal locator is allowed to survive one ordinary four-minute poll wait
// plus the bounded next cycle. It never extends a timing view or history lease.
const activeContinuationLifetime = 6 * time.Minute

type continuationScope struct {
	garmID   string
	githubID int64
}

// ActiveContinuation owns one input to the next cycle. No facts, names, URLs
// or auth material are retained. A copied handle still shares consumption.
type ActiveContinuation struct{ cell *activeContinuationCell }

type activeContinuationCell struct {
	used       atomic.Bool
	source     cycleSourceInstance
	sealedAt   time.Time
	scopes     []continuationScope
	candidates map[int64]runCandidate
}

// ActiveContinuation provides an opaque, one-use prior-active locator. The
// runtime should retain it only after the cycle is successfully published.
func (c Cycle) ActiveContinuation() *ActiveContinuation {
	if c.continuation == nil {
		return nil
	}
	return &ActiveContinuation{cell: c.continuation}
}

// ReadCycleContinuing preserves the ordinary scan. It may use spare capacity
// for one exact attempt previously seen active per repository. The hint is not
// a source proof, and the hard per-cycle and per-repository bounds do not grow.
func ReadCycleContinuing(ctx context.Context, source Source, enrolledGarmRepos []string, from, to time.Time, hint *ActiveContinuation) (Cycle, error) {
	return readCycle(ctx, source, enrolledGarmRepos, from, to, hint)
}

func continuationScopes(repos []demandrepair.CIRepository) []continuationScope {
	out := make([]continuationScope, 0, len(repos))
	for _, repo := range repos {
		out = append(out, continuationScope{repo.GarmID, repo.GitHubID})
	}
	return out
}

func continuationReaderLive(source Source) bool {
	if owner, ok := source.(interface{ LifetimeActive() bool }); ok {
		return owner.LifetimeActive()
	}
	return true // The supported low-level repair reader has no separate lifetime handle.
}

func mintActiveContinuation(c Cycle, repos []demandrepair.CIRepository, normalFacts int) *activeContinuationCell {
	if c.state != CycleObserved || c.source == (cycleSourceInstance{}) || c.sealedAt.IsZero() || normalFacts < 1 {
		return nil
	}
	candidates := make(map[int64]runCandidate)
	running := make(map[int64]bool)
	for _, fact := range c.facts[:normalFacts] {
		if fact.Clock.Phase != cihealth.Running && fact.Clock.Phase != cihealth.Queued {
			continue
		}
		key := fact.Key
		_, found := candidates[key.RepositoryID]
		isRunning := fact.Clock.Phase == cihealth.Running
		if !found || (isRunning && !running[key.RepositoryID]) {
			candidates[key.RepositoryID] = runCandidate{key.RunID, key.Attempt, key.WorkflowID}
			running[key.RepositoryID] = isRunning
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return &activeContinuationCell{source: c.source, sealedAt: c.sealedAt, scopes: continuationScopes(repos), candidates: candidates}
}

func (h *ActiveContinuation) consume(source Source, repos []demandrepair.CIRepository) *activeContinuationCell {
	if h == nil || h.cell == nil || h.cell.used.Swap(true) {
		return nil
	}
	cell := h.cell
	if !cell.current(source) || !slices.Equal(cell.scopes, continuationScopes(repos)) {
		return nil
	}
	return cell
}

func (h *activeContinuationCell) current(source Source) bool {
	if h == nil || h.source == (cycleSourceInstance{}) || h.source != captureCycleSource(source) || !continuationReaderLive(source) {
		return false
	}
	age := time.Since(h.sealedAt)
	return age >= 0 && age < activeContinuationLifetime
}

func (h *activeContinuationCell) candidate(scan repoScan) (runCandidate, bool, error) {
	want, ok := h.candidates[scan.repo.GitHubID]
	if !ok {
		return runCandidate{}, false, nil
	}
	for _, list := range [][]runCandidate{scan.active, scan.recent} {
		for _, got := range list {
			if got.runID == want.runID {
				if got.workflow != want.workflow {
					return runCandidate{}, false, ErrCycleObservation
				}
				if got.attempt == want.attempt {
					return runCandidate{}, false, nil // Exactly reobserved by ordinary work.
				}
			}
		}
	}
	return want, true, nil
}
