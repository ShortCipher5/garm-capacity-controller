package admission

import (
	"testing"
)

func TestCapacityStatesDeduplicatesAndExcludesReleasedHistory(t *testing.T) {
	reservations := map[string]Reservation{"a": {Request: Request{Key: "a", Host: "h", Pool: "p", Resources: Resources{CPU: 2, RAM: 3, Disk: 4, Runners: 1}}, State: "running"}, "b": {Request: Request{Key: "b", Host: "h", Pool: "p", Resources: Resources{CPU: 9, RAM: 9, Disk: 9, Runners: 1}}, State: "unknown"}}
	rows, total, err := capacityStates(reservations, []InventoryResource{{ID: "one", Host: "h", Pool: "p", ReservationKey: "a", State: "running", Resources: Resources{CPU: 3, RAM: 2, Disk: 5, Runners: 1}}}, map[string]bool{"b": true})
	if err != nil || len(rows) != 1 || rows[0].State != "running" || total != (Resources{CPU: 3, RAM: 3, Disk: 5, Runners: 1}) {
		t.Fatalf("%+v %+v %v", rows, total, err)
	}
	rows, total, err = capacityStates(reservations, []InventoryResource{{ID: "two", Host: "h", Pool: "p", ReservationKey: "b", State: "idle", Resources: Resources{CPU: 1, RAM: 1, Disk: 1, Runners: 1}}}, map[string]bool{"a": true, "b": true})
	if err != nil || len(rows) != 1 || rows[0].State != "orphan" || total.Runners != 1 {
		t.Fatalf("%+v %+v %v", rows, total, err)
	}
}
func TestCapacityStatesRejectsDanglingBinding(t *testing.T) {
	if _, _, e := capacityStates(nil, []InventoryResource{{ReservationKey: "missing"}}, nil); e == nil {
		t.Fatal("accepted missing reservation")
	}
}

func TestCapacityStatesConflictRemainsUnknown(t *testing.T) {
	for _, state := range []string{"unknown", "failed", "deleting", "creating"} {
		reservations := map[string]Reservation{"a": {Request: Request{Key: "a", Host: "h", Pool: "p", Resources: Resources{CPU: 1, RAM: 1, Disk: 1, Runners: 1}}, State: state}}
		rows, total, err := capacityStates(reservations, []InventoryResource{{ID: "one", Host: "h", Pool: "p", ReservationKey: "a", State: "running", Resources: Resources{CPU: 1, RAM: 1, Disk: 1, Runners: 1}}}, nil)
		if err != nil || len(rows) != 1 || rows[0].State != "unknown" || total.Runners != 1 {
			t.Fatalf("state %s: %+v %+v %v", state, rows, total, err)
		}
	}
}
