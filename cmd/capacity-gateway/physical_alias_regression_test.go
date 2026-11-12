package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	common "github.com/cloudbase/garm-provider-common/params"
)

// This service-boundary regression uses actual independent mTLS connections,
// private configuration files, SQLite and authenticated provider RPC. A shared
// authenticated daemon key must not manufacture independent host budgets merely
// by changing authority, project or reported name. Nothing reaches a real daemon.
func TestServiceDuplicateDaemonAliasesFailClosedBeforeDispatch(t *testing.T) {
	keys := credentials(t)
	var writes atomic.Int32
	var firstRoots, secondRoots atomic.Int32
	cert := keys.server.Certificate[0]
	certHash := sha256.Sum256(cert)
	rootHandler := func(project, reportedName string, roots *atomic.Int32) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("project") != project {
				t.Error("fixture received wrong project")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Method != http.MethodGet {
				writes.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			switch r.URL.Path {
			case "/1.0":
				roots.Add(1)
				syncJSON(w, map[string]any{
					"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"},
					"environment": map[string]any{
						"server_clustered": false, "server_name": reportedName, "server_version": "6.0.4",
						"certificate":             string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})),
						"certificate_fingerprint": hex.EncodeToString(certHash[:]),
					},
				})
			case "/1.0/instances":
				syncJSON(w, []any{})
			case "/1.0/operations":
				syncJSON(w, map[string]any{})
			default:
				http.NotFound(w, r)
			}
		})
	}
	first := mutualServer(t, keys, rootHandler("project-a", "daemon-name-a", &firstRoots))
	second := mutualServer(t, keys, rootHandler("project-b", "daemon-name-b", &secondRoots))
	if first.URL == second.URL {
		t.Fatal("regression requires distinct authorities, not duplicate endpoint text")
	}
	clientTLS := &tls.Config{RootCAs: keys.root, Certificates: []tls.Certificate{keys.other}, MinVersion: tls.VersionTLS13}
	observe := func(endpoint, project string) incusapi.PhysicalServerIdentity {
		t.Helper()
		client, err := incusapi.New(endpoint, project, clientTLS)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		identity, err := client.PhysicalIdentity(context.Background())
		if err != nil || !identity.Valid() {
			t.Fatalf("fixture did not supply authenticated identity: %v", err)
		}
		return identity
	}
	a := observe(first.URL, "project-a")
	b := observe(second.URL, "project-b")
	if a.ServerKeyFingerprint() != b.ServerKeyFingerprint() || a.OriginFingerprint() == b.OriginFingerprint() || a.ServerName() == b.ServerName() || a == b {
		t.Fatal("fixture must share only the authenticated daemon key, not full identity")
	}
	// Count service-side observations independently of this fixture self-check.
	firstRoots.Store(0)
	secondRoots.Store(0)
	garm := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || (r.URL.Path != "/api/v1/instances" && r.URL.Path != "/api/v1/pools/pool-a" && r.URL.Path != "/api/v1/pools/pool-b") || r.Header.Get("Authorization") != "Bearer disposable-alias-token" {
			t.Error("wrong Garm inventory authentication or method")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/pools/") {
			_ = json.NewEncoder(w).Encode(sourcePoolMetadataFixture(strings.TrimPrefix(r.URL.Path, "/api/v1/pools/")))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	dir := t.TempDir()
	token := filepath.Join(dir, "garm.token")
	if err := os.WriteFile(token, []byte("disposable-alias-token"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	unit := admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}
	global := admission.Resources{CPU: 2000, RAM: 2048, Runners: 2}
	policy := func(host string) gateway.PoolPolicy {
		return gateway.PoolPolicy{Host: host, CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}
	}
	providerHash := sha256.Sum256(keys.provider.Certificate[0])
	config := configuration{
		Listen: address, Controller: "controller-alias-test", Database: filepath.Join(dir, "ledger.db"),
		InitialPolicy: admission.Config{
			Global: global, MaxObservationAge: time.Minute,
			Hosts: map[string]admission.Host{
				"alias-a": {Budget: unit, Mode: "enabled", ObservedAt: time.Now()},
				"alias-b": {Budget: unit, Mode: "enabled", ObservedAt: time.Now()},
			},
			Pools: map[string]admission.Pool{
				"pool-a": {Budget: unit, Hosts: []string{"alias-a"}},
				"pool-b": {Budget: unit, Hosts: []string{"alias-b"}},
			},
		},
		Pools: map[string]gateway.PoolPolicy{"pool-a": policy("alias-a"), "pool-b": policy("alias-b")},
		Hosts: map[string]hostConfiguration{
			"alias-a": {Endpoint: first.URL, Project: "project-a", TLS: keys.otherFiles},
			"alias-b": {Endpoint: second.URL, Project: "project-b", TLS: keys.otherFiles},
		},
		Garm:      garmConfiguration{Endpoint: garm.URL, TokenFile: token, TLS: keys.otherFiles, Providers: []string{"capacity"}},
		ServerTLS: keys.serverFiles, ProviderFingerprints: []string{hex.EncodeToString(providerHash[:])},
	}
	sourceHash := sha256.Sum256(keys.other.Certificate[0])
	sourceActor := hex.EncodeToString(sourceHash[:])
	generation := uint64(0)
	jit := true
	config.BootstrapMode = "gateway-owned-default-cloudinit/v1"
	config.SourceFingerprints = []string{sourceActor}
	config.PreparationPools = map[string]gateway.PreparationPoolPolicy{}
	for id := range config.Pools {
		config.PreparationPools[id] = gateway.PreparationPoolPolicy{ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github-fixture", Generation: &generation, JITEnabled: &jit, SourceFingerprints: []string{sourceActor}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	initializeRuntimeFixture(t, &config)
	go func() { done <- serveOnListener(ctx, config, true, listener) }()
	exited := false
	t.Cleanup(func() {
		cancel()
		if exited {
			return
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("disposable service failed to stop")
		}
	})
	rpc, err := gatewayrpc.NewClient("https://"+address, &tls.Config{RootCAs: keys.root, Certificates: []tls.Certificate{keys.provider}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	probe := &http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: keys.root, Certificates: []tls.Certificate{keys.provider}, MinVersion: tls.VersionTLS13}}}
	defer probe.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			exited = true
			if err == nil {
				t.Fatal("duplicate daemon ended startup without a rejection")
			}
		default:
		}
		if exited {
			break // Rejecting the duplicate during startup is also fail-closed.
		}
		check, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
		request, err := http.NewRequestWithContext(check, http.MethodPost, "https://"+address+"/v1/list", strings.NewReader(`{"name":"","pool_id":"pool-a"}`))
		if err != nil {
			stop()
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := probe.Do(request)
		served := false
		if err == nil {
			response.Body.Close()
			// Identity failure may correctly make reads unavailable too. A real
			// authenticated 503 proves the service started without insisting on
			// an unsafe read-success fallback. Wrong-role 401/403 is NOT success.
			served = response.StatusCode == http.StatusOK || response.StatusCode == http.StatusServiceUnavailable
		}
		stop()
		if served {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service neither rejected startup nor exposed authenticated read RPC")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if firstRoots.Load() == 0 || secondRoots.Load() == 0 {
		t.Fatal("service did not authenticate BOTH independently configured daemon identities")
	}
	if !exited {
		for _, row := range []struct{ name, pool string }{{"runner-a", "pool-a"}, {"runner-b", "pool-b"}} {
			request := garmprotocol.Request{Protocol: garmprotocol.Version, Name: row.name, PoolID: row.pool}
			check, stop := context.WithTimeout(context.Background(), time.Second)
			exit, _, err := rpc.Admit(check, request)
			stop()
			if err == nil && exit != garmprotocol.DeferredExitCode {
				t.Errorf("duplicate daemon aliases obtained admission: pool=%s exit=%d", row.pool, exit)
			}
			osType, arch := "linux", "x64"
			download, filename := "https://example.invalid/disposable-runner.tar.gz", "actions-runner-linux-x64.tar.gz"
			bootstrap := common.BootstrapInstance{
				Name: row.name, PoolID: row.pool, OSType: common.Linux, OSArch: common.Amd64, Flavor: "small", Image: "linux",
				RepoURL: "https://github.com/example/disposable", CallbackURL: "https://example.invalid/callback", MetadataURL: "https://example.invalid/metadata",
				InstanceToken: "disposable-bootstrap-not-persisted", ExtraSpecs: []byte("{}"), JitConfigEnabled: true,
				Tools: []common.RunnerApplicationDownload{{OS: &osType, Architecture: &arch, DownloadURL: &download, Filename: &filename}},
			}
			check, stop = context.WithTimeout(context.Background(), time.Second)
			_, createErr := rpc.Create(check, bootstrap)
			stop()
			if createErr == nil {
				t.Error("duplicate daemon alias unexpectedly confirmed a create")
			}
		}
	}
	if writes.Load() != 0 {
		t.Errorf("duplicate physical alias reached %d backend write requests", writes.Load())
	}
	store, err := admission.OpenSQLite(config.Database, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Ready || len(state.Reservations) != 0 || len(state.Inventory) != 0 || state.Allocated.Global != (admission.Resources{}) {
		t.Fatalf("duplicate alias left actionable or charged admission state: ready=%t reservations=%d inventory=%d allocation=%+v", state.Ready, len(state.Reservations), len(state.Inventory), state.Allocated.Global)
	}
}
