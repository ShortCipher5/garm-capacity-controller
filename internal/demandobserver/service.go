package demandobserver

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Service exposes observation summaries only. It has no ledger or writer handle.
type Service struct {
	mu      sync.Mutex
	config  Config
	running bool
	state   string
	event   *Event
	maxAge  time.Duration
}
type Status struct {
	State                string     `json:"state"`
	ObservedAt           time.Time  `json:"observed_at"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	Source               string     `json:"source"`
	Event                *Event     `json:"event,omitempty"`
	AllocationAuthorized bool       `json:"allocation_authorized"`
}

func NewService(c Config) (*Service, error) {
	if c.validate() != nil {
		return nil, ErrObserver
	}
	// Freeze caller-owned slices before launching a background reader.
	raw, e := json.Marshal(c)
	if e != nil {
		return nil, ErrObserver
	}
	var frozen Config
	if json.Unmarshal(raw, &frozen) != nil {
		return nil, ErrObserver
	}
	age := time.Duration(c.IntervalSeconds) * 2 * time.Second
	if age > 5*time.Minute {
		age = 5 * time.Minute
	}
	return &Service{config: frozen, state: "not_started", maxAge: age}, nil
}
func (s *Service) Snapshot() Status { return s.snapshot(time.Now().UTC()) }
func (s *Service) snapshot(now time.Time) Status {
	out := Status{State: "unconfigured", ObservedAt: now, Source: "scoped-stock-and-github-observation"}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out.State = s.state
	if s.event == nil {
		return out
	}
	e := *s.event
	if e.Counts != nil {
		c := *e.Counts
		e.Counts = &c
	}
	if e.LastSuccessAt != nil {
		t := *e.LastSuccessAt
		e.LastSuccessAt = &t
	}
	out.Event = &e
	if e.Status == "observed" {
		expiry := e.StartedAt.Add(s.maxAge)
		out.ExpiresAt = &expiry
		if !now.Before(expiry) && out.State != "stopped" {
			out.State = "stale"
		}
	}
	if out.State != "observed" {
		e.Counts = nil
	}
	return out
}

// Write accepts only summaries emitted by the existing internal polling loop.
func (s *Service) Write(raw []byte) (int, error) {
	if len(raw) > 16384 {
		return 0, ErrObserver
	}
	var e Event
	if json.Unmarshal(raw, &e) != nil {
		return 0, ErrObserver
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.event = &e
	s.state = "unavailable"
	if e.Status == "observed" {
		s.state = "observed"
	}
	return len(raw), nil
}
func (s *Service) Run(ctx context.Context) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrObserver
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrObserver
	}
	s.running = true
	s.state = "observing"
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.state = "stopped"; s.mu.Unlock() }()
	return Run(ctx, s.config, s, false)
}
