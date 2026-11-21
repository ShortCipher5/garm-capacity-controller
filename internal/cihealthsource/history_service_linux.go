package cihealthsource

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

type OwnedCycleHistoryInput struct {
	Enabled bool
	// Root is already held in the dedicated service user's exclusive custody.
	// This adapter cannot acquire or authorize a directory or change its mode.
	Root *os.Root
	// Initialize is an explicit request to initialize the absent fixed store.
	// It does not adopt, repair or overwrite an existing unresolved store.
	Initialize bool
	Workflows  []cihealth.HistoryScope
}

// OwnedCycleHistory is a dormant, opt-in persistence stage for the exact
// configured reader. It owns only the store's duplicate of the held root; it
// does not run a worker or fetch data. Historical rows require an exact
// fresh source proof at every guarded read.
type OwnedCycleHistory struct{ cell *ownedCycleHistoryCell }

type ownedCycleHistoryCell struct {
	mu                        sync.Mutex
	closing                   atomic.Bool
	closed                    bool
	closeErr                  error
	uncertain                 bool
	history                   *CycleHistory
	startup                   []HistoryExpectation
	background                bool
	lastBackgroundObservation time.Time
	trendsVersion             atomic.Pointer[historyTrendVersion]
	lifetime                  context.Context
	cancel                    context.CancelFunc
}

// OpenOwnedCycleHistory validates the full scope before opening any history
// slot and reads initial per-workflow CAS metadata only. The caller retains
// its Root and reader. Disabled is a no-op even if other fields are invalid.
func OpenOwnedCycleHistory(ctx context.Context, source Source, in OwnedCycleHistoryInput) (*OwnedCycleHistory, []HistoryExpectation, error) {
	if !in.Enabled {
		return nil, nil, nil
	}
	if ctx == nil || ctx.Err() != nil || in.Root == nil {
		return nil, nil, ErrCycleHistory
	}
	adapter, err := bindCycleHistoryScope(source, in.Workflows)
	if err != nil {
		return nil, nil, err
	}
	store, err := cihealth.OpenHistory(ctx, cihealth.HistoryInput{Enabled: true, Root: in.Root, Initialize: in.Initialize})
	if err != nil {
		return nil, nil, err
	}
	adapter.history = store
	success := false
	defer func() {
		if !success {
			_ = store.Close()
		}
	}()
	order := make([]cihealth.HistoryScope, 0, len(adapter.scopes))
	for scope := range adapter.scopes {
		order = append(order, scope)
	}
	sortHistoryScopes(order)
	expected := make([]HistoryExpectation, 0, len(order))
	for _, scope := range order {
		cursor, err := adapter.Cursor(ctx, scope)
		if err != nil {
			return nil, nil, err
		}
		expected = append(expected, HistoryExpectation{Scope: scope, Cursor: cursor})
	}
	if ctx.Err() != nil {
		return nil, nil, ErrCycleHistory
	}
	success = true
	lifetime, cancel := context.WithCancel(context.Background())
	return &OwnedCycleHistory{cell: &ownedCycleHistoryCell{history: adapter, startup: append([]HistoryExpectation(nil), expected...), lifetime: lifetime, cancel: cancel}}, expected, nil
}

// PersistObservedCycle is one explicit attempt with caller-held expectations.
// There is no retry, silent cursor refresh, automatic reset or runtime source
// authorization based on stored facts. A second concurrent call is refused;
// an unknown durable outcome remains fenced until separately reviewed.
func (s *OwnedCycleHistory) PersistObservedCycle(ctx context.Context, cycle Cycle, expected []HistoryExpectation) (HistoryCycleReceipt, error) {
	if s == nil || s.cell == nil || ctx == nil || ctx.Err() != nil || s.cell.closing.Load() || !s.cell.mu.TryLock() {
		return HistoryCycleReceipt{}, ErrCycleHistory
	}
	defer s.cell.mu.Unlock()
	if s.cell.closing.Load() || s.cell.background || ctx.Err() != nil {
		return HistoryCycleReceipt{}, ErrCycleHistory
	}
	s.cell.startup = nil // An independent attempt consumes the startup cursor review.
	if s.cell.uncertain {
		return HistoryCycleReceipt{}, cihealth.ErrHistoryUncertain
	}
	receipt, err := s.cell.history.Persist(ctx, cycle, expected)
	if errors.Is(err, cihealth.ErrHistoryUncertain) {
		s.cell.uncertain = true
	}
	return receipt, err
}

// Close refuses new persistence immediately and joins any call already in
// progress before releasing the store. It never cleans up unresolved history.
func (s *OwnedCycleHistory) Close() error {
	if s == nil || s.cell == nil {
		return nil
	}
	s.cell.closing.Store(true)
	s.cell.cancel()
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return s.cell.closeLocked()
}

func (s *ownedCycleHistoryCell) closeLocked() error {
	if !s.closed {
		s.closing.Store(true)
		s.cancel()
		s.closeErr = s.history.history.Close()
		s.closed = true
	}
	return s.closeErr
}
