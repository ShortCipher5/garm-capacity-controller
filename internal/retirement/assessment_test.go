package retirement

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func fixture() (admission.SQLiteSnapshot, time.Time) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return admission.SQLiteSnapshot{
		Ready: true,
		Config: admission.Config{Hosts: map[string]admission.Host{
			"host-a": {Mode: "draining", Healthy: true, ObservedAt: now},
		}, MaxObservationAge: time.Minute},
		Reservations: map[string]admission.Reservation{},
	}, now
}

func hasBlocker(result Assessment, blocker string) bool {
	for _, value := range result.Blockers {
		if value == blocker {
			return true
		}
	}
	return false
}

func TestMissingInventoryRetainsEveryReservationAndNoPermissions(t *testing.T) {
	snapshot, now := fixture()
	for _, state := range []string{"creating", "idle", "running", "failed", "unknown", "deleting", "future-state"} {
		snapshot.Reservations[state] = admission.Reservation{Request: admission.Request{Key: state, Host: "host-a", Pool: "pool-a"}, State: state}
	}
	before, _ := json.Marshal(snapshot)
	result, err := Assess(snapshot, "host-a", now)
	if err != nil || !result.LedgerNewClaimsPaused || !result.ObservationFresh || result.TrackedAllocations != 7 || result.States != (Counts{Creating: 1, Idle: 1, Running: 1, Deleting: 1, Failed: 1, Unknown: 2}) {
		t.Fatalf("unexpected assessment: %+v %v", result, err)
	}
	for _, blocker := range []string{"tracked_allocations_remain_charged", "job_assignment_fence_unqualified", "confirmed_retirement_unimplemented"} {
		if !hasBlocker(result, blocker) {
			t.Fatalf("missing blocker %s", blocker)
		}
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("assessment mutated snapshot")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "future-state") || strings.Contains(string(encoded), "pool-a") {
		t.Fatal("raw resource metadata exposed")
	}
}

func TestLinkedPhysicalResourceCountedOnceOrRejectsAmbiguousIdentity(t *testing.T) {
	snapshot, now := fixture()
	snapshot.Reservations["key"] = admission.Reservation{Request: admission.Request{Key: "key", Host: "host-a", Pool: "pool-a"}, State: "creating"}
	snapshot.Inventory = []admission.InventoryResource{{ID: "runner-a", Host: "host-a", Pool: "pool-a", ReservationKey: "key", State: "idle"}, {ID: "orphan", Host: "host-a", Pool: "pool-a", State: "unknown"}}
	result, err := Assess(snapshot, "host-a", now)
	if err != nil || result.TrackedAllocations != 2 || result.States != (Counts{Idle: 1, Unknown: 1}) {
		t.Fatalf("double-counted or omitted resource: %+v %v", result, err)
	}
	for _, mutation := range []func(*admission.SQLiteSnapshot){
		func(s *admission.SQLiteSnapshot) { s.Inventory[0].Pool = "wrong" },
		func(s *admission.SQLiteSnapshot) { s.Inventory[0].ReservationKey = "missing" },
		func(s *admission.SQLiteSnapshot) { s.Inventory[1].ReservationKey = "key" },
		func(s *admission.SQLiteSnapshot) { s.Inventory[1].ID = "runner-a" },
		func(s *admission.SQLiteSnapshot) { s.Inventory[0].ID = "" },
		func(s *admission.SQLiteSnapshot) {
			s.Reservations["key"] = admission.Reservation{Request: admission.Request{Key: "key", Host: "wrong", Pool: "pool-a"}}
		},
	} {
		copy := snapshot
		copy.Inventory = append([]admission.InventoryResource{}, snapshot.Inventory...)
		copy.Reservations = map[string]admission.Reservation{"key": snapshot.Reservations["key"]}
		mutation(&copy)
		if result, err := Assess(copy, "host-a", now); err != ErrSnapshot || !reflect.DeepEqual(result, Assessment{}) {
			t.Fatalf("ambiguous identity accepted: %+v %v", result, err)
		}
	}
}

func TestEmptyHostAndFreshnessNeverCertifyJobDrain(t *testing.T) {
	for _, mode := range []string{"enabled", "draining", "disabled"} {
		for _, delta := range []time.Duration{0, time.Minute, time.Minute + 1, -1} {
			snapshot, now := fixture()
			host := snapshot.Config.Hosts["host-a"]
			host.Mode = mode
			snapshot.Config.Hosts["host-a"] = host
			result, err := Assess(snapshot, "host-a", now.Add(delta))
			fresh := delta >= 0 && delta <= time.Minute
			if err != nil || result.ObservationFresh != fresh || result.LedgerNewClaimsPaused != (mode != "enabled" || !fresh) || result.TrackedAllocations != 0 {
				t.Fatalf("mode/freshness: %+v %v", result, err)
			}
			if !hasBlocker(result, "job_assignment_fence_unqualified") || !hasBlocker(result, "confirmed_retirement_unimplemented") {
				t.Fatal("empty ledger was treated as drain completion")
			}
		}
	}
	for _, invalid := range []string{"", "missing"} {
		snapshot, now := fixture()
		if _, err := Assess(snapshot, invalid, now); err != ErrSnapshot {
			t.Fatal("unknown host accepted")
		}
	}
}

func TestInvalidObservationAndClosedReadinessPauseClaims(t *testing.T) {
	for _, mutate := range []func(*admission.SQLiteSnapshot){
		func(s *admission.SQLiteSnapshot) { s.Ready = false },
		func(s *admission.SQLiteSnapshot) { s.Config.MaxObservationAge = 0 },
		func(s *admission.SQLiteSnapshot) {
			host := s.Config.Hosts["host-a"]
			host.Healthy = false
			s.Config.Hosts["host-a"] = host
		},
		func(s *admission.SQLiteSnapshot) {
			host := s.Config.Hosts["host-a"]
			host.ObservedAt = time.Time{}
			s.Config.Hosts["host-a"] = host
		},
	} {
		snapshot, now := fixture()
		host := snapshot.Config.Hosts["host-a"]
		host.Mode = "enabled"
		snapshot.Config.Hosts["host-a"] = host
		mutate(&snapshot)
		result, err := Assess(snapshot, "host-a", now)
		if err != nil || !result.LedgerNewClaimsPaused {
			t.Fatalf("invalid observation/readiness allowed claims: %+v %v", result, err)
		}
	}
}

func TestForeignInventoryCannotClaimTargetHostReservation(t *testing.T) {
	for _, foreignHost := range []string{"host-b", ""} {
		for _, state := range []string{"idle", "running", "unknown"} {
			snapshot, now := fixture()
			snapshot.Reservations["key"] = admission.Reservation{Request: admission.Request{Key: "key", Host: "host-a", Pool: "pool-a"}, State: "creating"}
			snapshot.Inventory = []admission.InventoryResource{{ID: "runner", Host: foreignHost, Pool: "pool-a", ReservationKey: "key", State: state}}
			before, _ := json.Marshal(snapshot)
			result, err := Assess(snapshot, "host-a", now)
			if err != ErrSnapshot || !reflect.DeepEqual(result, Assessment{}) {
				t.Fatalf("foreign identity accepted for %q/%s: %+v %v", foreignHost, state, result, err)
			}
			after, _ := json.Marshal(snapshot)
			if string(before) != string(after) {
				t.Fatal("assessment mutated conflicting snapshot")
			}
		}
	}
}

func TestUnrelatedForeignInventoryDoesNotAffectHostAssessment(t *testing.T) {
	snapshot, now := fixture()
	snapshot.Reservations["other"] = admission.Reservation{Request: admission.Request{Key: "other", Host: "host-b", Pool: "pool-b"}, State: "creating"}
	snapshot.Inventory = []admission.InventoryResource{{ID: "runner", Host: "host-b", Pool: "pool-b", ReservationKey: "other", State: "idle"}}
	result, err := Assess(snapshot, "host-a", now)
	if err != nil || result.TrackedAllocations != 0 || !hasBlocker(result, "job_assignment_fence_unqualified") || !hasBlocker(result, "confirmed_retirement_unimplemented") {
		t.Fatalf("unrelated host changed assessment: %+v %v", result, err)
	}
}

func TestObservationGapsAreDistinctAndRemainCharged(t *testing.T) {
	snapshot, now := fixture()
	snapshot.Reservations["linked"] = admission.Reservation{Request: admission.Request{Key: "linked", Host: "host-a", Pool: "pool-a"}, State: "creating"}
	snapshot.Reservations["missing"] = admission.Reservation{Request: admission.Request{Key: "missing", Host: "host-a", Pool: "pool-a"}, State: "unknown"}
	snapshot.Inventory = []admission.InventoryResource{
		{ID: "linked-resource", Host: "host-a", Pool: "pool-a", ReservationKey: "linked", State: "idle"},
		{ID: "unlinked-resource", Host: "host-a", Pool: "pool-a", State: "running"},
	}
	before, _ := json.Marshal(snapshot)
	result, err := Assess(snapshot, "host-a", now)
	if err != nil || result.TrackedAllocations != 3 || result.UnobservedReservations != 1 || result.UnreservedResources != 1 {
		t.Fatalf("observation gaps not distinguished: %+v %v", result, err)
	}
	for _, blocker := range []string{"reservations_without_inventory_remain_charged", "inventory_without_reservation_requires_reconciliation", "job_assignment_fence_unqualified", "confirmed_retirement_unimplemented"} {
		if !hasBlocker(result, blocker) {
			t.Fatalf("missing blocker %s", blocker)
		}
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("assessment changed charges or identity")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "linked-resource") || strings.Contains(string(encoded), "unlinked-resource") || strings.Contains(string(encoded), "pool-a") {
		t.Fatal("observation gaps exposed raw allocation identities")
	}
}

func TestCompleteObservationHasNoGapButDoesNotPermitRetirement(t *testing.T) {
	snapshot, now := fixture()
	result, err := Assess(snapshot, "host-a", now)
	if err != nil || result.UnobservedReservations != 0 || result.UnreservedResources != 0 || hasBlocker(result, "reservations_without_inventory_remain_charged") || hasBlocker(result, "inventory_without_reservation_requires_reconciliation") {
		t.Fatalf("empty snapshot has invented gaps: %+v %v", result, err)
	}
	if !hasBlocker(result, "job_assignment_fence_unqualified") || !hasBlocker(result, "confirmed_retirement_unimplemented") {
		t.Fatal("zero gaps became retirement authority")
	}
}
