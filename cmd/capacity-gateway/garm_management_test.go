package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
)

const testManagedPool = "00000000-0000-4000-8000-000000000001"
const testManagedRepository = "00000000-0000-4000-8000-000000000002"

func TestGarmRuntimeRealTLSJournalledRoutesAndRestartUncertainty(t *testing.T) {
	keys := credentials(t)
	var mu sync.Mutex
	puts := 0
	failWrite, failRead := false, false
	pool := map[string]any{"id": testManagedPool, "provider_name": "capacity", "repo_id": testManagedRepository, "max_runners": 4, "min_idle_runners": 1, "enabled": true, "generation": 1, "priority": 0, "tags": []any{map[string]string{"name": "linux"}}}
	repo := map[string]any{"id": testManagedRepository, "owner": "example", "name": "app", "pool_balancing_type": "roundrobin"}
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer dedicated-upstream-write-fixture" {
			t.Error("wrong upstream credential")
			w.WriteHeader(401)
			return
		}
		var target map[string]any
		switch r.URL.Path {
		case "/api/v1/pools/" + testManagedPool:
			target = pool
		case "/api/v1/repositories/" + testManagedRepository:
			target = repo
		default:
			t.Errorf("unexpected remote path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.Method == "PUT" {
			puts++
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid remote body")
			}
			for key, value := range body {
				if key == "pool_balancer_type" {
					target["pool_balancing_type"] = value
				} else {
					target[key] = value
				}
			}
			if failWrite {
				w.WriteHeader(500)
				return
			}
		} else if r.Method != "GET" {
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(405)
			return
		} else if failRead {
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(target)
	}))
	remote.TLS = &tls.Config{Certificates: []tls.Certificate{keys.server}, MinVersion: tls.VersionTLS13}
	remote.StartTLS()
	defer remote.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	upstreamPath := filepath.Join(dir, "upstream.token")
	if err := os.WriteFile(upstreamPath, []byte("dedicated-upstream-write-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	config := &adminConfiguration{EnableExperimentalManagement: true, EnableExperimentalGarmManagement: true, GarmManagement: &garmManagementConfiguration{Endpoint: remote.URL, TokenFile: upstreamPath, TLS: keys.otherFiles, Providers: []string{"capacity"}, PoolIDs: []string{testManagedPool}, RepositoryIDs: []string{testManagedRepository}, Journal: filepath.Join(dir, "management.db"), QualifiedAdmissionRouting: true, ExclusiveSettingsWriter: true}}
	bootstrap, err := garmmanagement.InitializeJournal(config.GarmManagement.Journal)
	if err != nil {
		t.Fatal(err)
	}
	config.GarmManagement.JournalLineage = bootstrap.Lineage()
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}
	manager, closeManager, err := newGarmControl(config, "inventory-only-fixture", filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { closeManager() }()
	read, write := strings.Repeat("a", 64), strings.Repeat("c", 64)
	// Reuse an owned disposable store; the Garm journal remains physically separate.
	budgetStore := managementStore(t)
	h, err := adminview.New(budgetStore, read, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.EnableManagement(write); err != nil {
		t.Fatal(err)
	}
	if err := h.SetGarmManager(manager, true); err != nil {
		t.Fatal(err)
	}
	send := func(kind string, change adminview.GarmChange) int {
		body, _ := json.Marshal(change)
		r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/garm/"+kind, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+write)
		r.Header.Set("Origin", "https://127.0.0.1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	view, err := manager.Snapshot(context.Background())
	if err != nil || !view.SettingsAvailable || !view.WritesEnabled {
		t.Fatalf("initial discovery unavailable: %+v %v", view, err)
	}
	enabled := false
	change := adminview.GarmChange{RequestID: "11111111-1111-4111-8111-111111111111", Target: testManagedPool, Fingerprint: view.Settings.Pools[0].Fingerprint, Reason: "pause new pool creation", Pool: &garmapi.PoolUpdate{Enabled: &enabled}}
	if send("pool", change) != 200 {
		t.Fatal("actual pool update failed")
	}
	if send("pool", change) != 200 {
		t.Fatal("confirmed ID replay failed")
	}
	changed := change
	changed.Reason = "different operation"
	if send("pool", changed) != 409 {
		t.Fatal("request ID reused with changed input")
	}
	view, err = manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Settings.Pools[0].Enabled || len(view.History) != 1 || view.History[0].State != "confirmed" {
		t.Fatalf("missing persisted/readback result: %+v", view)
	}
	repoChange := adminview.GarmChange{RequestID: "22222222-2222-4222-8222-222222222222", Target: testManagedRepository, Fingerprint: view.Settings.Repositories[0].Fingerprint, Reason: "reviewed repository balancing", Balancer: "pack"}
	if send("repository", repoChange) != 200 {
		t.Fatal("actual repository update failed")
	}
	mu.Lock()
	failWrite = true
	mu.Unlock()
	enabled = true
	change.RequestID = "33333333-3333-4333-8333-333333333333"
	change.Fingerprint = view.Settings.Pools[0].Fingerprint
	if send("pool", change) != 503 {
		t.Fatal("ambiguous committed write reported as success/rejection")
	}
	closeManager()
	closeManager = func() {}
	manager, closeManager, err = newGarmControl(config, "inventory-only-fixture", filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	view, err = manager.Snapshot(context.Background())
	if err != nil || !view.WriteBlocked || view.Pending == nil || view.Pending.RequestID != change.RequestID {
		t.Fatalf("uncertainty lost across restart: %+v %v", view, err)
	}
	if !view.Settings.Pools[0].Enabled {
		t.Fatal("fixture did not actually commit ambiguous PUT")
	}
	mu.Lock()
	failRead = true
	mu.Unlock()
	view, err = manager.Snapshot(context.Background())
	if err != nil || view.SettingsAvailable || !view.WriteBlocked || view.Pending == nil {
		t.Fatalf("remote failure hid durable status: %+v %v", view, err)
	}
	// Wrong/missing/empty mounts must not silently initialize fresh history.
	// These are new fixture paths only; the original uncertain journal stays open.
	originalPath := config.GarmManagement.Journal
	for _, variant := range []string{"missing", "empty", "different-lineage"} {
		path := filepath.Join(dir, variant+".db")
		switch variant {
		case "empty":
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
		case "different-lineage":
			other, err := garmmanagement.InitializeJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			other.Close()
		}
		config.GarmManagement.Journal = path
		if _, close, err := newGarmControl(config, "inventory-only-fixture", filepath.Join(dir, "ledger.db")); err == nil {
			close()
			t.Fatalf("%s mount accepted", variant)
		}
		if variant == "missing" {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("missing mount silently initialized")
			}
		}
		if variant == "empty" {
			if info, err := os.Stat(path); err != nil || info.Size() != 0 {
				t.Fatal("empty mount adopted or mutated")
			}
		}
	}
	config.GarmManagement.Journal = originalPath
	view, err = manager.Snapshot(context.Background())
	if err != nil || !view.WriteBlocked || view.Pending == nil {
		t.Fatal("wrong mount attempts lost original uncertainty")
	}
	mu.Lock()
	if puts != 3 {
		t.Errorf("remote writes retried or wrong count %d", puts)
	}
	mu.Unlock()
}

func managementStore(t *testing.T) *admission.SQLiteStore {
	t.Helper()
	budget := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"app": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
func TestGarmRuntimeRejectsMissingOptInSharedCredentialAndJournal(t *testing.T) {
	keys := credentials(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "upstream.token")
	if os.WriteFile(path, []byte("inventory-token"), 0600) != nil {
		t.Fatal("token fixture")
	}
	config := &adminConfiguration{EnableExperimentalManagement: true, EnableExperimentalGarmManagement: true, GarmManagement: &garmManagementConfiguration{Endpoint: "https://127.0.0.1:1", TokenFile: path, TLS: keys.otherFiles, Providers: []string{"capacity"}, PoolIDs: []string{testManagedPool}, Journal: filepath.Join(dir, "journal.db"), QualifiedAdmissionRouting: true, ExclusiveSettingsWriter: false, JournalLineage: strings.Repeat("b", 64)}}
	if _, close, err := newGarmControl(config, "inventory-token", filepath.Join(dir, "ledger.db")); err == nil {
		close()
		t.Fatal("missing exclusive-writer acknowledgement accepted")
	}
	config.GarmManagement.ExclusiveSettingsWriter = true
	if _, close, err := newGarmControl(config, "inventory-token", filepath.Join(dir, "ledger.db")); err == nil {
		close()
		t.Fatal("inventory token reused for management")
	}
	if os.WriteFile(path, []byte("separate-write-token"), 0600) != nil {
		t.Fatal("write fixture")
	}
	config.EnableExperimentalManagement = false
	if _, close, err := newGarmControl(config, "inventory-token", filepath.Join(dir, "ledger.db")); err == nil {
		close()
		t.Fatal("remote writes without management role")
	}
	config.EnableExperimentalManagement = true
	config.GarmManagement.Journal = filepath.Join(dir, "ledger.db")
	if _, close, err := newGarmControl(config, "inventory-token", filepath.Join(dir, "ledger.db")); err == nil {
		close()
		t.Fatal("journal shared admission ledger")
	}
}

func TestGarmWriteStartupRejectsObviousRoutingMismatches(t *testing.T) {
	config := configuration{Garm: garmConfiguration{Endpoint: "https://garm.example.invalid", Providers: []string{"capacity"}}, Pools: map[string]gateway.PoolPolicy{testManagedPool: {Host: "a"}}, Admin: &adminConfiguration{EnableExperimentalGarmManagement: true, GarmManagement: &garmManagementConfiguration{Endpoint: "https://garm.example.invalid/", Providers: []string{"capacity"}, PoolIDs: []string{testManagedPool}, QualifiedAdmissionRouting: true}}}
	if err := validateGarmManagementRouting(config); err != nil {
		t.Fatal(err)
	}
	config.Admin.GarmManagement.Endpoint = "https://other.example.invalid"
	if validateGarmManagementRouting(config) == nil {
		t.Fatal("different Garm destination allowed for writes")
	}
	config.Admin.GarmManagement.Endpoint = config.Garm.Endpoint
	config.Admin.GarmManagement.Providers = []string{"unrestricted-provider"}
	if validateGarmManagementRouting(config) == nil {
		t.Fatal("foreign provider scope allowed")
	}
	config.Admin.GarmManagement.Providers = []string{"capacity"}
	config.Admin.GarmManagement.PoolIDs = []string{testManagedRepository}
	if validateGarmManagementRouting(config) == nil {
		t.Fatal("pool without fixed facade policy allowed")
	}
	config.Admin.GarmManagement.PoolIDs = []string{testManagedPool}
	config.Admin.GarmManagement.QualifiedAdmissionRouting = false
	if validateGarmManagementRouting(config) == nil {
		t.Fatal("unqualified routing implicitly enabled")
	}
	config.Admin.EnableExperimentalGarmManagement = false
	if err := validateGarmManagementRouting(config); err != nil {
		t.Fatal("read-only discovery incorrectly claimed capacity routing")
	}
}
