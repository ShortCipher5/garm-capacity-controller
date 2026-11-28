package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"strings"
	"testing"
	"time"
)

func TestManagementServerGroupSaveReadConflictAndFreshness(t *testing.T) {
	h, s, read := fixture(t)
	write := strings.Repeat("b", 64)
	if err := h.EnableManagement(write); err != nil {
		t.Fatal(err)
	}
	const path = "/admin/api/v1/broker-policy"
	const doc = `{"revision":0,"repositories":[],"groups":[{"id":"33333333-3333-4333-8333-333333333333","name":"Builders","hosts":["host-a"],"repositories":[],"pools":[],"limit":{"cpu_millis":2000,"ram_mib":8192,"runners":2}}]}`
	if w := post(t, h, "PUT", path, read, doc); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := post(t, h, "PUT", path, write, doc); w.Code != 422 {
		t.Fatalf("must fail without reconciliation: %d %s", w.Code, w.Body.String())
	}
	f, err := s.BeginReconciliation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = s.CompleteReconciliation(t.Context(), f, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host-a", ObservedAt: time.Now(), Healthy: true, Bounded: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if w := post(t, h, "PUT", path, write, doc); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := post(t, h, "PUT", path, write, doc); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := request(t, h, "GET", path, read)
	var got struct {
		Policy  admission.BrokerPolicy
		Charges map[string]admission.Resources `json:"group_charges"`
		Hosts   []string                       `json:"eligible_hosts"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Policy.Revision != 1 || len(got.Policy.Groups) != 1 || len(got.Hosts) != 1 || got.Hosts[0] != "host-a" {
		t.Fatalf("invalid projection %d %s", w.Code, w.Body.String())
	}
	if v, ok := got.Charges[got.Policy.Groups[0].ID]; !ok || v != (admission.Resources{}) {
		t.Fatal("missing group charge", got.Charges)
	}
}
