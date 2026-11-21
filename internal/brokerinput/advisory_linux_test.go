package brokerinput

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"math"
	"path/filepath"
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

func TestAdvisoryPressureGuestStateV2VetoOnly(t *testing.T) {
	const missing = "missing_or_stale_not_headroom"
	const partial = "partial_sample_not_headroom"
	const high = "high_partial_pressure_suppressed"
	const v2 = "guest-state-v2"
	const legacy = "guest-state"
	for _, tc := range []struct {
		name, source, status string
		host                 string
		age                  time.Duration
		cpu, ram, disk       int64
		legacyPartial, veto  bool
	}{
		{name: "v2 CPU veto", source: v2, status: high, cpu: 3600, veto: true},
		{name: "v2 RAM veto", source: v2, status: high, ram: 3600, veto: true},
		{name: "v2 disk cannot veto", source: v2, status: missing, cpu: 3599, ram: 3599, disk: 5000},
		{name: "v2 low cannot refresh legacy expiry", source: v2, status: partial, disk: 5000, legacyPartial: true},
		{name: "v2 stale", source: v2, status: missing, cpu: 5000, age: pressureAge + time.Second},
		{name: "v2 future", source: v2, status: missing, ram: 5000, age: -time.Second},
		{name: "v2 other host", source: v2, status: missing, cpu: 5000, host: "other"},
		{name: "legacy disk still vetoes", source: legacy, status: high, disk: 3600, veto: true},
		{name: "unknown source ignored", source: "guest-state-v3", status: missing, cpu: 5000, ram: 5000, disk: 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state, _, _ := ledgerFixture()
			store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), state.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC)
			legacyAt := now.Add(-time.Minute)
			if tc.legacyPartial {
				if err := store.RecordUsageSample(ctx, "host", legacyAt, 0, 0, 0, legacy); err != nil {
					t.Fatal(err)
				}
			}
			host := tc.host
			if host == "" {
				host = "host"
			}
			at := now.Add(-tc.age)
			if err := store.RecordUsageSample(ctx, host, at, tc.cpu, tc.ram, tc.disk, tc.source); err != nil {
				t.Fatal(err)
			}
			statuses, blocked, err := (&planReader{store: store}).readPressure(ctx, state, now)
			want := PressureStatus{Host: "host", State: tc.status}
			if tc.veto {
				want.SampledAt = at
			} else if tc.legacyPartial {
				want.SampledAt = legacyAt
			}
			if err != nil || len(statuses) != 1 || statuses[0] != want || blocked["host"] != tc.veto {
				t.Fatalf("status=%+v blocked=%v err=%v; want %+v blocked=%t", statuses, blocked, err, want, tc.veto)
			}
			snapshot := capacitybroker.Snapshot{Hosts: map[string]capacitybroker.Host{"host": {}}}
			var wantExpiry time.Time
			if tc.legacyPartial {
				wantExpiry = legacyAt.Add(pressureAge)
			}
			if expiry := stockMaximumPressureExpiry(snapshot, statuses, now); !expiry.Equal(wantExpiry) {
				t.Fatalf("preview pressure expiry %v; want %v", expiry, wantExpiry)
			}
		})
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
