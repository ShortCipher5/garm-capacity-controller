package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"reflect"
	"strings"
	"testing"
)

func TestCapacityStatesReadOnly(t *testing.T) {
	h, store, token := fixture(t)
	ctx := context.Background()
	baseline(t, store, nil)
	explicit := admission.Resources{CPU: 1000, RAM: 1000, Disk: 200, Runners: 1}
	requestResources := admission.Resources{CPU: 500, RAM: 1200, Disk: 300, Runners: 1}
	linkedResources := admission.Resources{CPU: 700, RAM: 800, Disk: 400, Runners: 1}
	key := strings.Repeat("a", 64)
	if _, err := store.Admit(ctx, admission.Request{Key: key, Host: "host-a", Pool: "pool-a", Resources: requestResources}); err != nil {
		t.Fatal(err)
	}
	baseline(t, store, []admission.InventoryResource{
		{ID: "fixture-unlinked-unknown", Host: "host-a", Pool: "pool-a", State: "unknown", Resources: explicit},
		{ID: "fixture-linked-running", Host: "host-a", Pool: "pool-a", ReservationKey: key, State: "running", Resources: linkedResources},
	})
	before, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const path = "/admin/api/v1/capacity-states"
	if w := request(t, h, "GET", path, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(t, h, "POST", path, token); w.Code != 405 {
		t.Fatal(w.Code)
	}
	w := request(t, h, "GET", path, token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var view admission.CapacityStateView
	if e := json.Unmarshal(w.Body.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	if view.Source != "charged-ledger" || !view.GrantsAvailable || view.Total != before.Allocated.Global || len(view.Rows) != 1 || view.Rows[0].State != "unknown" {
		t.Fatalf("%+v", view)
	}
	expectedDisagreement := admission.Resources{CPU: 700, RAM: 1200, Disk: 400, Runners: 1}
	if view.Unknown == nil || view.Unknown.RecordedUnknown != explicit || view.Unknown.StateDisagreement != expectedDisagreement {
		t.Fatal("authenticated view did not classify the two existing unknown charges")
	}
	if strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "fixture-") {
		t.Fatal("capacity attribution exposed a raw identifier")
	}
	after, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read mutated ledger")
	}
}
