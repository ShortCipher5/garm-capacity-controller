package brokerinput

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

var ErrRuntimeAuthority = errors.New("allocator runtime authority unavailable")
var ErrServiceRunning = errors.New("allocator service already running")

// LoopStatus reports this process's actual activity, not persisted configuration
// or proof of physical execution. No raw errors, observations or secrets escape.
type LoopStatus struct {
	State           string    `json:"state"`
	ObservedAt      time.Time `json:"observed_at"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	LastAttemptAt   time.Time `json:"last_attempt_at,omitempty"`
	LastCompletedAt time.Time `json:"last_completed_at,omitempty"`
	LastSuccessAt   time.Time `json:"last_success_at,omitempty"`
	StoppedAt       time.Time `json:"stopped_at,omitempty"`
	InFlight        bool      `json:"in_flight"`
	AttemptCount    uint64    `json:"attempt_count"`
	SuccessCount    uint64    `json:"success_count"`
	LastBatchID     string    `json:"last_batch_id,omitempty"`
	LastGrantCount  int       `json:"last_grant_count"`
	ReasonCode      string    `json:"reason_code,omitempty"`
}

// Service owns one sequential grant-only loop. Even successful cycles do not
// imply a pool-settings mutation or native creation. Runtime ledgers remain
// blocked until a real native/management activation capability exists; neither
// enrollment records nor caller configuration can override that boundary.
type Service struct {
	reader       *planReader
	mu           sync.Mutex
	status       LoopStatus
	running      bool
	runtimeBound bool
	interval     time.Duration
	cycle        func(context.Context) (GrantCycleResult, error)
}

func NewService(store *admission.SQLiteStore, source *garmapi.DemandClient, opt capacitybroker.Options, ttl, interval time.Duration) (*Service, error) {
	if store == nil || ttl < time.Minute || ttl > time.Hour || interval < time.Minute || interval > time.Hour {
		return nil, capacitybroker.ErrSnapshot
	}
	runtimeBound := store.RuntimeLineageBound()
	if !runtimeBound && source == nil {
		return nil, capacitybroker.ErrSnapshot
	}
	return &Service{reader: &planReader{store: store, source: source, opt: opt}, status: LoopStatus{State: "not_started"}, runtimeBound: runtimeBound, interval: interval, cycle: func(ctx context.Context) (GrantCycleResult, error) { return GrantCycle(ctx, store, source, opt, ttl) }}, nil
}
func (s *Service) Snapshot() LoopStatus {
	if s == nil {
		return LoopStatus{State: "unconfigured", ObservedAt: time.Now().UTC()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.status
	out.ObservedAt = time.Now().UTC()
	return out
}
func (s *Service) Run(ctx context.Context) error {
	if s == nil || ctx == nil {
		return capacitybroker.ErrSnapshot
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrServiceRunning
	}
	if ctx.Err() != nil {
		s.mu.Unlock()
		return ctx.Err()
	}
	s.running = true
	s.status.StartedAt = time.Now().UTC()
	s.status.StoppedAt = time.Time{}
	s.status.State = "starting"
	s.status.ReasonCode = ""
	if s.runtimeBound {
		s.status.State = "blocked"
		s.status.ReasonCode = "native_management_authority_unavailable"
		s.status.StoppedAt = time.Now().UTC()
		s.running = false
		s.mu.Unlock()
		return ErrRuntimeAuthority
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.status.State = "stopped"
		s.status.InFlight = false
		s.status.StoppedAt = time.Now().UTC()
		s.mu.Unlock()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		s.status.State = "running"
		s.status.InFlight = true
		s.status.LastAttemptAt = time.Now().UTC()
		s.status.AttemptCount++
		// An old successful result must not look like this attempt's result.
		s.status.LastBatchID = ""
		s.status.LastGrantCount = 0
		s.status.ReasonCode = ""
		s.mu.Unlock()
		child, cancel := context.WithTimeout(ctx, 90*time.Second)
		result, err := s.cycle(child)
		cancel()
		s.mu.Lock()
		now := time.Now().UTC()
		s.status.InFlight = false
		s.status.LastCompletedAt = now
		if err != nil || ctx.Err() != nil {
			s.status.State = "failed"
			s.status.ReasonCode = "cycle_failed"
			if ctx.Err() != nil {
				s.status.ReasonCode = "cancelled"
			}
		} else {
			s.status.State = "waiting"
			s.status.SuccessCount++
			s.status.LastSuccessAt = now
			s.status.LastBatchID = result.BatchID
			s.status.LastGrantCount = len(result.Grants)
		}
		s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		timer := time.NewTimer(s.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
