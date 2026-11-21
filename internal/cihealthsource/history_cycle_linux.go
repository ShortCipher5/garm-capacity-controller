package cihealthsource

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
)

const (
	maxHistoryCycleScopes = 64
	historyCycleFreshness = 2 * time.Minute
)

var ErrCycleHistory = errors.New("CI cycle history unavailable")

// CycleHistory narrows an already-owned local store to the approved numeric
// workflows of one configured CI source. It does not open, close, schedule or
// fetch anything. The configured caller owns the source, store and allowlist.
type CycleHistory struct {
	history *cihealth.History
	source  string
	owner   cycleSourceInstance
	scopes  map[cihealth.HistoryScope]bool
}

func NewCycleHistory(history *cihealth.History, source Source, approved []cihealth.HistoryScope) (*CycleHistory, error) {
	if history == nil {
		return nil, ErrCycleHistory
	}
	h, err := bindCycleHistoryScope(source, approved)
	if err != nil {
		return nil, err
	}
	h.history = history
	return h, nil
}

func bindCycleHistoryScope(source Source, approved []cihealth.HistoryScope) (*CycleHistory, error) {
	owner := captureCycleSource(source)
	if owner == (cycleSourceInstance{}) || source.SourceID() == "" || len(approved) == 0 || len(approved) > maxHistoryCycleScopes {
		return nil, ErrCycleHistory
	}
	repos := make(map[int64]bool)
	for _, r := range source.Repositories() {
		repos[r.GitHubID] = true
	}
	h := &CycleHistory{source: source.SourceID(), owner: owner, scopes: make(map[cihealth.HistoryScope]bool, len(approved))}
	for _, s := range approved {
		if s.Source != h.source || !repos[s.RepositoryID] || s.WorkflowID <= 0 || h.scopes[s] {
			return nil, ErrCycleHistory
		}
		h.scopes[s] = true
	}
	return h, nil
}

func (h *CycleHistory) allows(scope cihealth.HistoryScope) bool { return h != nil && h.scopes[scope] }

func (h *CycleHistory) Cursor(ctx context.Context, scope cihealth.HistoryScope) (cihealth.HistoryCursor, error) {
	if !h.allows(scope) {
		return cihealth.HistoryCursor{}, ErrCycleHistory
	}
	return h.history.Cursor(ctx, scope)
}

func (h *CycleHistory) Recent(ctx context.Context, scope cihealth.HistoryScope, limit int) ([]cihealth.AttemptFacts, error) {
	if !h.allows(scope) {
		return nil, ErrCycleHistory
	}
	return h.history.Recent(ctx, scope, limit, time.Now())
}

func (h *CycleHistory) recentPrefix(ctx context.Context, scope cihealth.HistoryScope, limit int) (cihealth.HistoryRecentPrefix, error) {
	if !h.allows(scope) {
		return cihealth.HistoryRecentPrefix{}, ErrCycleHistory
	}
	return h.history.RecentPrefix(ctx, scope, limit, time.Now())
}

type HistoryExpectation struct {
	Scope  cihealth.HistoryScope
	Cursor cihealth.HistoryCursor
}

type HistoryCycleCommit struct {
	Scope  cihealth.HistoryScope
	Cursor cihealth.HistoryCursor
}

type HistoryCycleReceipt struct {
	// The collection's scope/window was non-atomic. Observed is not complete
	// history, and Partial still has usable individually observed facts.
	CycleState CycleState
	// Commits contains only the successful prefix (including exact replays
	// acknowledged by History). Failed is the next scope attempted/refused;
	// its durable outcome may be unknown. Never replay automatically.
	Commits []HistoryCycleCommit
	Failed  *cihealth.HistoryScope
}

// Persist appends only approved positive facts, with a per-workflow CAS.
// There is no transaction across scopes. Empty or absent approved groups
// leave those cursors untouched; scans do not establish an upstream checkpoint.
func (h *CycleHistory) Persist(ctx context.Context, cycle Cycle, expected []HistoryExpectation) (HistoryCycleReceipt, error) {
	return h.persist(ctx, cycle, expected, nil)
}

// backgroundHistoryCall is a synchronous owner/caller fence in addition to the
// reader-parented context that reaches the actual store's commit checks.
type backgroundHistoryCall struct {
	caller context.Context
	cell   *ownedCycleHistoryCell
	source *demandobserver.CIHealthSource
}

func (g *backgroundHistoryCall) active(ctx context.Context) bool {
	return (g == nil || (g.caller.Err() == nil && !g.cell.closing.Load() && g.cell.lifetime.Err() == nil && g.source.LifetimeActive())) && ctx.Err() == nil
}

func (h *CycleHistory) persist(ctx context.Context, cycle Cycle, expected []HistoryExpectation, background *backgroundHistoryCall) (HistoryCycleReceipt, error) {
	var none HistoryCycleReceipt
	started := time.Now()
	now := started.UTC()
	if h == nil || h.owner == (cycleSourceInstance{}) || cycle.source != h.owner || ctx == nil || !background.active(ctx) ||
		(cycle.state != CycleObserved && cycle.state != CyclePartial) ||
		!freshHistoryCycleFact(cycle.observedAt, now) || len(cycle.facts) > maxCycleAttempts || len(expected) > maxHistoryCycleScopes {
		return none, ErrCycleHistory
	}
	cursors := make(map[cihealth.HistoryScope]cihealth.HistoryCursor, len(expected))
	for _, e := range expected {
		c := e.Cursor
		if _, exists := cursors[e.Scope]; !h.allows(e.Scope) || exists || c.Revision < 0 || c.Revision == math.MaxInt64 || c.Checkpoint < 0 || (c.Revision == 0 && c.Checkpoint != 0) {
			return none, ErrCycleHistory
		}
		cursors[e.Scope] = c
	}
	groups := make(map[cihealth.HistoryScope][]cihealth.AttemptFacts)
	seen := make(map[cihealth.AttemptKey]bool)
	cutoff := cycle.observedAt.Add(historyCycleFreshness)
	var jobs, steps int
	for _, fact := range cycle.facts {
		k := fact.Key
		if k.Source != h.source {
			return none, ErrCycleHistory
		}
		s := cihealth.HistoryScope{Source: k.Source, RepositoryID: k.RepositoryID, WorkflowID: k.WorkflowID}
		if !h.allows(s) {
			continue // No storage access, identifier or count for unapproved workflows.
		}
		if _, hasExpected := cursors[s]; !hasExpected || seen[k] || !freshHistoryCycleFact(fact.AsOf, now) || fact.AsOf.After(cycle.observedAt) || len(fact.Jobs) > maxCycleJobs-jobs {
			return none, ErrCycleHistory
		}
		jobs += len(fact.Jobs)
		for _, job := range fact.Jobs {
			if len(job.Steps) > maxCycleSteps-steps {
				return none, ErrCycleHistory
			}
			steps += len(job.Steps)
		}
		if _, err := cihealth.Measure(fact); err != nil {
			return none, ErrCycleHistory
		}
		seen[k] = true
		groups[s] = append(groups[s], cloneFacts(fact))
		if until := fact.AsOf.Add(historyCycleFreshness); until.Before(cutoff) {
			cutoff = until
		}
	}
	if !background.active(ctx) {
		return none, ErrCycleHistory
	}
	latest := time.Now()
	if !historyCycleTimeCurrent(started, started, cycle.observedAt, cutoff, latest) {
		return none, ErrCycleHistory
	}
	out := HistoryCycleReceipt{CycleState: cycle.state}
	order := make([]cihealth.HistoryScope, 0, len(groups))
	for s := range groups {
		order = append(order, s)
	}
	sortHistoryScopes(order)
	ctx, cancel := context.WithDeadline(ctx, cutoff)
	defer cancel()
	for _, s := range order {
		if !background.active(ctx) {
			out.Failed = &s
			return out, ErrCycleHistory
		}
		now := time.Now()
		if !historyCycleTimeCurrent(started, latest, cycle.observedAt, cutoff, now) {
			out.Failed = &s
			return out, ErrCycleHistory
		}
		latest = now
		cursor := cursors[s]
		next := cihealth.HistoryCursor{Revision: cursor.Revision + 1, Checkpoint: cursor.Checkpoint}
		if err := h.history.Apply(ctx, s, cursor, next, groups[s], now); err != nil {
			out.Failed = &s
			return out, err
		}
		out.Commits = append(out.Commits, HistoryCycleCommit{Scope: s, Cursor: next})
	}
	// A commit which entered the atomic publication region before cancellation
	// can still finish. Report those proven durable changes, not cycle success.
	if background != nil && (!background.active(ctx) || !historyCycleTimeCurrent(started, latest, cycle.observedAt, cutoff, time.Now())) {
		return out, ErrCycleHistory
	}
	return out, nil
}

func sortHistoryScopes(scopes []cihealth.HistoryScope) {
	sort.Slice(scopes, func(i, j int) bool {
		a, b := scopes[i], scopes[j]
		return a.RepositoryID < b.RepositoryID || a.RepositoryID == b.RepositoryID && a.WorkflowID < b.WorkflowID
	})
}

func freshHistoryCycleFact(at, now time.Time) bool {
	at = at.UTC()
	return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999 && !at.After(now) && now.Before(at.Add(historyCycleFreshness))
}

func historyCycleTimeCurrent(started, previous, observedAt, cutoff, now time.Time) bool {
	wall := now.UTC()
	return !now.Before(started) && !wall.Before(started.UTC()) && !wall.Before(previous.UTC()) &&
		!wall.Before(observedAt.UTC()) && wall.Before(cutoff.UTC())
}
