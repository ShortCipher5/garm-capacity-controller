package demandobserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestStatusExpiryFailureCopyAndStop(t *testing.T) {
	now := time.Now().UTC()
	s := &Service{state: "observing", maxAge: 2 * time.Minute}
	if s.snapshot(now).Event != nil {
		t.Fatal("invented event")
	}
	e := Event{Mode: "observation_only", Status: "observed", StartedAt: now, FinishedAt: now, Counts: &Counts{StockQueued: 2, ReaderOnly: 1, ObservedDemand: 3}}
	b, _ := json.Marshal(e)
	if _, err := s.Write(b); err != nil {
		t.Fatal(err)
	}
	got := s.snapshot(now)
	if got.State != "observed" || got.AllocationAuthorized || got.Event.Counts.ObservedDemand != 3 {
		t.Fatal(got)
	}
	got.Event.Counts.ObservedDemand = 999
	if s.snapshot(now).Event.Counts.ObservedDemand != 3 {
		t.Fatal("mutable snapshot")
	}
	stale := s.snapshot(now.Add(2 * time.Minute))
	if stale.State != "stale" || stale.Event.Counts != nil {
		t.Fatal(stale)
	}
	s.state = "stopped"
	if s.snapshot(now).Event.Counts != nil {
		t.Fatal("stopped counts")
	}
	e.Status = "garm_unavailable"
	e.Counts = nil
	b, _ = json.Marshal(e)
	s.Write(b)
	if s.snapshot(now).State != "unavailable" || s.snapshot(now).Event.Counts != nil {
		t.Fatal("failed poll reused counts")
	}
}
func TestServiceRejectsInvalidAndConcurrent(t *testing.T) {
	if _, e := NewService(Config{}); e == nil {
		t.Fatal("invalid config")
	}
	s := &Service{running: true}
	if s.Run(context.Background()) == nil {
		t.Fatal("duplicate loop")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.Run(ctx) == nil {
		t.Fatal("cancelled start")
	}
	var absent *Service
	if absent.Snapshot().State != "unconfigured" {
		t.Fatal("missing source")
	}
}
func TestServiceRunsActualPollAndCancels(t *testing.T) {
	c, _, _ := fixture(t)
	s, e := NewService(c)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.After(5 * time.Second)
	for {
		v := s.Snapshot()
		if v.Event != nil {
			if v.State != "observed" || v.Event.Counts == nil || v.Event.Atomic || v.Event.MissedWebhookCoverage {
				t.Fatal(v)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("no completed poll")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join")
	}
	got := s.Snapshot()
	if got.State != "stopped" || got.Event.Counts != nil {
		t.Fatal(got)
	}
}
