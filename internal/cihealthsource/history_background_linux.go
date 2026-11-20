package cihealthsource

import (
	"context"
	"slices"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
)

// BackgroundCycleHistory is a one-way, persistence-only transfer of an
// already-open owned history into the existing CI timing worker. It cannot
// open files, mint source observations, or expose persisted rows or proofs.
type BackgroundCycleHistory struct{ history *OwnedCycleHistory }

// BindBackground transfers a dormant owner only to the exact reviewed reader
// and original scoped cursor receipt. The previous handle remains closable,
// but it cannot persist directly or open a history view after transfer.
func (s *OwnedCycleHistory) BindBackground(source *demandobserver.CIHealthSource, reviewed []HistoryExpectation) (*BackgroundCycleHistory, error) {
	if s == nil || s.cell == nil || !source.LifetimeActive() || len(reviewed) == 0 || len(reviewed) > maxHistoryCycleScopes ||
		s.cell.closing.Load() || !s.cell.mu.TryLock() {
		return nil, ErrCycleHistory
	}
	defer s.cell.mu.Unlock()
	cell := s.cell
	if cell.closing.Load() || cell.uncertain || cell.background || !source.LifetimeActive() || captureCycleSource(source) != cell.history.owner || !slices.Equal(reviewed, cell.startup) {
		return nil, ErrCycleHistory
	}
	cell.background = true
	return &BackgroundCycleHistory{history: s}, nil
}

// PersistCycle performs at most one attempt from a non-partial positive
// collection. Missing approved workflow groups never advance. This does not
// mean the source collection covered complete GitHub history.
func (s *BackgroundCycleHistory) PersistCycle(ctx context.Context, cycle Cycle) (HistoryCycleReceipt, error) {
	if s == nil || s.history == nil || s.history.cell == nil || ctx == nil || ctx.Err() != nil {
		return HistoryCycleReceipt{}, ErrCycleHistory
	}
	cell := s.history.cell
	if cell.closing.Load() || !cell.mu.TryLock() {
		return HistoryCycleReceipt{}, ErrCycleHistory
	}
	defer cell.mu.Unlock()
	if !cell.background || cell.closing.Load() || ctx.Err() != nil || cell.lifetime.Err() != nil {
		return HistoryCycleReceipt{}, ErrCycleHistory
	}
	if cycle.source != cell.history.owner || !cycle.source.observer.LifetimeActive() || (cycle.State() != CycleObserved && cycle.State() != CyclePartial) {
		_ = cell.closeLocked()
		return HistoryCycleReceipt{}, ErrCycleHistory
	}
	if cycle.State() == CyclePartial {
		if !cycle.source.observer.LifetimeActive() || ctx.Err() != nil || cell.closing.Load() {
			_ = cell.closeLocked()
			return HistoryCycleReceipt{}, ErrCycleHistory
		}
		return HistoryCycleReceipt{CycleState: CyclePartial}, nil
	}
	if !cycle.ObservedAt().After(cell.lastBackgroundObservation) {
		_ = cell.closeLocked()
		return HistoryCycleReceipt{}, ErrCycleHistory
	}
	// Use the exact reader's own context as the ancestor all the way down
	// to the held-root store. A reader Close must not wait for a forwarding
	// goroutine to stop checks or a subsequent workflow write.
	call, release, err := cycle.source.observer.BindLocalLifetime(ctx)
	if err != nil {
		_ = cell.closeLocked()
		return HistoryCycleReceipt{}, ErrCycleHistory
	}
	defer release()
	call, cancel := context.WithCancel(call)
	defer cancel()
	disconnect := context.AfterFunc(cell.lifetime, cancel)
	defer disconnect()
	guard := &backgroundHistoryCall{caller: ctx, cell: cell, source: cycle.source.observer}
	receipt, err := cell.history.persist(call, cycle, cell.startup, guard)
	if err != nil {
		_ = cell.closeLocked()
		return receipt, err // Retain the actual prefix; never retry an ambiguous group.
	}
	if !guard.active(call) {
		_ = cell.closeLocked()
		return receipt, ErrCycleHistory
	}
	cell.lastBackgroundObservation = cycle.ObservedAt()
	for _, commit := range receipt.Commits {
		for i := range cell.startup {
			if cell.startup[i].Scope == commit.Scope {
				cell.startup[i].Cursor = commit.Cursor
				break
			}
		}
	}
	if !guard.active(call) {
		_ = cell.closeLocked()
		return receipt, ErrCycleHistory
	}
	return receipt, nil
}

func (s *BackgroundCycleHistory) Close() error {
	if s == nil {
		return nil
	}
	return s.history.Close()
}
