package brokerinput

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"math"
	"testing"
	"time"
)

func TestPressureOnlySuppressesNewAssignments(t *testing.T) {
	out := ReadPlan{RemainingJobs: 2, Pools: []ReadPlanPool{{Pool: "p", Additional: 3, AdditionalWarm: 1, Charged: admission.Resources{Runners: 9}, CoveredByIdle: 1, CoveredByGrant: 2, DeferredByInflight: 3}}}
	plan := capacitybroker.JobPlan{Plan: capacitybroker.Plan{Assignments: []capacitybroker.Assignment{{Pool: "p", Host: "hot", JobID: "j"}, {Pool: "p", Host: "hot", Warm: true}, {Pool: "p", Host: "other", JobID: "k"}}}}
	suppressPressure(plan, &out, map[string]bool{"hot": true})
	row := out.Pools[0]
	if row.Additional != 1 || row.AdditionalWarm != 0 || row.SuppressedByPressure != 2 || out.RemainingJobs != 3 || row.Charged.Runners != 9 || row.CoveredByIdle != 1 || row.CoveredByGrant != 2 || row.DeferredByInflight != 3 {
		t.Fatal(out)
	}
	if len(plan.Assignments) != 3 {
		t.Fatal("mutated planner")
	}
}
func TestPressureThresholdBoundaries(t *testing.T) {
	for _, limit := range []int64{1, 10, 101, math.MaxInt64} {
		threshold := limit - limit/10
		if pressureHigh(admission.UsageSample{CPUUsedMillis: threshold - 1}, admission.Resources{CPU: limit}) {
			t.Fatal("below", limit)
		}
		if !pressureHigh(admission.UsageSample{CPUUsedMillis: threshold}, admission.Resources{CPU: limit}) {
			t.Fatal("at", limit)
		}
	}
	if pressureHigh(admission.UsageSample{CPUUsedMillis: math.MaxInt64}, admission.Resources{}) {
		t.Fatal("unknown budget")
	}
}

func TestAdvisoryLoopReadOnlyCacheOwnershipAndClose(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan struct{})
	s := startAdvisoryLoop(context.Background(), func(ctx context.Context) (ReadPlan, error) {
		close(entered)
		select {
		case <-released:
		case <-ctx.Done():
			return ReadPlan{}, ctx.Err()
		}
		return ReadPlan{ExpiresAt: time.Now().Add(time.Minute), Pools: []ReadPlanPool{{Pool: "p", Additional: 2}}}, nil
	}, time.Hour)
	<-entered
	if _, err := s.ReadPlan(context.Background()); err == nil {
		t.Fatal("inflight fabricated plan")
	}
	close(released)
	deadline := time.Now().Add(time.Second)
	for {
		v, err := s.ReadPlan(context.Background())
		if err == nil {
			v.Pools[0].Additional = 999
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cache absent")
		}
		time.Sleep(time.Millisecond)
	}
	v, err := s.ReadPlan(context.Background())
	if err != nil || v.Pools[0].Additional != 2 {
		t.Fatal("cache caller mutation", v, err)
	}
	s.Close()
	if _, err := s.ReadPlan(context.Background()); err == nil {
		t.Fatal("closed loop retained plan")
	}
}
func TestAdvisoryLoopCancelsInflightAndRejectsExpired(t *testing.T) {
	entered := make(chan struct{})
	s := startAdvisoryLoop(context.Background(), func(ctx context.Context) (ReadPlan, error) {
		close(entered)
		<-ctx.Done()
		return ReadPlan{}, ctx.Err()
	}, time.Hour)
	<-entered
	s.Close()
	if _, err := s.ReadPlan(context.Background()); err == nil {
		t.Fatal("cancelled read accepted")
	}
	s = &AdvisoryLoop{plan: &ReadPlan{ExpiresAt: time.Now().Add(-time.Second)}}
	if _, err := s.ReadPlan(context.Background()); err == nil {
		t.Fatal("expired cache accepted")
	}
}

func TestAdvisoryFailureClearsPreviousCache(t *testing.T) {
	first := make(chan struct{})
	second := make(chan struct{})
	releaseError := make(chan struct{})
	calls := 0
	s := startAdvisoryLoop(context.Background(), func(ctx context.Context) (ReadPlan, error) {
		calls++
		if calls == 1 {
			close(first)
			return ReadPlan{ExpiresAt: time.Now().Add(time.Minute)}, nil
		}
		if calls == 2 {
			close(second)
			select {
			case <-releaseError:
			case <-ctx.Done():
			}
			return ReadPlan{}, capacitybroker.ErrSnapshot
		}
		<-ctx.Done()
		return ReadPlan{}, ctx.Err()
	}, time.Millisecond)
	defer s.Close()
	<-first
	<-second
	if _, err := s.ReadPlan(context.Background()); err != nil {
		t.Fatal("first result not available", err)
	}
	close(releaseError)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := s.ReadPlan(context.Background()); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failure kept prior result")
		}
		time.Sleep(time.Millisecond)
	}
}
