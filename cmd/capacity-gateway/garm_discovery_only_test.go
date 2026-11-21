package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	common "github.com/cloudbase/garm-provider-common/params"
)

func TestGarmZeroEnrolledRuntimeDiscoveryNoJournalOrWrites(t *testing.T) {
	keys := credentials(t)
	var reads, scaleReads atomic.Int32
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || (r.URL.Path != "/api/v1/pools" && r.URL.Path != "/api/v1/scalesets") || r.Header.Get("Authorization") != "Bearer upstream-discovery-fixture" {
			t.Error("unexpected upstream action")
			w.WriteHeader(400)
			return
		}
		if r.URL.Path == "/api/v1/scalesets" {
			scaleReads.Add(1)
			json.NewEncoder(w).Encode([]any{map[string]any{"id": 42, "name": "existing-scale", "provider_name": "capacity", "state": "created", "generation": 1, "max_runners": 4, "min_idle_runners": 0, "enabled": true, "tags": []any{}}})
			return
		}
		reads.Add(1)
		json.NewEncoder(w).Encode([]any{map[string]any{"id": testManagedPool, "provider_name": "capacity", "max_runners": 4, "min_idle_runners": 1, "enabled": true, "generation": 1, "priority": 0, "tags": []any{map[string]string{"name": "linux"}}}})
	}))
	remote.TLS = &tls.Config{Certificates: []tls.Certificate{keys.server}, MinVersion: tls.VersionTLS13}
	remote.StartTLS()
	defer remote.Close()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	token := filepath.Join(dir, "token")
	if e := os.WriteFile(token, []byte("upstream-discovery-fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	config := &adminConfiguration{GarmManagement: &garmManagementConfiguration{DiscoveryOnly: true, Endpoint: remote.URL, TokenFile: token, TLS: keys.otherFiles, Providers: []string{"capacity"}}}
	view, closeView, e := newGarmControl(config, "different-inventory-token", filepath.Join(dir, "ledger.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer closeView()
	snap, e := view.Snapshot(context.Background())
	if e != nil || !snap.DiscoveryOnly || snap.PoolInventory == nil || len(snap.PoolInventory.Pools) != 1 || snap.PoolInventory.Pools[0].Configured || snap.WritesEnabled {
		t.Fatal("invalid discovery", e)
	}
	if _, e = view.UpdatePool(context.Background(), "", testManagedPool, "", garmapi.PoolUpdate{}, ""); e != garmapi.ErrManagementDisabled {
		t.Fatal("mutation available")
	}
	if reads.Load() != 1 || scaleReads.Load() != 1 || snap.ScaleSetInventory == nil || len(snap.ScaleSetInventory.ScaleSets) != 1 || snap.ScaleSetInventory.ScaleSets[0].ID != 42 {
		t.Fatal("unexpected traffic")
	}
	files, e := os.ReadDir(dir)
	if e != nil || len(files) != 1 {
		t.Fatal("discovery created persistent state", e)
	}
	for _, mutate := range []func(*adminConfiguration){func(c *adminConfiguration) { c.EnableExperimentalGarmManagement = true }, func(c *adminConfiguration) { c.GarmManagement.PoolIDs = []string{testManagedPool} }, func(c *adminConfiguration) { c.GarmManagement.Journal = filepath.Join(dir, "new.db") }} {
		copy := *config
		remoteCopy := *config.GarmManagement
		copy.GarmManagement = &remoteCopy
		mutate(&copy)
		if _, _, e := newGarmControl(&copy, "different-inventory-token", filepath.Join(dir, "ledger.db")); e == nil {
			t.Fatal("accepted discovery/write ambiguity")
		}
	}
}

func TestGarmDiscoveryStaticPreflightMatchesRuntimeWithoutEffects(t *testing.T) {
	keys := credentials(t)
	var requests, connections atomic.Int32
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	remote.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	remote.TLS = &tls.Config{Certificates: []tls.Certificate{keys.server}, MinVersion: tls.VersionTLS13}
	remote.StartTLS()
	defer remote.Close()

	dir := t.TempDir()
	write := func(name, value string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const inventory = "fixture-inventory-token"
	inventoryFile := write("inventory.token", inventory)
	adminFile := write("admin.token", strings.Repeat("a", 64))
	discoveryFile := write("discovery.token", "fixture-discovery-token")
	journal := filepath.Join(dir, "must-not-exist.journal")
	c := configuration{
		Listen: "127.0.0.1:8443", Controller: "controller-test", Database: filepath.Join(dir, "must-not-exist.db"),
		IdentityFile: filepath.Join(dir, "must-not-exist.identity"),
		Pools:        map[string]gateway.PoolPolicy{"pool-a": {Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}},
		Hosts:        map[string]hostConfiguration{"host-a": {Endpoint: remote.URL, Project: "runners", TLS: keys.otherFiles}},
		Garm:         garmConfiguration{Endpoint: remote.URL, TokenFile: inventoryFile, TLS: keys.otherFiles, Providers: []string{"capacity"}},
		ServerTLS:    keys.serverFiles, ProviderFingerprints: []string{strings.Repeat("b", 64)},
		Admin: &adminConfiguration{Listen: "127.0.0.1:8444", TokenFile: adminFile, TLS: keys.serverFiles,
			GarmManagement: &garmManagementConfiguration{DiscoveryOnly: true, Endpoint: remote.URL, TokenFile: discoveryFile, TLS: keys.otherFiles, Providers: []string{"capacity"}}},
	}
	// The static path must not open dedicated discovery token/TLS paths.
	// Runtime retains its existing authority checks and cannot open them.
	noDiscoveryFiles, adminShape, discoveryShape := c, *c.Admin, *c.Admin.GarmManagement
	noDiscoveryFiles.Admin, adminShape.GarmManagement = &adminShape, &discoveryShape
	discoveryShape.TokenFile = filepath.Join(dir, "never-opened.token")
	discoveryShape.TLS.CA = filepath.Join(dir, "never-opened-ca.pem")
	discoveryShape.TLS.Certificate = filepath.Join(dir, "never-opened-cert.pem")
	discoveryShape.TLS.Key = filepath.Join(dir, "never-opened.key")
	if err := checkStaticConfig(noDiscoveryFiles, false); err != nil {
		t.Fatal("static discovery tried to load runtime credentials", err)
	}
	if _, release, err := newGarmControl(&adminShape, inventory, c.Database); err == nil {
		release()
		t.Fatal("discovery runtime ignored missing material")
	}
	// Preflight does not invent role-path restrictions for this existing
	// mode: relative paths and combined TLS PEMs stay valid shape.
	discoveryShape.TokenFile = "private/token"
	discoveryShape.TLS = securefiles.TLSFiles{CA: "private/combined.pem", Certificate: "private/combined.pem", Key: "private/combined.pem"}
	if err := checkStaticConfig(noDiscoveryFiles, false); err != nil {
		t.Fatal("relative or combined runtime credential paths rejected as shape", err)
	}

	if err := checkStaticConfig(c, false); err != nil {
		t.Fatal("valid runtime discovery config still fails preflight", err)
	}
	_, closeView, err := newGarmControl(c.Admin, inventory, c.Database)
	if err != nil {
		t.Fatal("valid discovery runtime refused", err)
	}
	closeView()

	for _, tc := range []struct {
		name   string
		mutate func(*adminConfiguration, *garmManagementConfiguration)
	}{
		{"pool scope", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.PoolIDs = []string{testManagedPool} }},
		{"repo scope", func(_ *adminConfiguration, m *garmManagementConfiguration) {
			m.RepositoryIDs = []string{testManagedPool}
		}},
		{"journal", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.Journal = journal }},
		{"lineage", func(_ *adminConfiguration, m *garmManagementConfiguration) {
			m.JournalLineage = strings.Repeat("c", 64)
		}},
		{"acknowledged routing", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.QualifiedAdmissionRouting = true }},
		{"exclusive writer", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.ExclusiveSettingsWriter = true }},
		{"conditional writer", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.ExperimentalConditionalSettings = true }},
		{"managed mode", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.ManagedSupervisor = true }},
		{"periodic cap", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.PolicyCapIntervalSeconds = 60 }},
		{"writer opt-in", func(a *adminConfiguration, _ *garmManagementConfiguration) { a.EnableExperimentalGarmManagement = true }},
		{"missing token path", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.TokenFile = "" }},
		{"missing TLS key path", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.TLS.Key = "" }},
		{"missing provider scope", func(_ *adminConfiguration, m *garmManagementConfiguration) { m.Providers = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, admin, management := c, *c.Admin, *c.Admin.GarmManagement
			config.Admin, admin.GarmManagement = &admin, &management
			tc.mutate(&admin, &management)
			if err := checkStaticConfig(config, false); err == nil {
				t.Fatal("mixed discovery settings passed preflight")
			}
			if _, release, err := newGarmControl(&admin, inventory, config.Database); err == nil {
				release()
				t.Fatal("mixed discovery settings passed runtime")
			}
		})
	}
	// Only the existing runtime loader can validate actual trust and token
	// role separation. Static shape is not a credential qualification.
	for _, path := range []string{inventoryFile, adminFile} {
		adminRuntime, managementRuntime := *c.Admin, *c.Admin.GarmManagement
		adminRuntime.GarmManagement = &managementRuntime
		managementRuntime.TokenFile = path
		if _, release, e := newGarmControl(&adminRuntime, inventory, c.Database); e == nil {
			release()
			t.Fatal("runtime allowed role token reuse")
		}
	}
	// The legacy non-discovery preflight still requires explicit journal
	// syntax and exact targets but must not open or initialize that journal.
	legacy := *c.Admin.GarmManagement
	legacy.DiscoveryOnly = false
	legacy.PoolIDs = []string{testManagedPool}
	c.Admin.GarmManagement = &legacy
	if err := checkStaticConfig(c, false); err == nil {
		t.Fatal("legacy preflight no longer requires independent journal lineage")
	}
	legacy.Journal, legacy.JournalLineage = journal, strings.Repeat("c", 64)
	if err := checkStaticConfig(c, false); err != nil {
		t.Fatal("valid legacy preflight changed", err)
	}
	if requests.Load() != 0 || connections.Load() != 0 {
		t.Fatal("configuration checks touched transport", requests.Load(), connections.Load())
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 3 {
		t.Fatal("configuration checks created private DB/journal state", err)
	}
}
