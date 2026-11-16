package adminview

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminJSONRefusesUnicodeFieldAliases(t *testing.T) {
	for _, body := range []string{`{"max_runners":4,"max_runnerſ":1}`, `{"max_runner\u017f":1}`, `{"pool":{"tagſ":["linux"]}}`, `{"request_id":"one","requeſt_id":"two"}`} {
		if uniqueJSON([]byte(body)) {
			t.Fatalf("accepted alias: %s", body)
		}
	}
	for _, body := range []string{`{"reason":"日本語-ſ","pool":{"tags":["日本語-ſ"]}}`, `{"max_runners":4}`, `{"pool":{"max_runners":4}}`} {
		if !uniqueJSON([]byte(body)) {
			t.Fatalf("rejected canonical request with Unicode values: %s", body)
		}
	}
}

func TestGarmUnicodeDuplicateAliasNeverReachesWriter(t *testing.T) {
	h, _, _ := fixture(t)
	token := strings.Repeat("c", 64)
	if err := h.EnableManagement(token); err != nil {
		t.Fatal(err)
	}
	manager := &testGarmManager{}
	if err := h.SetGarmManager(manager, true); err != nil {
		t.Fatal(err)
	}
	for _, fields := range []string{`"max_runners":4,"max_runnerſ":1`, `"max_runner\u017f":1,"max_runners":4`, `"tagſ":["linux"]`} {
		payload := `{"request_id":"11111111-1111-4111-8111-111111111111","target":"22222222-2222-4222-8222-222222222222","fingerprint":"` + strings.Repeat("f", 64) + `","reason":"reviewed test","pool":{` + fields + `}}`
		req := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/garm/pool", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != 400 || manager.writes != 0 {
			t.Fatalf("status=%d writes=%d", response.Code, manager.writes)
		}
	}
}
