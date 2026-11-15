package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestGarmZeroEnrolledRuntimeDiscoveryNoJournalOrWrites(t *testing.T) {
	keys := credentials(t)
	var reads atomic.Int32
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/pools" || r.Header.Get("Authorization") != "Bearer upstream-discovery-fixture" {
			t.Error("unexpected upstream action")
			w.WriteHeader(400)
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
	if reads.Load() != 1 {
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
