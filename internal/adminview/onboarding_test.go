package adminview

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
)

type countedProbe struct{ calls atomic.Int32 }

func (p *countedProbe) PhysicalIdentity(context.Context) (incusapi.PhysicalServerIdentity, error) {
	p.calls.Add(1)
	return incusapi.PhysicalServerIdentity{}, nil
}
func TestOnboardingRoutesAreExplicitScopedAuditedAndNonEnrolling(t *testing.T) {
	h, store, read := fixture(t)
	write := strings.Repeat("c", 64)
	observer := &countedProbe{}
	manager, err := onboarding.New(store, map[string]onboarding.Observer{"candidate-b": observer}, true)
	if err != nil {
		t.Fatal(err)
	}
	if h.SetOnboarding(manager, read) == nil {
		t.Fatal("probe role implicitly enabled")
	}
	if err := h.SetOnboarding(manager, ""); err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", write} {
		if request(t, h, "GET", "/admin/api/v1/onboarding", token).Code != 401 {
			t.Fatal("unauthorized profile discovery")
		}
	}
	response := request(t, h, "GET", "/admin/api/v1/onboarding", read)
	var discovery onboarding.Discovery
	if json.Unmarshal(response.Body.Bytes(), &discovery) != nil || response.Code != 200 || discovery.ProbeEnabled || discovery.EnrollmentSupported || discovery.CapacityQualified {
		t.Fatal("misleading profile discovery")
	}
	body := `{"request_id":"11111111-1111-4111-8111-111111111111","profile_id":"candidate-b","reason":"explicit read-only check"}`
	send := func(token, origin, content, body string) int {
		r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/onboarding/probe", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", content)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if send(write, "", "application/json", body) != 404 {
		t.Fatal("management role silently enabled probe capability")
	}
	if err := h.SetOnboarding(manager, write); err != nil {
		t.Fatal(err)
	}
	if send(read, "", "application/json", body) != 401 {
		t.Fatal("read token authorized external probe")
	}
	if send(write, "https://unapproved.example.invalid", "application/json", body) != 403 {
		t.Fatal("cross-origin probe allowed")
	}
	if send(write, "", "text/plain", body) != 415 {
		t.Fatal("simple content accepted")
	}
	if send(write, "", "application/json", strings.Repeat("x", 2049)) != 413 {
		t.Fatal("unbounded probe request")
	}
	for _, bad := range []string{`{"profile_id":"candidate-b","profile_id":"other"}`, strings.Replace(body, `"profile_id"`, `"Profile_ID"`, 1), strings.Replace(body, `"candidate-b"`, `null`, 1), strings.TrimSuffix(body, "}") + `,"endpoint":"https://unapproved.example.invalid"}`, strings.Replace(body, `"candidate-b"`, `"unknown-profile"`, 1), body + ` {}`} {
		if send(write, "", "application/json", bad) != 400 {
			t.Fatalf("unscoped or ambiguous input accepted: %s", bad)
		}
	}
	rejected, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observer.calls.Load() != 0 || len(rejected.Audit) != len(before.Audit) {
		t.Fatal("rejected request caused network read/audit")
	}
	r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/onboarding/probe", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+write)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://127.0.0.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var result onboarding.Result
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ObservationAvailable || result.EnrollmentPerformed || result.ActivationPerformed || result.CapacityQualified || observer.calls.Load() != 1 {
		t.Fatal("probe fabricated qualified/enrolled result")
	}
	after, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Audit) != len(before.Audit)+2 || len(after.HostBindings) != len(before.HostBindings) || after.Ready != before.Ready || after.Epoch != before.Epoch {
		t.Fatal("probe changed gate/binding or omitted audit")
	}
	if h.management || request(t, h, "POST", "/admin/api/v1/policy", write).Code != 404 {
		t.Fatal("read-only probe enabled unrelated ledger-policy writes")
	}
	if request(t, h, "GET", "/admin/api/v1/onboarding", write).Code != 401 {
		t.Fatal("write token silently granted read role")
	}
	for _, route := range []string{"/admin/api/v1/onboarding/enroll", "/admin/api/v1/onboarding/activate", "/admin/api/v1/onboarding/reset"} {
		if request(t, h, "POST", route, write).Code != 404 {
			t.Fatal("preflight exposed an activation/reset route")
		}
	}
}
