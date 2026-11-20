//go:build linux

package nativecontroller

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// RetirementService consumes canonical positive job/root receipts. The receipt
// merely selects a candidate: Coordinator.Retire independently obtains a fresh
// owner observation before the immutable intent, then uses one-use claims.
// The observer's read-only authorization is not silently expanded.
type RetirementConfiguration struct {
	Store    *admission.SQLiteStore
	Owners   []Configuration
	Interval time.Duration
}

const retirementCycleTimeout = 2 * time.Minute

type RetirementCycle struct {
	Examined, AwaitingEvidence, OutsideScope, OperationsCompleted, Unresolved int
	RetainedDiagnosticsAvailable, RetainedDiagnosticsUnavailable              int
	ReleaseAuthorized                                                         bool
}
type RetirementStatus struct {
	At         time.Time
	Result     RetirementCycle
	Incomplete bool
}
type RetirementService struct {
	store    *admission.SQLiteStore
	owners   map[string]*Coordinator
	interval time.Duration
	gate     chan struct{}
	after    string
	statusMu sync.RWMutex
	status   RetirementStatus
}

func NewRetirementService(ctx context.Context, c RetirementConfiguration) (*RetirementService, error) {
	if ctx == nil || ctx.Err() != nil || c.Store == nil {
		return nil, ErrConfiguration
	}
	if c.Store.RuntimeLineageBound() {
		return nil, ErrUnqualified
	}
	if len(c.Owners) == 0 || len(c.Owners) > 100 || c.Interval < time.Second || c.Interval > time.Hour {
		return nil, ErrConfiguration
	}
	s := &RetirementService{store: c.Store, owners: map[string]*Coordinator{}, interval: c.Interval, gate: make(chan struct{}, 1)}
	for _, cfg := range c.Owners {
		if cfg.Store != c.Store || s.owners[cfg.Policy.DomainID()] != nil {
			return nil, ErrConfiguration
		}
		owner, e := New(ctx, cfg)
		if e != nil {
			return nil, e
		}
		s.owners[owner.binding.Spec.DomainID] = owner
	}
	return s, nil
}
func (s *RetirementService) Latest() RetirementStatus {
	if s == nil {
		return RetirementStatus{Incomplete: true}
	}
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

// LatestWithFreshness is a process-local diagnostic only. The window accounts
// for both the configured delay after a cycle and the next cycle's deadline.
// It never says whether the worker is running or supplies release evidence.
func (s *RetirementService) LatestWithFreshness() (RetirementStatus, string) {
	status := s.Latest()
	if s == nil {
		return status, "no_cycle"
	}
	return status, retirementFreshness(status.At, time.Now().UTC(), s.interval)
}

func retirementFreshness(at, now time.Time, interval time.Duration) string {
	if at.IsZero() {
		return "no_cycle"
	}
	if now.IsZero() || at.After(now) {
		return "clock_invalid"
	}
	if !now.Before(at.Add(interval + retirementCycleTimeout)) {
		return "stale"
	}
	return "within_cycle_window"
}

// Cycle's cursor is fairness, not authority. Restart begins from the first row;
// all mutation/recovery decisions remain in permanent canonical claims/receipts.
func (s *RetirementService) Cycle(ctx context.Context) (result RetirementCycle, err error) {
	if s == nil || s.store == nil || ctx == nil || ctx.Err() != nil {
		return result, ErrConfiguration
	}
	if s.store.RuntimeLineageBound() {
		return result, ErrUnqualified
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	defer func() {
		s.statusMu.Lock()
		s.status = RetirementStatus{time.Now().UTC(), result, err != nil || result.Unresolved > 0}
		s.statusMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, retirementCycleTimeout)
	defer cancel()
	rows, e := s.store.NativeRunnerBindings(ctx, s.after, 64)
	if e != nil {
		return result, e
	}
	if len(rows) == 0 {
		s.after = ""
		return result, nil
	}
	for _, b := range rows {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		s.after = b.PermitID
		result.Examined++
		owner := s.owners[b.DomainID]
		if owner == nil || owner.policy.Digest() != b.PolicyDigest {
			result.OutsideScope++
			continue
		}
		if _, e = s.store.LookupNativeRootFence(ctx, b.PermitID); errors.Is(e, sql.ErrNoRows) {
			result.AwaitingEvidence++
			continue
		} else if e != nil {
			result.Unresolved++
			continue
		}
		outcome, e := owner.Retire(ctx, b.PermitID)
		if e != nil || outcome.ReleaseAuthorized || outcome.StopState != "succeeded" || outcome.DeleteState != "succeeded" {
			result.Unresolved++
			continue
		}
		result.OperationsCompleted++
		// Diagnostics are separate from operation completion and never authorize release.
		if observation, diagnosticErr := owner.ObserveRetirementDiagnostics(ctx, b.PermitID); diagnosticErr == nil && observation.Valid() {
			result.RetainedDiagnosticsAvailable++
		} else {
			result.RetainedDiagnosticsUnavailable++
		}
	}
	return result, nil
}
func (s *RetirementService) Run(ctx context.Context) error {
	if s == nil || s.store == nil || ctx == nil {
		return ErrConfiguration
	}
	if s.store.RuntimeLineageBound() {
		return ErrUnqualified
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			_, _ = s.Cycle(ctx)
			timer.Reset(s.interval)
		}
	}
}
