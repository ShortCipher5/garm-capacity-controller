package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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

//go:embed testdata/pinned_garm_preparation_manager.gotmpl
var actualPreparationManager string

//go:embed testdata/pinned_garm_preparation_wire.gotmpl
var actualPreparationSource string

type actualSourceManifest struct {
	Endpoint, Controller, Pool, Entity, Forge string
	Generation                                uint64
}
type actualSourcePublished struct {
	ID, Name   string
	Agent      int64
	Generation uint64
}
type actualSourceCommandResult struct {
	output []byte
	err    error
}

func preparationWireFile(t *testing.T, path string, target any, completed <-chan actualSourceCommandResult, finished *bool) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			if target != nil && json.Unmarshal(raw, target) != nil {
				t.Fatal("invalid disposable source coordination")
			}
			return
		}

		select {
		case result := <-completed:
			if finished != nil {
				*finished = true
			}
			t.Fatalf("source process ended before fixture coordination: %v\n%s", result.err, result.output)
		default:
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatal("disposable source coordination unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func preparationWireMarker(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
}

// Actual official source HTTP enrollment + SQL + AddRunner/JIT + pending poll ->
// actual compiled wrapper -> runtime gateway8 -> disposable native TLS fixtures.
// Fake JIT response/JWT and fake native execution do NOT qualify real credentials,
// guest/image behavior, hardware, external writers or production deployment.
func TestPinnedGarmActualSourcePreparationWire(t *testing.T) {
	work := os.Getenv("GCC_PINNED_GARM_WORKTREE")
	if work == "" {
		t.Skip("mandatory pinned-source integration job supplies the canonical disposable source")
	}
	head, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil || !filepath.IsAbs(work) || strings.TrimSpace(string(head)) != "154638445c3949c1958b01812f69d9a1e4d82684" {
		t.Fatal("refusing unpinned source")
	}
	status, err := exec.Command("git", "-C", work, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal("source status unavailable")
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "??") {
			t.Fatal("untracked source outside canonical patch")
		}
	}
	applied, err := exec.Command("git", "-C", work, "diff", "--binary", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("../../integration/garm-v0.2.1/admission-preflight.patch")
	if err != nil || sha256.Sum256(applied) != sha256.Sum256(expected) {
		t.Fatal("source differs from canonical patch")
	}
	for _, source := range []struct{ path, body string }{{"runner/pool/capacity_wire_qualification.go", actualPreparationManager}, {"apiserver/controllers/capacity_wire_qualification_test.go", actualPreparationSource}} {
		target := filepath.Join(work, source.path)
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal("refusing to overwrite source fixture")
		}
		defer os.Remove(target)
		if _, err = file.WriteString(source.body); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if file.Close() != nil {
			t.Fatal("source fixture close failed")
		}
		if _, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "gofmt"), "-w", target).CombinedOutput(); err != nil {
			t.Fatal("source fixture format failed")
		}
	}
	for _, mode := range []string{"confirmed", "lost-sql-ack", "wrong-source-role", "known-generation-drift", "lost-record-ack"} {
		t.Run(mode, func(t *testing.T) { qualifyActualSourcePreparation(t, work, mode) })
	}
}
func qualifyActualSourcePreparation(t *testing.T, work, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	directory := t.TempDir()
	if os.Chmod(directory, 0700) != nil {
		t.Fatal("private fixture directory")
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	binary := filepath.Join(directory, "capacity-provider")
	if output, err := exec.CommandContext(ctx, goBinary, "build", "-race", "-buildvcs=false", "-o", binary, "../capacity-provider").CombinedOutput(); err != nil {
		t.Fatalf("actual wrapper build: %v %s", err, output)
	}
	keys := credentials(t)
	command := exec.CommandContext(ctx, goBinary, "test", "-mod=vendor", "-tags", "testing,capacity_wire_qualification", "-race", "-count=1", "-timeout=3m", "-run", "^TestCapacityActualSourcePreparationWire$", "./apiserver/controllers")
	command.Dir = work
	command.Env = append(os.Environ(), "GCC_SOURCE_WIRE_DIRECTORY="+directory, "GCC_SOURCE_WIRE_MODE="+mode, "GCC_SOURCE_WIRE_BINARY="+binary, "GCC_SOURCE_WIRE_CERT="+keys.serverFiles.Certificate, "GCC_SOURCE_WIRE_KEY="+keys.serverFiles.Key, "GCC_SOURCE_WIRE_CA="+keys.serverFiles.CA)
	completed := make(chan actualSourceCommandResult, 1)
	go func() { output, err := command.CombinedOutput(); completed <- actualSourceCommandResult{output, err} }()
	childFinished := false
	defer func() {
		cancel()
		if !childFinished {
			select {
			case <-completed:
			case <-time.After(15 * time.Second):
				t.Error("source fixture failed to stop")
			}
		}
	}()
	var manifest actualSourceManifest
	preparationWireFile(t, filepath.Join(directory, "manifest.json"), &manifest, completed, &childFinished)
	if manifest.Controller == "" || manifest.Pool == "" || manifest.Endpoint == "" {
		t.Fatal("incomplete actual source manifest")
	}
	var nativeWrites, waitChecks atomic.Int32
	var mu sync.Mutex
	var name string
	var nativeConfig map[string]string
	const operation = "12345678-1234-1234-1234-123456789abc"
	databasePath := filepath.Join(directory, "ledger.db")
	native := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project") != "runners" {
			t.Error("unexpected native project")
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			nativeWrites.Add(1)
			if r.Method != http.MethodPost || r.URL.Path != "/1.0/instances" {
				t.Error("unexpected native mutation")
				w.WriteHeader(503)
				return
			}
		}
		switch {
		case r.URL.Path == "/1.0":
			syncJSON(w, incusIdentityRoot(keys, "actual-source-native"))
		case r.URL.Path == "/1.0/instances" && r.Method == http.MethodPost:
			var body struct {
				Name   string            `json:"name"`
				Config map[string]string `json:"config"`
				Start  bool              `json:"start"`
				Source map[string]string `json:"source"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || !body.Start || body.Config["limits.cpu"] != "1" || body.Config["limits.memory"] != "1024MiB" || body.Config["limits.memory.enforce"] != "hard" || body.Source["fingerprint"] != strings.Repeat("a", 64) || nativeConfig != nil {
				t.Error("actual source physical request invalid/duplicated")
				w.WriteHeader(409)
				return
			}
			name = body.Name
			nativeConfig = body.Config
			nativeConfig["volatile.uuid"] = "11111111-1111-4111-8111-111111111111"
			nativeConfig["volatile.uuid.generation"] = "22222222-2222-4222-8222-222222222222"
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation + "?project=runners", "metadata": map[string]any{"id": operation}})
		case r.URL.Path == "/1.0/instances":
			rows := []any{}
			if nativeConfig != nil {
				rows = append(rows, map[string]any{"name": name, "status_code": 103, "expanded_config": nativeConfig})
			}
			syncJSON(w, rows)
		case r.URL.Path == "/1.0/operations":
			syncJSON(w, map[string]any{})
		case r.URL.Path == "/1.0/operations/"+operation+"/wait":
			observed, err := admission.OpenSQLite(databasePath, admission.Config{})
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			key := sha256.Sum256([]byte(manifest.Controller + "\x00" + manifest.Pool + "\x00" + name))
			dispatch, err := observed.LookupDispatchReadOnly(ctx, hex.EncodeToString(key[:]))
			original, receiptErr := observed.LookupOriginalPreparation(ctx, hex.EncodeToString(key[:]))
			observed.Close()
			if err != nil || receiptErr != nil || dispatch.State != "submitted" || dispatch.OriginalPreparationSequence != original.RecordSequence || original.Original.AgentID != 42 || original.Original.PoolID != manifest.Pool {
				t.Error("wait preceded exact durable source receipt/submission")
				w.WriteHeader(500)
				return
			}
			waitChecks.Add(1)
			syncJSON(w, map[string]any{"id": operation, "status_code": 200, "resources": map[string][]string{"instances": {"/1.0/instances/" + name + "?project=runners"}}})
		case r.URL.Path == "/1.0/operations/"+operation:
			syncJSON(w, map[string]any{"id": operation, "class": "task", "description": "Creating instance", "status_code": 200, "status": "Success", "err": "", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "resources": map[string][]string{"instances": {"/1.0/instances/" + name + "?project=runners"}}, "metadata": map[string]any{"private": "source-fixture-must-not-be-journaled"}})
		case name != "" && r.URL.Path == "/1.0/instances/"+name+"/state":
			syncJSON(w, map[string]any{"status_code": 103})
		case name != "" && r.URL.Path == "/1.0/instances/"+name:
			syncJSON(w, map[string]any{"name": name, "status_code": 103, "created_at": "2026-01-01T00:00:00Z", "expanded_config": nativeConfig})
		default:
			http.NotFound(w, r)
		}
	}))
	token := filepath.Join(directory, "inventory.token")
	if os.WriteFile(token, []byte("disposable-source-inventory"), 0600) != nil {
		t.Fatal("fixture token unavailable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	providerHash, sourceHash := sha256.Sum256(keys.provider.Certificate[0]), sha256.Sum256(keys.other.Certificate[0])
	sourceActor := hex.EncodeToString(sourceHash[:])
	routeGeneration := manifest.Generation
	if mode == "known-generation-drift" {
		routeGeneration++
	}
	jit := true
	budget := admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}
	config := configuration{Listen: address, Controller: manifest.Controller, Database: databasePath, BootstrapMode: "gateway-owned-default-cloudinit/v1", PreparationPools: map[string]gateway.PreparationPoolPolicy{manifest.Pool: {ProviderName: "capacity", EntityType: "repository", EntityID: manifest.Entity, ForgeEndpointName: manifest.Forge, Generation: &routeGeneration, JITEnabled: &jit, SourceFingerprints: []string{sourceActor}}}, SourceFingerprints: []string{sourceActor}, ProviderFingerprints: []string{hex.EncodeToString(providerHash[:])}, InitialPolicy: admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{manifest.Pool: {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: 30 * time.Second}, Pools: map[string]gateway.PoolPolicy{manifest.Pool: {Host: "a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}}, Hosts: map[string]hostConfiguration{"a": {Endpoint: native.URL, Project: "runners", TLS: keys.otherFiles}}, Garm: garmConfiguration{Endpoint: manifest.Endpoint, TokenFile: token, TLS: keys.otherFiles, Providers: []string{"capacity"}}, ServerTLS: keys.serverFiles}
	initializeRuntimeFixture(t, &config)
	serviceCtx, stopService := context.WithCancel(ctx)
	serviceDone := make(chan error, 1)
	go func() { serviceDone <- serveOnListener(serviceCtx, config, true, listener) }()
	defer func() {
		stopService()
		select {
		case err := <-serviceDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(35 * time.Second):
			t.Error("gateway fixture shutdown timed out")
		}
	}()
	tlsConfig, err := securefiles.LoadTLS(keys.providerFiles)
	if err != nil {
		t.Fatal(err)
	}
	client, err := gatewayrpc.NewClient("https://"+address, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	until := time.Now().Add(10 * time.Second)
	for {
		if _, err = client.List(ctx, manifest.Pool); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("runtime gateway startup failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	wrapperEndpoint := "https://" + address
	var appliedRecords, droppedACKs, cancellationRequests atomic.Int32
	if mode == "lost-record-ack" {
		// Test-only authenticated fault relay. Preserve the incoming leaf's exact
		// identity when forwarding; it grants no role unavailable at the gateway.
		providerTLS, err := securefiles.LoadTLS(keys.providerFiles)
		if err != nil {
			t.Fatal(err)
		}
		sourceTLS, err := securefiles.LoadTLS(keys.otherFiles)
		if err != nil {
			t.Fatal(err)
		}
		clients := map[string]*http.Client{
			hex.EncodeToString(providerHash[:]): {Transport: &http.Transport{TLSClientConfig: providerTLS, Proxy: nil}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
			sourceActor:                         {Transport: &http.Transport{TLSClientConfig: sourceTLS, Proxy: nil}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		}
		for _, client := range clients {
			defer client.CloseIdleConnections()
		}
		relay := mutualServer(t, keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/cancel-admission" {
				cancellationRequests.Add(1)
			}
			if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
				w.WriteHeader(401)
				return
			}
			peer := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
			forward := clients[hex.EncodeToString(peer[:])]
			if forward == nil {
				w.WriteHeader(403)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
			if err != nil || len(body) > 1<<20 {
				w.WriteHeader(400)
				return
			}
			request, err := http.NewRequestWithContext(r.Context(), r.Method, "https://"+address+r.URL.RequestURI(), bytes.NewReader(body))
			if err != nil {
				w.WriteHeader(503)
				return
			}
			request.Header = r.Header.Clone()
			response, err := forward.Do(request)
			if err != nil {
				w.WriteHeader(503)
				return
			}
			defer response.Body.Close()
			data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			if err != nil || len(data) > 1<<20 {
				w.WriteHeader(503)
				return
			}
			if r.URL.Path == "/v1/record-preparation" && response.StatusCode == http.StatusOK {
				// The REAL handler returned only after durable receipt/audit commit.
				// Drop every client byte instead of inventing a before-commit error.
				appliedRecords.Add(1)
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Error("fault relay cannot drop ACK")
					w.WriteHeader(503)
					return
				}
				connection, _, err := hijacker.Hijack()
				if err != nil {
					t.Error("fault relay ACK drop failed")
					return
				}
				droppedACKs.Add(1)
				_ = connection.Close()
				return
			}
			for key, values := range response.Header {
				w.Header()[key] = values
			}
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(data)
		}))
		wrapperEndpoint = relay.URL
	}

	sourceFiles := keys.otherFiles
	if mode == "wrong-source-role" {
		sourceFiles = keys.providerFiles
	}
	raw, err := json.Marshal(map[string]any{"gateway": wrapperEndpoint, "controller": manifest.Controller, "tls": keys.providerFiles, "source_tls": sourceFiles, "bootstrap_mode": "gateway-owned-default-cloudinit/v1"})
	if err != nil || os.WriteFile(filepath.Join(directory, "provider.json"), raw, 0600) != nil {
		t.Fatal("private provider configuration unavailable")
	}
	preparationWireMarker(t, filepath.Join(directory, "gateway-ready"))
	store, err := admission.OpenRuntimeSQLite(config.Database, config.IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var published actualSourcePublished
	if mode != "wrong-source-role" && mode != "known-generation-drift" && mode != "lost-record-ack" {
		preparationWireFile(t, filepath.Join(directory, "published.json"), &published, completed, &childFinished)
		// AddRunner is held AFTER real SQL commit, BEFORE its caller sees an ACK.
		// Force a fresh complete collection of the genuine positive-agent API row.
		until = time.Now().Add(20 * time.Second)
		for {
			state, readErr := store.ReadSnapshot(ctx)
			verified := false
			for _, event := range state.Audit {
				if event.Kind == "garm_preparation_verified" {
					verified = true
				}
			}
			if readErr == nil && state.Ready && verified && len(state.Inventory) == 0 && state.Allocated.Global.Runners == 1 {
				break
			}
			if time.Now().After(until) {
				t.Fatal("actual positive-agent SQL row blocked readiness or lost charge")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if nativeWrites.Load() != 0 {
			t.Fatal("physical create preceded source poll/SQL ACK coordination")
		}
		preparationWireMarker(t, filepath.Join(directory, "allow-poll"))
	}
	result := <-completed
	childFinished = true
	if result.err != nil {
		t.Fatalf("actual source-to-wrapper integration failed: %v\n%s", result.err, result.output)
	}
	var outcome map[string]any
	preparationWireFile(t, filepath.Join(directory, "result.json"), &outcome, nil, nil)
	state, err := store.ReadSnapshot(ctx)
	if mode == "known-generation-drift" {
		if err != nil || state.Allocated.Global.Runners != 0 || len(state.Reservations) != 0 || state.CancellationCount != 0 || nativeWrites.Load() != 0 || outcome["deferred"] != true || outcome["jit_calls"] != float64(0) {
			t.Fatal("known source drift consumed capacity/JIT or wrote a row")
		}
		return
	}
	if err != nil || state.Allocated.Global.Runners != 1 || len(state.Reservations) != 1 || state.CancellationCount != 0 {
		t.Fatal("source outcome lost reservation charge/history")
	}
	for key := range state.Reservations {
		receipt, receiptErr := store.LookupOriginalPreparation(ctx, key)
		dispatch, dispatchErr := store.LookupDispatchReadOnly(ctx, key)
		if mode == "wrong-source-role" {
			if !errors.Is(receiptErr, sql.ErrNoRows) || !errors.Is(dispatchErr, sql.ErrNoRows) || nativeWrites.Load() != 0 || outcome["rejected"] != true {
				t.Fatal("ordinary provider role minted/wrote/released")
			}
			continue
		}
		if mode == "lost-record-ack" {
			if receiptErr != nil || receipt.Original.AgentID != 42 || receipt.Source.ClientFingerprint != sourceActor || receipt.Source.Role != "source" || !errors.Is(dispatchErr, sql.ErrNoRows) || appliedRecords.Load() != 1 || droppedACKs.Load() != 1 || cancellationRequests.Load() != 0 || nativeWrites.Load() != 0 || outcome["rejected"] != true || outcome["deferred"] != false {
				t.Fatal("committed-but-lost Record ACK was retried, published, released or cleaned up")
			}
			t.Log("verified committed-then-lost Record ACK: one commit/drop, zero SQL rows/native mutations/cancellation/registration cleanup; full charge retained")
			continue
		}

		if receiptErr != nil || dispatchErr != nil || dispatch.State != "confirmed" || dispatch.OriginalPreparationSequence != receipt.RecordSequence || receipt.Original.InstanceID != published.ID || receipt.Original.AgentID != 42 || receipt.Source.ClientFingerprint != sourceActor || nativeWrites.Load() != 1 || waitChecks.Load() != 1 {
			t.Fatal("actual source lost receipt/binding/one-shot correlation")
		}
		evidence, err := store.LookupCreateEvidence(ctx, key)
		if err != nil {
			t.Fatal("actual source create evidence unavailable")
		}
		encoded, _ := json.Marshal([]any{receipt, evidence})
		if strings.Contains(string(encoded), "disposable-source-bootstrap-token") || strings.Contains(string(encoded), "source-fixture-must-not-be-journaled") {
			t.Fatal("private source/native payload persisted")
		}
	}
	if outcome["jit_calls"] != float64(1) {
		t.Fatal("source repeated JIT acquisition")
	}
}
