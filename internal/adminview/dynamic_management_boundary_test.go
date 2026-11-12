package adminview

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func TestNoImplicitDynamicEnrollmentPlacementOrLegacyImport(t *testing.T) {
	h, store, read := fixture(t)
	write := strings.Repeat("c", 64)
	if err := h.EnableManagement(write); err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/admin/api/v1/hosts/register", "/admin/api/v1/pool-placement", "/admin/api/v1/legacy-scope-import", "/admin/api/v1/backend-bind", "/admin/api/v1/backend-reset"} {
		for _, token := range []string{read, write} {
			if response := request(t, h, "POST", route, token); response.Code != 404 {
				t.Fatalf("unimplemented control exposed: %s %d", route, response.Code)
			}
		}
	}
	for _, body := range []map[string]any{
		{"revision": admission.PolicyRevision(before.Config), "action": "pool_hosts", "target": "pool-a", "hosts": []string{"host-a"}, "reason": "not implemented"},
		{"revision": admission.PolicyRevision(before.Config), "action": "host_mode", "target": "host-a", "mode": "enabled", "endpoint": "https://unapproved.example.invalid", "reason": "not enrollment"},
	} {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/policy", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+write)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("dynamic policy escaped bounded contract: %d", w.Code)
		}
	}
	after, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Config, after.Config) || len(before.Audit) != len(after.Audit) || !reflect.DeepEqual(before.LegacyUnboundHosts, after.LegacyUnboundHosts) {
		t.Fatal("unavailable management controls changed ledger state")
	}
}
