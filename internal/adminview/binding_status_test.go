package adminview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func TestBackendBindingStatusIsExplicitMinimalAndLegacyConservative(t *testing.T) {
	_, store, _ := fixture(t)
	state, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.Ready = true // A bare trusted fixture can be marked ready without a binding.
	view, err := overview(state, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if view.Hosts[0].BackendBindingStatus != "unbound" || !view.Hosts[0].AdmissionPaused {
		t.Fatal("absence invented a binding")
	}
	state.HostBindings = map[string]admission.HostBinding{"host-a": {Host: "host-a", ServerKeyFingerprint: strings.Repeat("d", 64), OriginFingerprint: strings.Repeat("e", 64), ScopeFingerprint: strings.Repeat("f", 64), ServerName: "private-fixture-daemon"}}
	view, err = overview(state, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if view.Hosts[0].BackendBindingStatus != "bound_observation" {
		t.Fatal("binding presence not classified")
	}
	encoded, _ := json.Marshal(view)
	for _, private := range []string{strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64), "private-fixture-daemon"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("raw binding detail exposed")
		}
	}
	state.LegacyUnboundHosts = []string{"host-a"}
	view, err = overview(state, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if view.Hosts[0].BackendBindingStatus != "legacy_scope_import_required" || !view.Hosts[0].AdmissionPaused {
		t.Fatal("legacy exclusion lost to binding presence")
	}
	// The marker is historical, not computed from current charge count. Clearing a
	// fixture's in-memory resource lists must not make the UI advertise auto-bind.
	state.Reservations = map[string]admission.Reservation{}
	state.Inventory = nil
	view, err = overview(state, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if view.Hosts[0].BackendBindingStatus != "legacy_scope_import_required" || !view.Hosts[0].AdmissionPaused {
		t.Fatal("zero charges falsely cleared historical exclusion")
	}
	after, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Ready || len(after.HostBindings) != 0 || len(after.LegacyUnboundHosts) != 0 {
		t.Fatal("status calculation mutated stored readiness or binding history")
	}
}
