package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"reflect"
	"testing"
)

func TestCapacityStatesReadOnly(t *testing.T) {
	h, store, token := fixture(t)
	baseline(t, store, []admission.InventoryResource{{ID: "one", Host: "host-a", Pool: "pool-a", State: "unknown", Resources: admission.Resources{CPU: 1000, RAM: 1000, Runners: 1}}})
	before, _ := store.ReadSnapshot(context.Background())
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
	if view.Source != "charged-ledger" || view.GrantsAvailable || view.Total != before.Allocated.Global || len(view.Rows) != 1 || view.Rows[0].State != "unknown" {
		t.Fatalf("%+v", view)
	}
	after, _ := store.ReadSnapshot(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read mutated ledger")
	}
}
