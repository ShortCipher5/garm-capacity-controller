package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}
func TestDiscoveryAndDestructiveCommandsNeverLoadCredentials(t *testing.T) {
	for command, want := range map[string]string{"GetVersion": "experimental", "GetConfigJSONSchema": "gateway", "GetSupportedInterfaceVersions": "v0.1.1", "GetExtraSpecsJSONSchema": "additionalProperties"} {
		var output bytes.Buffer
		exit := run(context.Background(), environment(map[string]string{"GARM_COMMAND": command}), strings.NewReader(""), &output)
		if exit != 0 || !strings.Contains(output.String(), want) {
			t.Fatalf("%s: %d %s", command, exit, &output)
		}
	}
	for _, command := range []string{"DeleteInstance", "RemoveAllInstances", "StartInstance", "StopInstance", "ValidatePoolInfo", "unknown"} {
		var output bytes.Buffer
		exit := run(context.Background(), environment(map[string]string{"GARM_COMMAND": command, "GARM_INTERFACE_VERSION": "v0.1.1"}), strings.NewReader("secret"), &output)
		if exit != 1 || output.Len() != 0 {
			t.Fatalf("unsafe unsupported command %s", command)
		}
	}
}
func TestProviderNeverPrintsInvalidPrivateConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.json")
	if err := os.WriteFile(path, []byte(`{"gateway":"https://example.invalid","controller":"controller-a","incus_private_key":"secret-must-not-print"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"AdmitInstance", "CancelAdmission", "CreateInstance", "GetInstance", "ListInstances"} {
		var output bytes.Buffer
		exit := run(context.Background(), environment(map[string]string{"GARM_COMMAND": command, "GARM_INTERFACE_VERSION": "v0.1.1", "GARM_PROVIDER_CONFIG_FILE": path, "GARM_CONTROLLER_ID": "controller-a", "GARM_POOL_ID": "pool-a"}), strings.NewReader(`{"secret":"must-not-print"}`), &output)
		if exit != 1 || output.Len() != 0 {
			t.Fatalf("invalid config leaked %d %s", exit, &output)
		}
	}
}

func TestExecutableWireEnvironmentAndAdmissionExitCode(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || (r.URL.Path != "/v1/admit" && r.URL.Path != "/v1/cancel-admission") {
			t.Error("wrong command route")
		}
		if r.URL.Path == "/v1/cancel-admission" {
			_, _ = w.Write([]byte(`{"protocol":"garm-capacity/v1","outcome":"cancelled","name":"runner-1","pool_id":"pool-a","dispatched":false,"reservation_key":"key-1","replay":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"exit_code":75,"response":{"protocol":"garm-capacity/v1","outcome":"deferred","name":"runner-1","pool_id":"pool-a","dispatched":false,"retry_after_seconds":10}}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	key, err := x509.MarshalPKCS8PrivateKey(server.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := configuration{Gateway: server.URL, Controller: "controller-a", TLS: securefiles.TLSFiles{CA: write("ca.pem", cert), Certificate: write("client.pem", cert), Key: write("client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := write("provider.json", raw)
	env := map[string]string{"GARM_COMMAND": "AdmitInstance", "GARM_INTERFACE_VERSION": "v0.1.1", "GARM_PROVIDER_CONFIG_FILE": path, "GARM_CONTROLLER_ID": "controller-a", "GARM_POOL_ID": "pool-a"}
	var output bytes.Buffer
	exit := run(context.Background(), environment(env), strings.NewReader(`{"protocol":"garm-capacity/v1","name":"runner-1","pool_id":"pool-a"}`), &output)
	if exit != 75 || !strings.Contains(output.String(), `"dispatched":false`) || requests.Load() != 1 {
		t.Fatalf("wire defer %d %s requests%d", exit, &output, requests.Load())
	}
	output.Reset()
	env["GARM_COMMAND"] = "CancelAdmission"
	if exit = run(context.Background(), environment(env), strings.NewReader(`{"protocol":"garm-capacity/v1","name":"runner-1","pool_id":"pool-a"}`), &output); exit != 0 || !strings.Contains(output.String(), `"outcome":"cancelled"`) || requests.Load() != 2 {
		t.Fatalf("wire cancellation %d %s", exit, &output)
	}
	output.Reset()
	env["GARM_CONTROLLER_ID"] = "wrong-controller"
	if exit = run(context.Background(), environment(env), strings.NewReader("{}"), &output); exit != 1 || output.Len() != 0 || requests.Load() != 2 {
		t.Fatal("controller mismatch reached gateway")
	}
}

func TestPreparationCommandsSelectOnlyExplicitSourceCredentialsAndExactBootstrapMode(t *testing.T) {
	var requests atomic.Int32
	original := garmprotocol.PreparationRequest{Protocol: garmprotocol.PreparationVersion, Name: "runner-1", PoolID: "pool-a", InstanceID: "11111111-1111-4111-8111-111111111111", ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/record-preparation" && r.URL.Path != "/v1/validate-preparation" {
			t.Error("wrong source route")
		}
		no, yes := false, true
		outcome := "recorded"
		replay := false
		if r.URL.Path == "/v1/validate-preparation" {
			outcome = "validated"
			replay = yes
		}
		response := garmprotocol.PreparationResponse{PreparationRequest: original, Outcome: outcome, ReservationKey: strings.Repeat("a", 64), RecordSequence: 1, Replay: &replay, Dispatched: &no}
		raw, err := garmprotocol.EncodePreparationResponse(response, original)
		if err != nil {
			t.Error(err)
		}
		w.Write(raw)
	}))
	defer server.Close()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	key, err := x509.MarshalPKCS8PrivateKey(server.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	valid := securefiles.TLSFiles{CA: write("ca.pem", certificate), Certificate: write("client.pem", certificate), Key: write("client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}
	invalid := securefiles.TLSFiles{CA: filepath.Join(dir, "never-read-provider-ca"), Certificate: filepath.Join(dir, "never-read-provider-cert"), Key: filepath.Join(dir, "never-read-provider-key")}
	config := configuration{Gateway: server.URL, Controller: "controller-a", TLS: invalid, SourceTLS: &valid, BootstrapMode: "gateway-owned-default-cloudinit/v1"}
	configPath := filepath.Join(dir, "provider.json")
	save := func() {
		raw, _ := json.Marshal(config)
		if err := os.WriteFile(configPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	raw, _ := json.Marshal(original)
	env := map[string]string{"GARM_COMMAND": "RecordPreparation", "GARM_INTERFACE_VERSION": "v0.1.1", "GARM_PROVIDER_CONFIG_FILE": configPath, "GARM_CONTROLLER_ID": "controller-a", "GARM_POOL_ID": "pool-a"}
	for _, command := range []string{"RecordPreparation", "ValidatePreparation"} {
		env["GARM_COMMAND"] = command
		var output bytes.Buffer
		if exit := run(context.Background(), environment(env), bytes.NewReader(raw), &output); exit != 0 {
			t.Fatalf("source command %s exit%d", command, exit)
		}
		outcome := "recorded"
		if command == "ValidatePreparation" {
			outcome = "validated"
		}
		if _, err := garmprotocol.ParsePreparationResponse(0, output.Bytes(), original, outcome); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 2 {
		t.Fatal("source commands did not reach expected TLS route")
	}
	for _, kind := range []string{"missing-source", "wrong-mode", "missing-mode", "pool-mismatch", "missing-explicit-zero", "duplicate-field"} {
		t.Run(kind, func(t *testing.T) {
			config.SourceTLS = &valid
			config.BootstrapMode = "gateway-owned-default-cloudinit/v1"
			env["GARM_POOL_ID"] = "pool-a"
			body := raw
			switch kind {
			case "missing-source":
				config.SourceTLS = nil
			case "wrong-mode":
				config.BootstrapMode = "silently-ignore-custom-template"
			case "missing-mode":
				config.BootstrapMode = ""
			case "pool-mismatch":
				env["GARM_POOL_ID"] = "pool-b"
			case "missing-explicit-zero":
				body = []byte(strings.Replace(string(raw), `,"generation":0`, "", 1))
			case "duplicate-field":
				body = append([]byte(`{"generation":0,`), raw[1:]...)
			}
			save()
			var output bytes.Buffer
			if exit := run(context.Background(), environment(env), bytes.NewReader(body), &output); exit != 1 || output.Len() != 0 || requests.Load() != 2 {
				t.Fatalf("invalid source command reached wire or leaked output exit%d output%s", exit, &output)
			}
		})
	}
}
