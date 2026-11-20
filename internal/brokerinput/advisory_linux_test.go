package brokerinput

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"math"
	"reflect"
	"testing"
	"time"
)

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
	}, time.Hour, 12*time.Second)
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
	}, time.Hour, 12*time.Second)
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
	}, time.Millisecond, 12*time.Second)
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

func TestAdvisoryPressureFitsAlternateWithoutChangingOccupancy(t *testing.T) {
	snapshot, policy, demand, opt := fixture()
	snapshot.Hosts["other"] = snapshot.Hosts["host"]
	p := snapshot.Pools[pool]
	p.Hosts = append(p.Hosts, "other")
	snapshot.Pools[pool] = p
	baseline, err := Stock(snapshot, policy, demand, opt)
	if err != nil || len(baseline.Assignments) != 1 || baseline.Assignments[0].Host != "host" {
		t.Fatalf("baseline %+v %v", baseline, err)
	}
	plan, err := advisoryStock(snapshot, policy, demand, opt, map[string]bool{"host": true})
	if err != nil || len(plan.Assignments) != 1 || plan.Assignments[0].Host != "other" || len(plan.RemainingJobs) != 0 {
		t.Fatalf("avoidable backlog %+v %v", plan, err)
	}
	if !snapshot.Hosts["host"].AcceptNew {
		t.Fatal("mutated source host")
	}
	// Full unknown occupancy on the vetoed host still exhausts global capacity.
	snapshot.Charges = []capacitybroker.Charge{{ID: "unknown", Host: "host", Pool: pool, Resources: snapshot.Global}}
	want := append([]capacitybroker.Charge(nil), snapshot.Charges...)
	plan, err = advisoryStock(snapshot, policy, demand, opt, map[string]bool{"host": true})
	if err != nil || len(plan.Assignments) != 0 || len(plan.RemainingJobs) != 1 || !reflect.DeepEqual(want, snapshot.Charges) {
		t.Fatalf("unknown charge discarded %+v %v", plan, err)
	}
}
func TestAdvisoryPressureRetainsHotHostExistingIdleAndGrants(t *testing.T) {
	for _, kind := range []string{"idle", "grant", "inflight"} {
		t.Run(kind, func(t *testing.T) {
			s, p, d, o := fixture()
			s.Charges = []capacitybroker.Charge{{ID: "existing", Host: "host", Pool: pool, Resources: s.Pools[pool].Size}}
			if kind == "idle" {
				d.Supply[0].Idle = 1
			} else {
				v := s.Pools[pool]
				if kind == "grant" {
					v.OutstandingGrants = 1
				} else {
					v.Inflight = 1
				}
				v.PendingWarm = 1
				s.Pools[pool] = v
			}
			before, err := Stock(s, p, d, o)
			if err != nil {
				t.Fatal(err)
			}
			after, err := advisoryStock(s, p, d, o, map[string]bool{"host": true})
			if err != nil || len(after.Assignments) != 0 || !reflect.DeepEqual(before.CoveredByIdle, after.CoveredByIdle) || !reflect.DeepEqual(before.CoveredByGrant, after.CoveredByGrant) || !reflect.DeepEqual(before.DeferredByInflight, after.DeferredByInflight) {
				t.Fatalf("coverage changed %+v %+v %v", before, after, err)
			}
		})
	}
}
func TestAdvisoryPressureCannotEnableOrOverfillAlternative(t *testing.T) {
	for _, kind := range []string{"disabled", "cpu", "ram", "disk", "not-pool-member"} {
		t.Run(kind, func(t *testing.T) {
			s, p, d, o := fixture()
			h := s.Hosts["host"]
			switch kind {
			case "disabled":
				h.AcceptNew = false
			case "cpu":
				h.Budget[0] = 999
			case "ram":
				h.Budget[1] = 999
			case "disk":
				h.Budget[2] = 999
			}
			s.Hosts["other"] = h
			if kind != "not-pool-member" {
				v := s.Pools[pool]
				v.Hosts = append(v.Hosts, "other")
				s.Pools[pool] = v
			}
			plan, err := advisoryStock(s, p, d, o, map[string]bool{"host": true})
			if err != nil || len(plan.Assignments) != 0 || len(plan.RemainingJobs) != 1 {
				t.Fatalf("invalid alternative %+v %v", plan, err)
			}
		})
	}
}

// Fake reader ignores its own deadline and returns advice with a future
// expiry. The real cache must still treat the observation as unavailable.
func TestAdvisoryLoopRejectsLateSuccessfulPoll(t *testing.T) {
	next := make(chan struct{})
	calls := 0
	s := startAdvisoryLoop(context.Background(), func(ctx context.Context) (ReadPlan, error) {
		calls++
		if calls == 2 {
			close(next)
		}
		<-ctx.Done()
		return ReadPlan{ExpiresAt: time.Now().Add(time.Minute)}, nil
	}, time.Millisecond, time.Millisecond)
	defer s.Close()
	select {
	case <-next:
	case <-time.After(time.Second):
		t.Fatal("next poll did not start")
	}
	plan, err := s.ReadPlan(context.Background())
	if err == nil || PlanFailureReason(err) != "observation_unavailable" || !plan.ExpiresAt.IsZero() {
		t.Fatalf("late source result was not discarded: %+v %v", plan, err)
	}
}
