package admission

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPlanningChargesPreserveCanonicalUnknownOrphansAndGrants(t *testing.T) {
	s := BrokerGrantState{
		Config: Config{Hosts: map[string]Host{"h": {}}, Pools: map[string]Pool{"p": {}}},
		Reservations: map[string]Reservation{
			"a": {Request: Request{Key: "a", Host: "h", Pool: "p", Resources: Resources{10, 20, 30, 1}}, State: "unknown"},
			"b": {Request: Request{Key: "b", Host: "h", Pool: "p", Resources: Resources{50, 50, 50, 1}}, State: "deleted"},
		},
		Released: map[string]bool{"b": true},
		Inventory: []InventoryResource{
			{ID: "linked", Host: "h", Pool: "p", ReservationKey: "a", Resources: Resources{20, 10, 40, 1}},
			{ID: "returned", Host: "h", Pool: "p", ReservationKey: "b", Resources: Resources{5, 5, 5, 1}},
			{ID: "orphan", Host: "h", Pool: "p", Resources: Resources{3, 3, 3, 1}},
		},
		Grants: []BrokerGrant{
			{ID: "unused", State: "unused", Spec: BrokerGrantSpec{Host: "h", Authority: StockProviderAuthority{PoolID: "p"}, Resources: Resources{2, 2, 2, 1}, ExpiresAt: time.Unix(1, 0)}},
			{ID: "consumed", State: "consumed"},
			{ID: "revoked", State: "revoked"},
		},
	}
	got, err := brokerPlanningCharges(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []BrokerPlanningCharge{
		{"grant:unused", "h", "p", Resources{2, 2, 2, 1}},
		{"inventory:h:orphan", "h", "p", Resources{3, 3, 3, 1}},
		{"inventory:h:returned", "h", "p", Resources{5, 5, 5, 1}},
		{"reservation:a", "h", "p", Resources{20, 20, 40, 1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	// Conflicting links fail closed, never silently merge two physical resources.
	s.Inventory = append(s.Inventory, s.Inventory[0])
	if _, err := brokerPlanningCharges(s); err == nil {
		t.Fatal("duplicate physical link accepted")
	}
}

func TestReadBrokerPlanningStateTracksRealGrantConsumption(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	issueStockTestGrant(t, s, "planning")
	state, charges, err := s.ReadBrokerPlanningState(ctx)
	if err != nil || !state.Ready || len(charges) != 1 || !strings.HasPrefix(charges[0].ID, "grant:") {
		t.Fatalf("grant read %+v %+v %v", state, charges, err)
	}
	before := state.Sequence
	_, err = s.AdmitStockProviderPlacement(ctx, placementRequest("planning-actual"), stockAuthority(), stockDigest())
	if err != nil {
		t.Fatal(err)
	}
	state, charges, err = s.ReadBrokerPlanningState(ctx)
	if err != nil || state.Sequence <= before || len(charges) != 1 || !strings.HasPrefix(charges[0].ID, "reservation:") {
		t.Fatalf("consumed double charge %+v %+v %v", state, charges, err)
	}
}
