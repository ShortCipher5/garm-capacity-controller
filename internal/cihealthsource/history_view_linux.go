package cihealthsource

import (
	"context"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const maxObservedHistoryRows = 64

// WorkflowHistoryProof is an opaque, short-lived view prerequisite minted
// from a positive exact attempt in the opted-in background reader's cycle.
// A standalone timing record, origin hash, or disk row cannot mint it.
type WorkflowHistoryProof struct {
	key          cihealth.AttemptKey
	owner        cycleSourceInstance
	observedAt   time.Time
	sealedAt     time.Time
	deadline     time.Time // retains the monotonic clock from cycle seal
	wallDeadline time.Time
}

// HistoryProof selects one exact currently observed attempt, without fetching
// or publishing anything. Observed and partial cycles both need the actual
// positive attempt; absence never establishes authorization for a workflow.
func (c Cycle) HistoryProof(key cihealth.AttemptKey) (WorkflowHistoryProof, bool) {
	if (c.state != CycleObserved && c.state != CyclePartial) || c.source.observer == nil || !c.source.observer.LifetimeActive() ||
		c.sealedAt.IsZero() || c.observedAt.IsZero() || c.observedAt.After(c.sealedAt.UTC()) || len(c.facts) > maxCycleAttempts {
		return WorkflowHistoryProof{}, false
	}
	for _, fact := range c.facts {
		if fact.Key != key || fact.AsOf.IsZero() || fact.AsOf.After(c.observedAt) {
			continue
		}
		// The attempt clock is stamped by the exact positive run/attempt GET.
		// Neither later job pages nor another attempt can renew its lease.
		at := fact.Clock.ObservedAt.UTC()
		if at.IsZero() || at.After(fact.AsOf) {
			return WorkflowHistoryProof{}, false
		}
		until := at.Add(historyCycleFreshness)
		remaining := until.Sub(c.sealedAt.UTC())
		if remaining <= 0 || remaining > historyCycleFreshness {
			return WorkflowHistoryProof{}, false
		}
		p := WorkflowHistoryProof{key, c.source, at, c.sealedAt, c.sealedAt.Add(remaining), until}
		if p.current(time.Now()) {
			return p, true
		}
		return WorkflowHistoryProof{}, false
	}
	return WorkflowHistoryProof{}, false
}

func (p WorkflowHistoryProof) current(now time.Time) bool {
	return p.owner.observer != nil && p.owner.observer.LifetimeActive() && !p.observedAt.IsZero() && !p.sealedAt.IsZero() &&
		!now.Before(p.sealedAt) && !now.UTC().Before(p.sealedAt.UTC()) && !now.UTC().Before(p.observedAt) &&
		now.Before(p.deadline) && now.UTC().Before(p.wallDeadline)
}

type ObservedWorkflowHistory struct {
	Scope  cihealth.HistoryScope
	Cursor cihealth.HistoryCursor
	// AuthorizationObservedAt/ExpiresAt bound the live proof only. Attempts
	// retain their individual original observation timestamps and never become
	// fresh telemetry merely because this history read succeeded.
	AuthorizationObservedAt time.Time
	AuthorizationExpiresAt  time.Time
	Attempts                []cihealth.AttemptFacts
}

// ReadObservedWorkflowHistory performs only a local held-store read at the
// caller's explicit unchanged revision. The owner must withdraw any projected
// view on stop/expiry and must not retain it as live telemetry. No caller,
// route or public projection is wired here.
func (s *OwnedCycleHistory) ReadObservedWorkflowHistory(ctx context.Context, proof WorkflowHistoryProof, expected cihealth.HistoryCursor, limit int) (ObservedWorkflowHistory, error) {
	var none ObservedWorkflowHistory
	if s == nil || s.cell == nil || ctx == nil || ctx.Err() != nil || s.cell.closing.Load() || expected.Revision < 0 || expected.Checkpoint < 0 ||
		(expected.Revision == 0 && expected.Checkpoint != 0) || limit < 1 || limit > maxObservedHistoryRows || !s.cell.mu.TryLock() {
		return none, ErrCycleHistory
	}
	defer s.cell.mu.Unlock()
	k := proof.key
	scope := cihealth.HistoryScope{Source: k.Source, RepositoryID: k.RepositoryID, WorkflowID: k.WorkflowID}
	h := s.cell.history
	if proof.owner != h.owner || !h.allows(scope) || s.cell.uncertain || s.cell.closing.Load() || ctx.Err() != nil || !proof.current(time.Now()) {
		return none, ErrCycleHistory
	}
	ctx, cancel := context.WithDeadline(ctx, proof.deadline)
	defer cancel()
	if current, err := h.Cursor(ctx, scope); err != nil || current != expected {
		return none, ErrCycleHistory
	}
	if s.cell.closing.Load() || ctx.Err() != nil || !proof.current(time.Now()) {
		return none, ErrCycleHistory
	}
	rows, err := h.Recent(ctx, scope, limit)
	if err != nil {
		return none, ErrCycleHistory
	}
	if current, err := h.Cursor(ctx, scope); err != nil || current != expected || s.cell.closing.Load() || ctx.Err() != nil || !proof.current(time.Now()) {
		return none, ErrCycleHistory
	}
	return ObservedWorkflowHistory{scope, expected, proof.observedAt, proof.wallDeadline, rows}, nil
}
