package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	common "github.com/cloudbase/garm-provider-common/params"
)

func TestManagedControllerEmptyPilotFixtureIsNotStartupReady(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/managed-control-plane/testdata/controller.json")
	if err != nil {
		t.Fatal(err)
	}
	var c configuration
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		t.Fatal("invalid actual controller schema")
	}
	if !c.Garm.ManagedProxy || c.Garm.TokenFile != "" || c.AllocatorLoop != nil || c.NativeClaims != nil || c.NativeRetirement != nil || len(c.PreparationPools) != 0 {
		t.Fatal("fixture enabled authority")
	}
	if len(c.Hosts) != 0 || len(c.Pools) != 0 {
		t.Fatal("fixture must not invent infrastructure")
	}
	// This real static validator rejects empty pools/hosts before opening TLS files.
	// Schema acceptance is not startup qualification. No serve() is invoked.
	// Assert only the exact empty-inventory refusal here. The independent
	// positive baseline below uses ephemeral, unattached test TLS material.
	if err := checkStaticConfig(c, false); err != errStaticConfig {
		t.Fatalf("expected empty-inventory static refusal, got %v", err)
	}
}

func TestManagedControllerStaticPositiveAndIsolatedMissingInputs(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/managed-control-plane/testdata/controller.json")
	if err != nil {
		t.Fatal(err)
	}
	var c configuration
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("fixture schema")
	}
	keys := credentials(t) // Existing helper: only fresh t.TempDir PEMs, no service.
	dir := t.TempDir()
	c.Database, c.IdentityFile = filepath.Join(dir, "not-created.db"), filepath.Join(dir, "not-created.identity")
	c.Pools = map[string]gateway.PoolPolicy{"fixture-pool": {Host: "fixture-host", CPU: 1000, RAM: 1024, Flavor: "small", Image: "fixture", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}}
	c.Hosts = map[string]hostConfiguration{"fixture-host": {Endpoint: "https://incus.invalid", Project: "fixture", TLS: keys.otherFiles}}
	c.Garm.TLS, c.ServerTLS = keys.otherFiles, keys.serverFiles
	c.Garm.Providers = []string{"fixture-provider"}
	c.ProviderFingerprints = []string{strings.Repeat("b", 64)}
	if err := checkStaticConfig(c, false); err != nil {
		t.Fatal("positive static baseline refused", err)
	}
	for _, field := range []string{"pools", "hosts", "listen", "database", "identity_file", "controller"} {
		t.Run(field, func(t *testing.T) {
			changed := c
			switch field {
			case "pools":
				changed.Pools = nil
			case "hosts":
				changed.Hosts = nil
			case "listen":
				changed.Listen = ""
			case "database":
				changed.Database = ""
			case "identity_file":
				changed.IdentityFile = ""
			case "controller":
				changed.Controller = ""
			}
			if err := checkStaticConfig(changed, false); err != errStaticConfig {
				t.Fatalf("missing %s: %v", field, err)
			}
		})
	}
	for _, path := range []string{c.Database, c.IdentityFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("static validation wrote state", err)
		}
	}
}
