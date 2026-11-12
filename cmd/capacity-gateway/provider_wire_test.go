package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	common "github.com/cloudbase/garm-provider-common/params"
)

//go:embed testdata/pinned_garm_provider_wire.gotmpl
var pinnedGarmWireTest string

// This opt-in qualification is REQUIRED by the pinned-source CI job, not the
// fast unit suite. The worktree must be the exact public Garm source+our patch.
func TestPinnedGarmCompiledProviderWire(t *testing.T) {
	work := os.Getenv("GCC_PINNED_GARM_WORKTREE")
	if work == "" {
		t.Skip("run integration/garm-v0.2.1/test.sh for actual pinned-source qualification")
	}
	if !filepath.IsAbs(work) {
		t.Fatal("qualification requires absolute disposable source path")
	}
	head, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != "154638445c3949c1958b01812f69d9a1e4d82684" {
		t.Fatal("refusing unpinned source")
	}
	generated := filepath.Join(work, "runner/providers/v0.1.1/capacity_gateway_wire_test.go")
	source, err := os.OpenFile(generated, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("qualification refuses overwriting a source file")
	}
	defer os.Remove(generated)
	if _, err := source.WriteString(pinnedGarmWireTest); err != nil {
		source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	gofmt := filepath.Join(runtime.GOROOT(), "bin", "gofmt")
	if output, err := exec.Command(gofmt, "-w", generated).CombinedOutput(); err != nil {
		t.Fatalf("format qualification source: %v %s", err, output)
	}
	for _, mode := range []string{"confirmed", "unknown"} {
		t.Run(mode, func(t *testing.T) { qualifyPinnedProviderWire(t, work, goBinary, mode) })
	}
}

func qualifyPinnedProviderWire(t *testing.T, work, goBinary, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	directory := t.TempDir()
	binary := filepath.Join(directory, "capacity-provider")
	// Disposable fixture binary metadata is not provenance. HEAD is verified
	// separately; avoid an enclosing synthetic .git confusing linked worktrees.
	if output, err := exec.CommandContext(ctx, goBinary, "build", "-race", "-buildvcs=false", "-o", binary, "../capacity-provider").CombinedOutput(); err != nil {
		t.Fatalf("compile actual provider: %v %s", err, output)
	}
	keys := credentials(t)
	var mu sync.Mutex
	var creates atomic.Int32
	var submittedChecks atomic.Int32
	databasePath := filepath.Join(directory, "ledger.db")
	instances := map[string]map[string]string{}
	const operation = "12345678-1234-1234-1234-123456789abc"
	incus := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project") != "runners" {
			t.Error("wrong bounded project")
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/1.0":
			syncJSON(w, map[string]any{"api_version": "1.0", "auth": "trusted", "environment": map[string]any{"server_clustered": false}, "api_extensions": []string{"instance_create_start"}})
		case r.URL.Path == "/1.0/instances" && r.Method == "POST":
			creates.Add(1)
			var body struct {
				Name   string            `json:"name"`
				Config map[string]string `json:"config"`
				Start  bool              `json:"start"`
				Source map[string]string `json:"source"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Config["limits.cpu"] != "1" || body.Config["limits.memory"] != "1024MiB" || body.Config["limits.memory.enforce"] != "hard" || !body.Start || body.Source["fingerprint"] != strings.Repeat("a", 64) || body.Config["user.garm-capacity.reservation"] == "" {
				t.Error("actual provider failed explicit limits/correlation")
			}
			if _, exists := instances[body.Name]; exists {
				w.WriteHeader(409)
				return
			}
			instances[body.Name] = body.Config
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation + "?project=runners", "metadata": map[string]any{"id": operation}})
		case r.URL.Path == "/1.0/instances":
			rows := []any{}
			for name, config := range instances {
				rows = append(rows, map[string]any{"name": name, "status_code": 103, "expanded_config": config})
			}
			syncJSON(w, rows)
		case r.URL.Path == "/1.0/operations":
			syncJSON(w, map[string]any{})
		case r.URL.Path == "/1.0/operations/"+operation+"/wait":
			// This HTTP wait request must not be sent before the received
			// operation receipt is durable in a separately opened ledger.
			observed, err := admission.OpenSQLite(databasePath, admission.Config{})
			if err != nil {
				t.Error(err)
				http.Error(w, "ledger unavailable", 500)
				return
			}
			requestKey := sha256.Sum256([]byte("controller-test\x00pool-a\x00wire-1"))
			receipt, err := observed.LookupDispatch(context.Background(), hex.EncodeToString(requestKey[:]))
			observed.Close()
			if err != nil || receipt.State != "submitted" || receipt.OperationID != operation || receipt.ResourceID != "wire-1" {
				t.Errorf("wait preceded durable submission %+v %v", receipt, err)
				http.Error(w, "submission unavailable", 500)
				return
			}
			submittedChecks.Add(1)

			if mode == "unknown" {
				http.Error(w, "private-unknown-fault", http.StatusServiceUnavailable)
				return
			}
			syncJSON(w, map[string]any{"id": operation, "status_code": 200, "resources": map[string][]string{"instances": {"/1.0/instances/wire-1?project=runners"}}})
		case r.URL.Path == "/1.0/instances/wire-1/state":
			syncJSON(w, map[string]any{"status_code": 103})
		case r.URL.Path == "/1.0/instances/wire-1":
			config, exists := instances["wire-1"]
			if !exists {
				http.NotFound(w, r)
				return
			}
			syncJSON(w, map[string]any{"name": "wire-1", "status_code": 103, "expanded_config": config})
		default:
			http.NotFound(w, r)
		}
	}))
	garm := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances" {
			t.Error("wrong actual Garm inventory route")
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	token := filepath.Join(directory, "garm.token")
	if err := os.WriteFile(token, []byte("disposable-inventory-token"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	fingerprint := sha256.Sum256(keys.provider.Certificate[0])
	budget := admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}
	config := configuration{Listen: address, Controller: "controller-test", Database: databasePath, InitialPolicy: admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}, Pools: map[string]gateway.PoolPolicy{"pool-a": {Host: "a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}}, Hosts: map[string]hostConfiguration{"a": {Endpoint: incus.URL, Project: "runners", TLS: keys.otherFiles}}, Garm: garmConfiguration{Endpoint: garm.URL, TokenFile: token, TLS: keys.otherFiles, Providers: []string{"capacity"}}, ServerTLS: keys.serverFiles, ProviderFingerprints: []string{hex.EncodeToString(fingerprint[:])}}
	running, cancelService := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serveOnListener(running, config, true, listener) }()
	defer func() {
		cancelService()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("qualification service shutdown %v", err)
			}
		case <-time.After(35 * time.Second):
			t.Error("qualification service failed to stop")
		}
	}()
	clientTLS, err := securefiles.LoadTLS(keys.providerFiles)
	if err != nil {
		t.Fatal(err)
	}
	client, err := gatewayrpc.NewClient("https://"+address, clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		check, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		_, err = client.List(check, "pool-a")
		stop()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual gateway failed to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	providerConfiguration := struct {
		Gateway    string               `json:"gateway"`
		Controller string               `json:"controller"`
		TLS        securefiles.TLSFiles `json:"tls"`
	}{"https://" + address, "controller-test", keys.providerFiles}
	raw, err := json.Marshal(providerConfiguration)
	if err != nil {
		t.Fatal(err)
	}
	providerPath := filepath.Join(directory, "provider.local.json")
	if err := os.WriteFile(providerPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, goBinary, "test", "-mod=vendor", "-tags", "testing", "-race", "-count=1", "-timeout=3m", "-run", "^TestCapacityGatewayCompiledProviderWire$", "./runner/providers/v0.1.1")
	command.Dir = work
	command.Env = append(os.Environ(), "GCC_WIRE_PROVIDER_BINARY="+binary, "GCC_WIRE_PROVIDER_CONFIG="+providerPath, "GCC_WIRE_OUTCOME="+mode)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("actual pinned Garm/executable protocol failed: %v\n%s", err, output)
	}
	t.Logf("actual pinned Garm external provider -> compiled facade -> gateway -> Incus TLS fixtures passed: %s", output)
	store, err := admission.OpenSQLite(config.Database, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requestKey := sha256.Sum256([]byte("controller-test\x00pool-a\x00wire-1"))
	receipt, err := store.LookupDispatch(ctx, hex.EncodeToString(requestKey[:]))
	if err != nil || receipt.State != mode || receipt.OperationID != operation || receipt.ResourceID != "wire-1" {
		t.Fatalf("lost durable dispatch correlation: %+v %v", receipt, err)
	}
	if submittedChecks.Load() != 1 {
		t.Fatalf("submission-before-wait checks=%d", submittedChecks.Load())
	}
	unusedKey := sha256.Sum256([]byte("controller-test\x00pool-a\x00wire-unused"))
	cancelled, err := store.LookupCancellation(ctx, hex.EncodeToString(unusedKey[:]))
	if err != nil || cancelled.Request.Key != hex.EncodeToString(unusedKey[:]) {
		t.Fatalf("lost durable cancellation %+v %v", cancelled, err)
	}
	if _, err := store.LookupDispatch(ctx, hex.EncodeToString(unusedKey[:])); err == nil {
		t.Fatal("cancelled identity dispatched")

	}
	if creates.Load() != 1 {
		t.Fatalf("physical creates=%d; expected exactly one despite confirmed replay", creates.Load())
	}
}
