package adminview

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func TestReuseStatusReadOnly(t *testing.T) {
	h, store, token := fixture(t)
	req := admission.Request{Key: "reserved-one", Pool: "pool-a", Host: "host-a", Resources: admission.Resources{CPU: 1000, RAM: 1000, Disk: 4096, Runners: 1}}
	baseline(t, store, nil)
	if _, err := store.Admit(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordState(context.Background(), req.Key, "deleting"); err != nil {
		t.Fatal(err)
	}
	baseline(t, store, []admission.InventoryResource{{ID: "one", Host: "host-a", Pool: "pool-a", State: "deleting", ReservationKey: req.Key, Resources: req.Resources}})
	before, _ := store.ReadSnapshot(context.Background())
	const path = "/admin/api/v1/reuse-status"
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
	var view admission.ReuseStatusView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Rows) != 1 || view.Rows[0].Disposition != admission.ReuseStatusPendingCleanup || len(view.Rows[0].Reasons) != 1 || view.Rows[0].Remaining != req.Resources {
		t.Fatalf("%+v", view)
	}
	after, _ := store.ReadSnapshot(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read mutated ledger")
	}
}
