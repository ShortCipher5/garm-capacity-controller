package adminview

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
)

type fakeDiscoverySource struct{ calls atomic.Int32 }

func (f *fakeDiscoverySource) PhysicalIdentity(context.Context) (incusapi.PhysicalServerIdentity, error) {
	f.calls.Add(1)
	return incusapi.PhysicalServerIdentity{}, nil
}
func (f *fakeDiscoverySource) HostTotals(context.Context) (incusapi.HostTotals, error) {
	return incusapi.HostTotals{CPUMillisTotal: 8000, RAMMiBTotal: 32768, DiskMiBTotal: 524288}, nil
}
func (f *fakeDiscoverySource) Guests(context.Context) ([]incusapi.Guest, error) {
	return []incusapi.Guest{{Name: "runner-1", Status: "Running"}, {Name: "runner-2", Status: "Running", Controller: "ctl-1"}}, nil
}
func (f *fakeDiscoverySource) SampleUtilization(context.Context) (incusapi.UtilizationSample, error) {
	return incusapi.UtilizationSample{CPUUsedMillis: 100, RAMUsedMiB: 200, DiskUsedMiB: 300, Guests: 2}, nil
}

func discoveryHandler(t *testing.T, probeToken bool) (*Handler, *fakeDiscoverySource, *admission.SQLiteStore, string, string) {
	t.Helper()
	h, store, read := fixture(t)
	write := strings.Repeat("c", 64)
	if err := h.EnableManagement(write); err != nil {
		t.Fatal(err)
	}
	source := &fakeDiscoverySource{}
	manager, err := onboarding.New(store, map[string]onboarding.Observer{"candidate-b": source}, true)
	if err != nil {
		t.Fatal(err)
	}
	token := ""
	if probeToken {
		token = write
	}
	if err := h.SetOnboarding(manager, token); err != nil {
		t.Fatal(err)
	}
	return h, source, store, read, write
}

func sendDiscovery(t *testing.T, h *Handler, method, target, token, origin string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://127.0.0.1"+target, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDiscoveryEndpointIsScopedAuditedAndNonEnrolling(t *testing.T) {
	h, source, store, read, write := discoveryHandler(t, true)
	before, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sendDiscovery(t, h, "GET", "/admin/api/v1/onboarding/profiles/candidate-b/discovery", "", "").Code != 401 {
		t.Fatal("unauthenticated discovery")
	}
	if sendDiscovery(t, h, "GET", "/admin/api/v1/onboarding/profiles/candidate-b/discovery", read, "").Code != 401 {
		t.Fatal("read token authorized external discovery")
	}
	if sendDiscovery(t, h, "GET", "/admin/api/v1/onboarding/profiles/candidate-b/discovery", write, "https://unapproved.example.invalid").Code != 403 {
		t.Fatal("cross-origin discovery allowed")
	}
	if sendDiscovery(t, h, "POST", "/admin/api/v1/onboarding/profiles/candidate-b/discovery", write, "").Code != 405 {
		t.Fatal("non-GET discovery allowed")
	}
	for _, target := range []string{"/admin/api/v1/onboarding/profiles/unknown/discovery", "/admin/api/v1/onboarding/profiles/bad.name/discovery", "/admin/api/v1/onboarding/profiles/a/b/discovery", "/admin/api/v1/onboarding/profiles/candidate-b/other"} {
		if sendDiscovery(t, h, "GET", target, write, "").Code != 404 {
			t.Fatalf("unsafe discovery route accepted: %s", target)
		}
	}
	rejected, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if source.calls.Load() != 0 || len(rejected.Audit) != len(before.Audit) {
		t.Fatal("rejected discovery caused read/audit")
	}
	response := sendDiscovery(t, h, "GET", "/admin/api/v1/onboarding/profiles/candidate-b/discovery", write, "")
	if response.Code != 200 {
		t.Fatalf("discovery rejected: %d %s", response.Code, response.Body.String())
	}
	var result onboarding.DiscoveryResult
	if json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatal("invalid discovery JSON")
	}
	if result.Profile != "candidate-b" || result.Identity.ObservationAvailable || len(result.Guests) != 2 || result.Utilization == nil || result.Utilization.Source != "guest-state" {
		t.Fatalf("discovery result sections: %+v", result)
	}
	if result.Totals == nil || result.Totals.CPUMillis == nil || *result.Totals.CPUMillis != 8000 || result.SuggestedBudget.CPU != 6000 {
		t.Fatalf("discovery totals/suggestion: %+v %+v", result.Totals, result.SuggestedBudget)
	}
	found := false
	for _, blocker := range result.Blockers {
		if blocker == "physical_capacity_scope_unqualified" {
			found = true
		}
	}
	if !found {
		t.Fatalf("qualification blocker missing: %v", result.Blockers)
	}
	if !strings.Contains(response.Body.String(), "unmanaged guests: the shared limit may not cover them") {
		t.Fatalf("unmanaged warning missing: %s", response.Body.String())
	}
	after, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Audit) != len(before.Audit)+2 || len(after.HostBindings) != len(before.HostBindings) || after.Ready != before.Ready || after.Epoch != before.Epoch || len(after.Config.Hosts) != len(before.Config.Hosts) {
		t.Fatal("discovery changed gate/binding/policy or omitted audit")
	}
	kinds := map[string]bool{}
	for _, event := range after.Audit {
		kinds[event.Kind] = true
	}
	if !kinds["onboarding_discovery_requested"] || !kinds["onboarding_discovery_completed"] {
		t.Fatalf("discovery audit pair missing: %v", after.Audit)
	}
}

func TestDiscoveryEndpointRequiresExplicitProbeOptIn(t *testing.T) {
	h, source, store, _, write := discoveryHandler(t, false)
	before, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sendDiscovery(t, h, "GET", "/admin/api/v1/onboarding/profiles/candidate-b/discovery", write, "").Code != 404 {
		t.Fatal("probe-opt-out discovery allowed")
	}
	if source.calls.Load() != 0 || len(snapshotAudit(t, store)) != len(before.Audit) {
		t.Fatal("disabled discovery caused read/audit")
	}
}

func snapshotAudit(t *testing.T, store *admission.SQLiteStore) []admission.AuditEvent {
	t.Helper()
	snapshot, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Audit
}

func sendEnroll(t *testing.T, h *Handler, body, token, origin, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/hosts", strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestEnrollHostEndpointRegistersDisabledHostAndOverviewShowsDetected(t *testing.T) {
	h, _, _, read, write := discoveryHandler(t, false)
	detected := admission.Resources{CPU: 8000, RAM: 32768, Disk: 524288}
	body := `{"name":"host-b","budget":{"cpu_millis":2000,"ram_mib":4096,"disk_mib":51200,"runners":2},"detected":{"cpu_millis":8000,"ram_mib":32768,"disk_mib":524288,"runners":0},"mode":"disabled"}`
	if sendEnroll(t, h, body, write, "", "application/json").Code != 201 {
		t.Fatal("authorized enrollment rejected")
	}
	duplicate := sendEnroll(t, h, body, write, "", "application/json")
	if duplicate.Code != 409 {
		t.Fatalf("duplicate enrollment: %d", duplicate.Code)
	}
	response := request(t, h, "GET", "/admin/api/v1/overview", read)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var view Overview
	if json.Unmarshal(response.Body.Bytes(), &view) != nil {
		t.Fatal("invalid overview")
	}
	found := false
	for _, host := range view.Hosts {
		if host.ID != "host-b" {
			continue
		}
		found = true
		if host.Mode != "disabled" || !host.Healthy || host.Detected == nil || *host.Detected != detected {
			t.Fatalf("enrolled host view: %+v", host)
		}
	}
	if !found {
		t.Fatal("enrolled host missing from overview")
	}
}

func TestEnrollHostEndpointValidationAndScope(t *testing.T) {
	h, _, store, read, write := discoveryHandler(t, false)
	before, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	initialHosts := len(before.Config.Hosts)
	valid := `{"name":"host-b","budget":{"cpu_millis":2000,"ram_mib":4096,"disk_mib":51200,"runners":2},"mode":"disabled"}`
	if sendEnroll(t, h, valid, write, "", "application/json").Code != 201 {
		t.Fatal("baseline enrollment rejected")
	}
	if sendEnroll(t, h, `{"name":"host-c","budget":{"cpu_millis":2000,"ram_mib":4096,"runners":2},"mode":"enabled"}`, write, "", "application/json").Code != 400 {
		t.Fatal("enabled initial mode accepted")
	}
	if sendEnroll(t, h, `{"name":"host-c","budget":{"cpu_millis":-1,"ram_mib":4096,"runners":2},"mode":"disabled"}`, write, "", "application/json").Code != 400 {
		t.Fatal("negative budget accepted")
	}
	if sendEnroll(t, h, `{"name":"host-c","budget":{"cpu_millis":2000,"ram_mib":4096,"runners":2},"mode":"disabled","endpoint":"https://unapproved.example.invalid"}`, write, "", "application/json").Code != 400 {
		t.Fatal("browser-supplied endpoint accepted")
	}
	if sendEnroll(t, h, strings.Repeat("x", 8193), write, "", "application/json").Code != 413 {
		t.Fatal("unbounded enrollment accepted")
	}
	if sendEnroll(t, h, valid, write, "", "text/plain").Code != 415 {
		t.Fatal("simple content accepted")
	}
	if sendEnroll(t, h, valid, write, "https://unapproved.example.invalid", "application/json").Code != 403 {
		t.Fatal("cross-origin enrollment allowed")
	}
	if sendEnroll(t, h, valid, read, "", "application/json").Code != 401 {
		t.Fatal("read token enrolled a host")
	}
	after, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Config.Hosts) != initialHosts+1 || len(after.Audit) != len(before.Audit)+1 {
		t.Fatalf("rejected enrollments mutated policy: hosts=%d audit=%d", len(after.Config.Hosts), len(after.Audit))
	}
}

func TestEnrollHostEndpointRequiresManagementRole(t *testing.T) {
	h, store, read := fixture(t)
	before, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"name":"host-b","budget":{"cpu_millis":2000,"ram_mib":4096,"runners":2},"mode":"disabled"}`
	if sendEnroll(t, h, body, strings.Repeat("c", 64), "", "application/json").Code != 404 {
		t.Fatal("enrollment available without management role")
	}
	if request(t, h, "POST", "/admin/api/v1/hosts", read).Code != 404 {
		t.Fatal("read-only host route exposed")
	}
	after, _ := store.ReadSnapshot(context.Background())
	if len(after.Config.Hosts) != len(before.Config.Hosts) || len(after.Audit) != len(before.Audit) {
		t.Fatal("unmanaged enrollment mutated ledger")
	}
}
