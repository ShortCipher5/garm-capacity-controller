package adminview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

type testGarmManager struct {
	reads, writes int
	err           error
	change        GarmChange
}

func (m *testGarmManager) Snapshot(context.Context) (GarmOverview, error) {
	m.reads++
	return GarmOverview{Settings: garmapi.ManagementSnapshot{Pools: []garmapi.ManagedPool{}, Repositories: []garmapi.ManagedRepository{}}, WritesEnabled: true}, m.err
}
func (m *testGarmManager) UpdatePool(_ context.Context, request, target, fingerprint string, update garmapi.PoolUpdate, reason string) (garmapi.ManagedPool, error) {
	m.writes++
	m.change = GarmChange{RequestID: request, Target: target, Fingerprint: fingerprint, Reason: reason, Pool: &update}
	return garmapi.ManagedPool{}, m.err
}
func (m *testGarmManager) UpdateRepositoryBalancer(_ context.Context, request, target, fingerprint, value, reason string) (garmapi.ManagedRepository, error) {
	m.writes++
	return garmapi.ManagedRepository{}, m.err
}
func TestGarmRoutesSeparateReadWriteAndExplicitOptIn(t *testing.T) {
	h, _, read := fixture(t)
	write := strings.Repeat("c", 64)
	m := &testGarmManager{}
	if h.SetGarmManager(m, true) == nil {
		t.Fatal("remote writes without management role")
	}
	if err := h.SetGarmManager(m, false); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", write} {
		if request(t, h, "GET", "/admin/api/v1/garm", token).Code != 401 {
			t.Fatal("unauthorized remote read")
		}
	}
	if m.reads != 0 {
		t.Fatal("unauthorized request reached Garm")
	}
	if request(t, h, "GET", "/admin/api/v1/garm", read).Code != 200 || m.reads != 1 {
		t.Fatal("read discovery failed")
	}
	if err := h.EnableManagement(write); err != nil {
		t.Fatal(err)
	}
	payload := `{"request_id":"11111111-1111-4111-8111-111111111111","target":"22222222-2222-4222-8222-222222222222","fingerprint":"` + strings.Repeat("f", 64) + `","reason":"reviewed test","pool":{"enabled":false}}`
	send := func(token, path, origin, content, body string) int {
		r := httptest.NewRequest("POST", "https://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", content)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	route := "/admin/api/v1/garm/pool"
	if send(write, route, "", "application/json", payload) != 404 || m.writes != 0 {
		t.Fatal("ledger opt-in silently enabled remote writes")
	}
	if err := h.SetGarmManager(m, true); err != nil {
		t.Fatal(err)
	}
	if send(read, route, "", "application/json", payload) != 401 || m.writes != 0 {
		t.Fatal("read token wrote Garm")
	}
	if send(write, route, "https://evil.example", "application/json", payload) != 403 || m.writes != 0 {
		t.Fatal("cross-origin mutation")
	}
	for _, body := range []string{`{"Pool":{}}`, `{"pool":null}`, strings.Replace(payload, `"enabled":false`, `"enabled":null,"priority":1`, 1), `{"pool":{},"pool":{}}`, payload + `{}`, strings.Replace(payload, `"enabled":false`, `"enabled":false,"Enabled":true`, 1), strings.Replace(payload, `"enabled":false`, `"image":"unsafe"`, 1)} {
		if send(write, route, "", "application/json", body) != 400 {
			t.Fatal("strict JSON rejected incorrectly")
		}
	}
	if send(write, route, "", "text/plain", payload) != 415 {
		t.Fatal("simple request accepted")
	}
	if send(write, route, "", "application/json", strings.Repeat("x", 8193)) != 413 {
		t.Fatal("oversize accepted")
	}
	if m.writes != 0 {
		t.Fatal("invalid requests reached manager")
	}
	if send(write, route, "https://127.0.0.1", "application/json", payload) != 200 || m.writes != 1 {
		t.Fatal("reviewed request not delegated")
	}
	if m.change.Pool.Enabled == nil || *m.change.Pool.Enabled || m.change.Reason != "reviewed test" {
		t.Fatal("typed change corrupted")
	}
	for _, pair := range []struct {
		err  error
		code int
	}{{garmapi.ErrManagementConflict, 409}, {garmapi.ErrManagementUnsafe, 400}, {garmapi.ErrManagementOutcomeUnknown, 503}, {errors.New("secret backend error"), 503}} {
		m.err = pair.err
		if send(write, route, "", "application/json", payload) != pair.code {
			t.Fatalf("wrong safe error: %v", pair.err)
		}
	}
	data, _ := json.Marshal(m.change)
	if strings.Contains(string(data), write) || strings.Contains(string(data), read) {
		t.Fatal("credential leaked into mutation data")
	}
}
