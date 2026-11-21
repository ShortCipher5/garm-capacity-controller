package cihealthsource

import (
	"context"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

type historyTrendVersion struct{ at, sealedAt time.Time }

// ObservedWorkflowTrend carries only aggregates and exact numeric scope.
// It contains no retained rows, artifact data, or read credentials.
type ObservedWorkflowTrend struct {
	Scope   cihealth.TrendScope
	Trends  cihealth.RetainedTrends
	Limited cihealth.HistorySelectionLimit
}

// WorkflowTrendsSnapshot is bound to this exact source, the earliest selected
// positive attempt proof, the storage owner and the committed revisions.
// Current does no I/O and is safe to call at owner HTTP read time.
type WorkflowTrendsSnapshot struct {
	cell      *ownedCycleHistoryCell
	caller    context.Context
	version   *historyTrendVersion
	proofs    []WorkflowHistoryProof
	expiresAt time.Time
	workflows []ObservedWorkflowTrend
}

func (l WorkflowTrendsSnapshot) Current() bool {
	if l.cell == nil || l.version == nil || l.caller == nil || l.caller.Err() != nil || l.cell.closing.Load() ||
		l.cell.trendsVersion.Load() != l.version || len(l.proofs) == 0 {
		return false
	}
	now := time.Now()
	for _, p := range l.proofs {
		if !p.current(now) {
			return false
		}
	}
	return l.caller.Err() == nil && !l.cell.closing.Load() && l.cell.trendsVersion.Load() == l.version
}

// Summary exposes only independent copies of the bounded aggregate while the
// exact lease remains current. Zero or withdrawn snapshots expose nothing.
func (l WorkflowTrendsSnapshot) Summary() (time.Time, time.Time, []ObservedWorkflowTrend, bool) {
	if !l.Current() {
		return time.Time{}, time.Time{}, nil, false
	}
	out := append([]ObservedWorkflowTrend(nil), l.workflows...)
	for i := range out {
		tr := &out[i].Trends
		for _, b := range []*cihealth.RetainedTrendBucket{&tr.Previous.Initial, &tr.Previous.Retries, &tr.Current.Initial, &tr.Current.Retries} {
			for _, m := range []*cihealth.RetainedTrendMetric{&b.Attempts, &b.Jobs, &b.Steps} {
				if m.Duration != nil {
					d := *m.Duration
					m.Duration = &d
				}
			}
		}
	}
	if !l.Current() {
		return time.Time{}, time.Time{}, nil, false
	}
	return l.version.at, l.expiresAt, out, true
}

// SummarizeObservedTrends reads only the exact owned revision already checked
// into this background cycle. No raw row crosses the source boundary. A new
// observation, failed read or closed owner cannot re-authorize old aggregates.
func (s *BackgroundCycleHistory) SummarizeObservedTrends(ctx context.Context, cycle Cycle) (WorkflowTrendsSnapshot, error) {
	fail := func() (WorkflowTrendsSnapshot, error) {
		return WorkflowTrendsSnapshot{}, ErrCycleHistory
	}
	if s == nil || !s.trends || s.history == nil || s.history.cell == nil ||
		ctx == nil || ctx.Err() != nil || cycle.State() != CycleObserved {
		return fail()
	}
	cell := s.history.cell
	if cell.closing.Load() || !cell.mu.TryLock() {
		return fail()
	}
	defer cell.mu.Unlock()
	v := cell.trendsVersion.Load()
	if cell.closing.Load() || cell.uncertain || cell.lifetime.Err() != nil ||
		v == nil || v.at != cycle.ObservedAt() || v.sealedAt != cycle.sealedAt || cycle.source != cell.history.owner {
		return fail()
	}
	best := make(map[cihealth.HistoryScope]cihealth.AttemptFacts)
	for _, fact := range cycle.facts {
		k := fact.Key
		scope := cihealth.HistoryScope{Source: k.Source, RepositoryID: k.RepositoryID, WorkflowID: k.WorkflowID}
		if cell.history.allows(scope) && fact.Clock.ObservedAt.After(best[scope].Clock.ObservedAt) {
			best[scope] = fact
		}
	}
	lease := WorkflowTrendsSnapshot{cell: cell, caller: ctx, version: v}
	for _, expected := range cell.startup {
		fact, ok := best[expected.Scope]
		if !ok || expected.Cursor.Revision <= 0 {
			continue
		}
		p, ok := cycle.HistoryProof(fact.Key)
		if !ok {
			return fail()
		}
		scope := cihealth.TrendScope{Source: expected.Scope.Source, RepositoryID: expected.Scope.RepositoryID, WorkflowID: expected.Scope.WorkflowID}
		got, err := summarizeObservedWorkflow(ctx, cell, p, expected, scope, v.at)
		if err != nil {
			return fail()
		}
		lease.workflows = append(lease.workflows, got)
		lease.proofs = append(lease.proofs, p)
		if lease.expiresAt.IsZero() || p.wallDeadline.Before(lease.expiresAt) {
			lease.expiresAt = p.wallDeadline
		}
	}
	if len(lease.workflows) == 0 {
		return WorkflowTrendsSnapshot{}, nil
	}
	if ctx.Err() != nil || !lease.Current() {
		return fail()
	}
	return lease, nil
}

func summarizeObservedWorkflow(ctx context.Context, cell *ownedCycleHistoryCell, p WorkflowHistoryProof,
	expected HistoryExpectation, scope cihealth.TrendScope, at time.Time) (ObservedWorkflowTrend, error) {
	fail := func() (ObservedWorkflowTrend, error) { return ObservedWorkflowTrend{}, ErrCycleHistory }
	ctx, cancel := context.WithDeadline(ctx, p.deadline)
	defer cancel()
	current := func() bool {
		return ctx.Err() == nil && !cell.closing.Load() && p.current(time.Now())
	}
	if cursor, err := cell.history.Cursor(ctx, expected.Scope); err != nil || cursor != expected.Cursor || !current() {
		return fail()
	}
	page, err := cell.history.recentPrefix(ctx, expected.Scope, cihealth.MaxRetainedTrendAttempts)
	if err != nil || !current() {
		return fail()
	}
	trends, err := cihealth.SummarizeRetainedTrends(scope, at, page.Attempts)
	if err != nil {
		return fail()
	}
	if cursor, err := cell.history.Cursor(ctx, expected.Scope); err != nil || cursor != expected.Cursor || !current() {
		return fail()
	}
	return ObservedWorkflowTrend{Scope: scope, Trends: trends, Limited: page.Limited}, nil
}
