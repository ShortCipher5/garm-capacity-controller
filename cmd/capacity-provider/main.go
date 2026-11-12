// capacity-provider speaks the opt-in Garm v0.1.1 external provider interface.
// Its private configuration contains gateway-only mTLS credentials, never Incus
// keys, Garm API tokens or database paths. Unsupported destructive commands fail.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	common "github.com/cloudbase/garm-provider-common/params"
)

type configuration struct {
	Gateway       string                `json:"gateway"`
	Controller    string                `json:"controller"`
	TLS           securefiles.TLSFiles  `json:"tls"`
	SourceTLS     *securefiles.TLSFiles `json:"source_tls,omitempty"`
	BootstrapMode string                `json:"bootstrap_mode,omitempty"`
}

func run(ctx context.Context, getenv func(string) string, input io.Reader, output io.Writer) int {
	command := getenv("GARM_COMMAND")
	switch command {
	case "GetVersion":
		fmt.Fprintln(output, "garm-capacity-controller-experimental")
		return 0
	case "GetSupportedInterfaceVersions":
		fmt.Fprintln(output, `["v0.1.1"]`)
		return 0
	case "GetConfigJSONSchema":
		schema := map[string]any{"type": "object", "required": []string{"gateway", "controller", "tls"}, "additionalProperties": false}
		tlsSchema := map[string]any{"type": "object", "required": []string{"ca", "certificate", "key"}, "additionalProperties": false, "properties": map[string]any{"ca": map[string]string{"type": "string"}, "certificate": map[string]string{"type": "string"}, "key": map[string]string{"type": "string"}}}
		schema["properties"] = map[string]any{"gateway": map[string]string{"type": "string"}, "controller": map[string]string{"type": "string"}, "tls": tlsSchema, "source_tls": tlsSchema, "bootstrap_mode": map[string]string{"type": "string", "const": "gateway-owned-default-cloudinit/v1"}}
		if json.NewEncoder(output).Encode(schema) != nil {
			return 1
		}
		return 0
	case "GetExtraSpecsJSONSchema":
		fmt.Fprintln(output, `{"type":"object","additionalProperties":false}`)
		return 0
	}
	if getenv("GARM_INTERFACE_VERSION") != "v0.1.1" {
		return 1
	}
	// Refuse unsupported commands before loading any credentials or networking.
	switch command {
	case "AdmitInstance", "CancelAdmission", "RecordPreparation", "ValidatePreparation", "CreateInstance", "GetInstance", "ListInstances":
	default:
		return 1
	}
	raw, err := securefiles.Read(getenv("GARM_PROVIDER_CONFIG_FILE"), 1<<20)
	if err != nil {
		return 1
	}
	var config configuration
	if gatewayrpc.Decode(bytes.NewReader(raw), &config) != nil || config.Controller == "" || config.Controller != getenv("GARM_CONTROLLER_ID") {
		return 1
	}
	files := config.TLS
	sourceCommand := command == "RecordPreparation" || command == "ValidatePreparation"
	if sourceCommand {
		if config.SourceTLS == nil || config.BootstrapMode != "gateway-owned-default-cloudinit/v1" {
			return 1
		}
		files = *config.SourceTLS
	}
	credentials, err := securefiles.LoadTLS(files)
	if err != nil {
		return 1
	}
	client, err := gatewayrpc.NewClient(config.Gateway, credentials)
	if err != nil {
		return 1
	}
	defer client.Close()
	pool := getenv("GARM_POOL_ID")
	if pool == "" {
		return 1
	}
	switch command {
	case "RecordPreparation", "ValidatePreparation":
		data, err := io.ReadAll(io.LimitReader(input, garmprotocol.MaximumResponseBytes+1))
		if err != nil {
			return 1
		}
		request, err := garmprotocol.DecodePreparationRequest(data)
		if err != nil || request.PoolID != pool {
			return 1
		}
		var response garmprotocol.PreparationResponse
		if command == "RecordPreparation" {
			response, err = client.RecordPreparation(ctx, request)
		} else {
			response, err = client.ValidatePreparation(ctx, request)
		}
		if err != nil {
			return 1
		}
		encoded, err := garmprotocol.EncodePreparationResponse(response, request)
		if err != nil {
			return 1
		}
		if _, err := output.Write(append(encoded, '\n')); err != nil {
			return 1
		}
		return 0
	case "AdmitInstance":
		var request garmprotocol.Request
		if gatewayrpc.Decode(input, &request) != nil || request.PoolID != pool {
			return 1
		}
		exit, data, err := client.Admit(ctx, request)
		if err != nil {
			return 1
		}
		if _, err = output.Write(data); err != nil {
			return 1
		}
		return exit
	case "CancelAdmission":
		var request garmprotocol.Request
		if gatewayrpc.Decode(input, &request) != nil || request.PoolID != pool {
			return 1
		}
		response, err := client.CancelAdmission(ctx, request)
		if err != nil {
			return 1
		}
		data, err := garmprotocol.EncodeCancellation(response, request)
		if err != nil {
			return 1
		}
		if _, err = output.Write(data); err != nil {
			return 1
		}
		return 0
	case "CreateInstance":
		var request common.BootstrapInstance
		if gatewayrpc.Decode(input, &request) != nil || request.PoolID != pool {
			return 1
		}
		instance, err := client.Create(ctx, request)
		if err != nil {
			return 1
		}
		if json.NewEncoder(output).Encode(instance) != nil {
			return 1
		}
		return 0
	case "GetInstance":
		name := getenv("GARM_INSTANCE_ID")
		if name == "" {
			return 1
		}
		instance, err := client.Get(ctx, gatewayrpc.Identity{Name: name, PoolID: pool})
		if err != nil {
			return 1
		}
		if json.NewEncoder(output).Encode(instance) != nil {
			return 1
		}
		return 0
	case "ListInstances":
		instances, err := client.List(ctx, pool)
		if err != nil {
			return 1
		}
		if json.NewEncoder(output).Encode(instances) != nil {
			return 1
		}
		return 0
	}
	return 1
}
func main() {
	code := run(context.Background(), os.Getenv, os.Stdin, os.Stdout)
	if code != 0 && code != garmprotocol.DeferredExitCode {
		fmt.Fprintln(os.Stderr, "capacity provider request unavailable")
	}
	os.Exit(code)
}
