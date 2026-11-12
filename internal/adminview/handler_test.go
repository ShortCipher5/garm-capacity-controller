package adminview

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func fixture(t *testing.T) (*Handler, *admission.SQLiteStore, string) {
	t.Helper()
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	token := strings.Repeat("a", 64)
	handler, err := New(store, token, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	return handler, store, token
}
func request(t *testing.T, h *Handler, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://127.0.0.1"+target, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func baseline(t *testing.T, store *admission.SQLiteStore, resources []admission.InventoryResource) {
	t.Helper()
	fence, err := store.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = store.CompleteReconciliation(context.Background(), fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host-a", ObservedAt: time.Now(), Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}, Resources: resources})
	if err != nil {
		t.Fatal(err)
	}
}
func TestReadOnlyAuthenticationAndNoMutationOrURLTokens(t *testing.T) {
	h, store, token := fixture(t)
	before, _ := store.Snapshot(context.Background())
	for _, value := range []string{"", strings.Repeat("b", 64), "Basic fake"} {
		response := request(t, h, "GET", "/admin/api/v1/overview", value)
		if response.Code != 401 {
			t.Fatalf("bad token accepted %d", response.Code)
		}
	}
	for _, route := range []string{"/admin/api/v1/config", "/admin/api/v1/host-mode", "/v1/create", "/v1/cancel-admission", "/admin/api/v1/cancel-admission", "/admin/api/v1/delete"} {
		response := request(t, h, "POST", route, token)
		if response.Code < 400 {
			t.Fatal("admin token gained mutation path")
		}
	}
	if response := request(t, h, "POST", "/admin/api/v1/overview", token); response.Code != 405 {
		t.Fatal("non-GET overview allowed")
	}
	response := request(t, h, "GET", "/admin/api/v1/overview?token="+token, token)
	if response.Code != 400 || strings.Contains(response.Body.String(), token) {
		t.Fatal("URL token accepted/leaked")
	}
	after, _ := store.Snapshot(context.Background())
	if len(before.Audit) != len(after.Audit) || after.Ready {
		t.Fatal("unauthorized request accessed/mutated admission")
	}
	if request(t, h, "OPTIONS", "/admin/api/v1/overview", token).Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("cross-origin auth enabled")
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1/admin/api/v1/overview", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("plaintext admin API accepted")
	}
}
func TestOverviewUsesActualConservativeLedgerWithoutPayloads(t *testing.T) {
	h, store, token := fixture(t)
	baseline(t, store, nil)
	req := admission.Request{Key: "hashed-request-1", Pool: "pool-a", Host: "host-a", Resources: admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}}
	if _, err := store.Admit(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	baseline(t, store, []admission.InventoryResource{{ID: "private-runner-name", Host: "host-a", Pool: "pool-a", ReservationKey: req.Key, Resources: admission.Resources{CPU: 2000, RAM: 2048, Runners: 1}, State: "idle"}, {ID: "private-orphan", Host: "host-a", Pool: "pool-a", Resources: admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}, State: "unknown"}})
	response := request(t, h, "GET", "/admin/api/v1/overview", token)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var view Overview
	if json.Unmarshal(response.Body.Bytes(), &view) != nil {
		t.Fatal("invalid overview")
	}
	if view.Mode != "experimental-ledger" || !view.Ready || view.Global.Allocated.CPU != 3000 || view.Global.Allocated.Runners != 2 || view.States["idle"] != 1 || view.States["unknown"] != 1 {
		t.Fatalf("bad actual ledger view %+v", view)
	}
	if strings.Contains(response.Body.String(), "private-runner-name") || strings.Contains(response.Body.String(), "private-orphan") || strings.Contains(response.Body.String(), token) {
		t.Fatal("unnecessary private payload/credentials exposed")
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("private view cache/referrer policy absent")
	}
	if err := store.SetHostMode(context.Background(), "host-a", "draining"); err != nil {
		t.Fatal(err)
	}
	response = request(t, h, "GET", "/admin/api/v1/overview", token)
	_ = json.Unmarshal(response.Body.Bytes(), &view)
	if view.Ready || !view.Hosts[0].AdmissionPaused || view.Hosts[0].Mode != "draining" || view.Global.Allocated.Runners != 2 {
		t.Fatal("host pause misrepresented state/charges")
	}
	found := false
	for _, event := range view.Events {
		if event.Kind == "host_mode_draining" {
			found = true
			if event.Host != "host-a" || event.RequestKey != "" {
				t.Fatal("mode audit target incorrect")
			}
		}
	}
	if !found {
		t.Fatal("host audit missing")
	}
}
func TestFailureIsNotAnEmptyHealthyOverview(t *testing.T) {
	h, store, token := fixture(t)
	_ = store.Close()
	response := request(t, h, "GET", "/admin/api/v1/overview", token)
	if response.Code != 503 || strings.Contains(response.Body.String(), token) {
		t.Fatal("failed ledger reported current data or leaked error")
	}
}
func TestStaticAssetsAreConfinedAndNeverListOrServeSecrets(t *testing.T) {
	h, _, _ := fixture(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Public app shell</title>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log('static')"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "credential.key")
	if err := os.WriteFile(outside, []byte("must-not-serve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "assets", "escape.js")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "assets", "fifo.js"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h.assets = root
	for _, route := range []string{"/", "/assets/app.js"} {
		if response := request(t, h, "GET", route, ""); response.Code != 200 || response.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("app shell not served safely")
		}
	}
	for _, route := range []string{"/assets/", "/assets/escape.js", "/assets/fifo.js", "/assets/../index.html", "/assets/.hidden.js", "/private/credential.key", "/assets/credential.key"} {
		response := request(t, h, "GET", route, "")
		if response.Code != 404 || strings.Contains(response.Body.String(), "must-not-serve") {
			t.Fatalf("unsafe static route %s code%d", route, response.Code)
		}
	}
}
func TestSeparateTLSReadOnlySurfaceRejectsCookiesAndDuplicateAuth(t *testing.T) {
	h, _, token := fixture(t)
	server := httptest.NewTLSServer(h)
	defer server.Close()
	client := server.Client()
	r, _ := http.NewRequest("GET", server.URL+"/admin/api/v1/overview", nil)
	r.AddCookie(&http.Cookie{Name: "admin", Value: token})
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("cookie acted as bearer credential")
	}
	r, _ = http.NewRequest("GET", server.URL+"/admin/api/v1/overview", nil)
	r.Header.Add("Authorization", "Bearer "+token)
	r.Header.Add("Authorization", "Bearer "+token)
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("duplicate auth accepted")
	}
}
