package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	for _, command := range []string{"AdmitInstance", "CreateInstance", "GetInstance", "ListInstances"} {
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
		if r.Method != "POST" || r.URL.Path != "/v1/admit" {
			t.Error("wrong command route")
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
	env["GARM_CONTROLLER_ID"] = "wrong-controller"
	if exit = run(context.Background(), environment(env), strings.NewReader("{}"), &output); exit != 1 || output.Len() != 0 || requests.Load() != 1 {
		t.Fatal("controller mismatch reached gateway")
	}
}
