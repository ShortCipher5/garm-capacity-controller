package admission

import (
	"testing"
)

func TestCapacityStatesDeduplicatesAndExcludesReleasedHistory(t *testing.T) {
	reservations := map[string]Reservation{"a": {Request: Request{Key: "a", Host: "h", Pool: "p", Resources: Resources{CPU: 2, RAM: 3, Disk: 4, Runners: 1}}, State: "running"}, "b": {Request: Request{Key: "b", Host: "h", Pool: "p", Resources: Resources{CPU: 9, RAM: 9, Disk: 9, Runners: 1}}, State: "unknown"}}
	rows, total, split, err := capacityStates(reservations, []InventoryResource{{ID: "one", Host: "h", Pool: "p", ReservationKey: "a", State: "running", Resources: Resources{CPU: 3, RAM: 2, Disk: 5, Runners: 1}}}, map[string]bool{"b": true})
	if err != nil || split != nil || len(rows) != 1 || rows[0].State != "running" || total != (Resources{CPU: 3, RAM: 3, Disk: 5, Runners: 1}) {
		t.Fatalf("%+v %+v %v", rows, total, err)
	}
	rows, total, split, err = capacityStates(reservations, []InventoryResource{{ID: "two", Host: "h", Pool: "p", ReservationKey: "b", State: "idle", Resources: Resources{CPU: 1, RAM: 1, Disk: 1, Runners: 1}}}, map[string]bool{"a": true, "b": true})
	if err != nil || split != nil || len(rows) != 1 || rows[0].State != "orphan" || total.Runners != 1 {
		t.Fatalf("%+v %+v %v", rows, total, err)
	}
}
func TestCapacityStatesRejectsDanglingBinding(t *testing.T) {
	if _, _, _, e := capacityStates(nil, []InventoryResource{{ReservationKey: "missing"}}, nil); e == nil {
		t.Fatal("accepted missing reservation")
	}
}

func TestCapacityStatesConflictRemainsUnknown(t *testing.T) {
	for _, state := range []string{"unknown", "failed", "deleting", "creating"} {
		reservations := map[string]Reservation{"a": {Request: Request{Key: "a", Host: "h", Pool: "p", Resources: Resources{CPU: 1, RAM: 1, Disk: 1, Runners: 1}}, State: state}}
		rows, total, split, err := capacityStates(reservations, []InventoryResource{{ID: "one", Host: "h", Pool: "p", ReservationKey: "a", State: "running", Resources: Resources{CPU: 1, RAM: 1, Disk: 1, Runners: 1}}}, nil)
		if err != nil || len(rows) != 1 || rows[0].State != "unknown" || total.Runners != 1 ||
			split == nil || split.RecordedUnknown != (Resources{}) || split.StateDisagreement != total {
			t.Fatalf("state %s: %+v %+v %v", state, rows, total, err)
		}
	}
}

func TestCapacityStatesUnknownBreakdownConservesEveryDimension(t *testing.T) {
	newReservation := func(key, state string, resources Resources) Reservation {
		return Reservation{Request: Request{Key: key, Host: "h", Pool: "p", Resources: resources}, State: state}
	}
	reservations := map[string]Reservation{
		"a": newReservation("a", "unknown", Resources{CPU: 2, RAM: 3, Disk: 4, Runners: 1}),
		"b": newReservation("b", "unknown", Resources{CPU: 5, RAM: 6, Disk: 7, Runners: 1}),
		"c": newReservation("c", "unknown", Resources{CPU: 8, RAM: 9, Disk: 10, Runners: 1}),
	}
	inventory := []InventoryResource{
		{ID: "one", Host: "h", Pool: "p", ReservationKey: "a", State: "running", Resources: Resources{CPU: 3, RAM: 2, Disk: 5, Runners: 1}},
		{ID: "two", Host: "h", Pool: "p", ReservationKey: "b", State: "unknown", Resources: Resources{CPU: 4, RAM: 7, Disk: 8, Runners: 1}},
		{ID: "three", Host: "h", Pool: "p", State: "unknown", Resources: Resources{CPU: 9, RAM: 11, Disk: 13, Runners: 1}},
		{ID: "four", Host: "h", Pool: "p", ReservationKey: "c", State: "unknown", Resources: Resources{CPU: 1, RAM: 1, Disk: 1, Runners: 1}},
	}
	rows, total, split, err := capacityStates(reservations, inventory, map[string]bool{"c": true})
	want := CapacityUnknownBreakdown{
		RecordedUnknown:   Resources{CPU: 14, RAM: 18, Disk: 21, Runners: 2},
		StateDisagreement: Resources{CPU: 3, RAM: 3, Disk: 5, Runners: 1},
	}
	if err != nil || split == nil || *split != want || total != (Resources{CPU: 18, RAM: 22, Disk: 27, Runners: 4}) ||
		len(rows) != 2 || rows[0].State != "orphan" || rows[1] != (CapacityStateRow{State: "unknown", Resources: Resources{CPU: 17, RAM: 21, Disk: 26, Runners: 3}}) {
		t.Fatalf("incorrect charged attribution: rows=%+v total=%+v split=%+v err=%v", rows, total, split, err)
	}
}
