package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectionHTTPReviewGateAndLateObservation(t *testing.T) {
	h, source, store, read, write := discoveryHandler(t, true)
	service, err := onboarding.NewConnectionService(store, map[string]onboarding.ConnectionTarget{"alias": {Endpoint: "https://192.0.2.10:8443", Project: "runners", Factory: func(admission.Connection) (onboarding.Observer, func(), error) { return source, func() {}, nil }}}, []string{"candidate-b"})
	if err != nil {
		t.Fatal(err)
	}
	h.onboarding.SetConnections(service)
	send := func(method, body, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/onboarding/connections", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if send("GET", "", read, "").Code != 401 {
		t.Fatal("read token exposed connection config")
	}
	if send("GET", "", write, "https://elsewhere.invalid").Code != 403 {
		t.Fatal("cross-origin connection action")
	}
	body := `{"action":"save-draft","expected_revision":0,"connection":{"id":"dynamic","credential_ref":"alias","endpoint":"https://192.0.2.10:8443","project":"runners","server_key_sha256":"` + strings.Repeat("a", 64) + `"}}`
	if w := send("POST", body, write, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := sendDiscovery(t, h, "GET", "/admin/api/v1/onboarding/profiles/dynamic/discovery", write, ""); w.Code != 404 {
		t.Fatal("unapproved draft discovered", w.Body.String())
	}
	approval := `{"action":"approve","id":"dynamic","expected_revision":1,"approved_key":"` + strings.Repeat("a", 64) + `"}`
	if w := send("POST", approval, write, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if send("POST", approval, write, "").Code != 409 {
		t.Fatal("stale approval replayed")
	}
	w := sendDiscovery(t, h, "GET", "/admin/api/v1/onboarding/profiles/dynamic/discovery", write, "")
	var result onboarding.DiscoveryResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Review == nil || result.ConnectionRevision != 2 {
		t.Fatal("dynamic reviewed observation missing", w.Body.String())
	}
	if w := send("POST", `{"action":"revoke","id":"dynamic","expected_revision":2}`, write, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := store.EnrollHostFromReview(context.Background(), "new-host", admission.Resources{CPU: 1, RAM: 1}, "disabled", *result.Review); err != admission.ErrConflict {
		t.Fatal("revoked review enrolled")
	}
}
