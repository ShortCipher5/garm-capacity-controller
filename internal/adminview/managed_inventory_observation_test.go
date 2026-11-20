package adminview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagedInventoryObservationIsOptionalAuthenticatedAndIndependent(t *testing.T) {
	h, store, token := fixture(t)
	h.provision = false
	const path = "/admin/api/v1/overview"
	base := request(t, h, "GET", path, token)
	if base.Code != 200 || strings.Contains(base.Body.String(), "managed_inventory_observation") {
		t.Fatal(base.Code, base.Body.String())
	}
	before, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.SetManagedInventoryObservation(func(now time.Time) ManagedInventoryObservation {
		calls++
		count := 0
		return ManagedInventoryObservation{Available: true, SampledAt: &now, Count: &count}
	})
	if request(t, h, "GET", path, "").Code != 401 || request(t, h, "POST", path, token).Code != 405 || calls != 0 {
		t.Fatal("unauthorized observer read")
	}
	var view, original Overview
	w := request(t, h, "GET", path, token)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || json.Unmarshal(base.Body.Bytes(), &original) != nil ||
		calls != 1 || view.ManagedInventoryObservation == nil || !view.ManagedInventoryObservation.Available ||
		view.ManagedInventoryObservation.Count == nil || *view.ManagedInventoryObservation.Count != 0 {
		t.Fatal("authenticated observer missing")
	}
	if view.Ready || view.Provisioning || view.ManagementEnabled || view.GarmManagementAvailable ||
		view.Ready != original.Ready || view.Provisioning != original.Provisioning {
		t.Fatal("observer changed independent gates")
	}
	after, err := store.Snapshot(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("observer changed ledger", err)
	}
}
