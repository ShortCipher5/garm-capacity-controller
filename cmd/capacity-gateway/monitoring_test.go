package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	common "github.com/cloudbase/garm-provider-common/params"
)

// TestMonitoringOptInCollectorLifecycleWiresSameBackends starts the service
// twice against fixture Incus/Garm servers: with the monitoring opt-in the
// collector performs its baseline sweep (a /1.0/resources read, which no
// other component issues) and stops cleanly on shutdown; without the opt-in
// no monitoring read ever happens.
func TestMonitoringOptInCollectorLifecycleWiresSameBackends(t *testing.T) {
	for _, monitoring := range []bool{true, false} {
		t.Run(map[bool]string{true: "opt-in-sweeps-and-stops", false: "default-no-monitoring-reads"}[monitoring], func(t *testing.T) {
			keys := credentials(t)
			var resourceReads atomic.Int32
			var inventoryReads atomic.Int32
			incus := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("project") != "runners" {
					t.Error("wrong physical scope")
				}
				switch r.URL.Path {
				case "/1.0":
					syncJSON(w, incusIdentityRoot(keys, "monitoring-incus"))
				case "/1.0/resources":
					resourceReads.Add(1)
					syncJSON(w, map[string]any{"cpu": map[string]any{"total": 8}, "memory": map[string]any{"total": 32 << 30}, "disk": []any{map[string]any{"size": 1 << 40, "usage": 1 << 30}}})
				case "/1.0/instances":
					if r.URL.Query().Get("recursion") != "1" {
						t.Error("incomplete inventory query")
					}
					inventoryReads.Add(1)
					syncJSON(w, []any{})
				case "/1.0/operations":
					syncJSON(w, map[string]any{})
				default:
					http.NotFound(w, r)
				}
			}))
			garm := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || (r.URL.Path != "/api/v1/instances" && r.URL.Path != "/api/v1/pools/pool-a") || r.Header.Get("Authorization") != "Bearer disposable-monitoring-token" {
					t.Error("wrong Garm route/auth")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if r.URL.Path == "/api/v1/pools/pool-a" {
					syncJSON(w, sourcePoolMetadataFixture("pool-a"))
					return
				}
				_, _ = w.Write([]byte(`[]`))
			}))
			directory := t.TempDir()
			token := filepath.Join(directory, "garm.token")
			if err := os.WriteFile(token, []byte("disposable-monitoring-token"), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(keys.provider.Certificate[0])
			budget := admission.Resources{CPU: 2000, RAM: 4096, Runners: 2}
			config := configuration{Listen: listener.Addr().String(), Controller: "controller-test", Database: filepath.Join(directory, "ledger.db"), InitialPolicy: admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: 3 * time.Second}, Pools: map[string]gateway.PoolPolicy{"pool-a": {Host: "a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}}, Hosts: map[string]hostConfiguration{"a": {Endpoint: incus.URL, Project: "runners", TLS: keys.otherFiles}}, Garm: garmConfiguration{Endpoint: garm.URL, TokenFile: token, TLS: keys.otherFiles, Providers: []string{"capacity"}}, ServerTLS: keys.serverFiles, ProviderFingerprints: []string{hex.EncodeToString(hash[:])}}
			initializeRuntimeFixture(t, &config)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- serveOnListener(ctx, config, false, monitoring, listener) }()
			waitFor := func(reads *atomic.Int32, want int32) {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for reads.Load() < want {
					if time.Now().After(deadline) {
						cancel()
						t.Fatalf("service never reached %d reads (got %d)", want, reads.Load())
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			waitFor(&inventoryReads, 1)
			if monitoring {
				waitFor(&resourceReads, 1)
			}
			// Wait until the startup reconciliation has durably completed, so
			// cancellation cannot abort it and surface as a startup error.
			state, err := admission.OpenSQLite(config.Database, admission.Config{})
			if err != nil {
				t.Fatal(err)
			}
			readyDeadline := time.Now().Add(10 * time.Second)
			for {
				snapshot, err := state.Snapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Ready {
					break
				}
				if time.Now().After(readyDeadline) {
					state.Close()
					cancel()
					t.Fatal("reconciliation never completed in fixture")
				}
				time.Sleep(20 * time.Millisecond)
			}
			state.Close()
			observed := resourceReads.Load()
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("shutdown %v", err)
				}
			case <-time.After(35 * time.Second):
				t.Fatal("service did not stop")
			}
			time.Sleep(300 * time.Millisecond)
			if later := resourceReads.Load(); later != observed {
				t.Fatalf("collector kept sweeping after shutdown: %d -> %d", observed, later)
			}
			if !monitoring && observed != 0 {
				t.Fatalf("monitoring reads happened without the opt-in: %d", observed)
			}
		})
	}
}
