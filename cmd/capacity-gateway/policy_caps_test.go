package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	common "github.com/cloudbase/garm-provider-common/params"
)

func TestPolicyCapStartupExplicitRestrictedConfiguration(t *testing.T) {
	stop, e := startPolicyCapReconciler(context.Background(), configuration{}, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	stop()
	for _, seconds := range []int{-1, 1, 60, 3601} {
		c := configuration{Admin: &adminConfiguration{GarmManagement: &garmManagementConfiguration{PolicyCapIntervalSeconds: seconds}}}
		if _, e = startPolicyCapReconciler(context.Background(), c, nil, nil); e == nil {
			t.Fatal("unqualified startup", seconds)
		}
	}
}

func TestStaticConfigCheckMatchesManagedPolicyCapIntervalGate(t *testing.T) {
	keys := credentials(t)
	dir := t.TempDir()
	token := func(name, value string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const controller, pool = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	const endpoint = "https://127.0.0.1:1"
	pin := strings.Repeat("c", 64)
	c := configuration{
		Listen: "127.0.0.1:8443", Controller: controller, Database: filepath.Join(dir, "must-not-exist.db"),
		IdentityFile: filepath.Join(dir, "must-not-exist.identity"), ServerTLS: keys.serverFiles,
		ProviderFingerprints: []string{strings.Repeat("e", 64)},
		Pools: map[string]gateway.PoolPolicy{
			pool: {Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64},
		},
		Hosts: map[string]hostConfiguration{"host-a": {Endpoint: "https://incus.invalid", Project: "runners", TLS: keys.otherFiles}},
		Garm:  garmConfiguration{ManagedProxy: true, ProxyServerKeyPin: pin, Endpoint: endpoint, TLS: keys.otherFiles, Providers: []string{"capacity"}},
		Admin: &adminConfiguration{
			Listen: "127.0.0.1:8444", TLS: keys.serverFiles, TokenFile: token("owner.token", strings.Repeat("a", 64)),
			ManagementTokenFile: token("write.token", strings.Repeat("b", 64)), EnableExperimentalManagement: true, EnableExperimentalGarmManagement: true,
			GarmManagement: &garmManagementConfiguration{
				ManagedSupervisor: true, ControllerID: controller, ProxyServerKeyPin: pin, Endpoint: endpoint,
				TLS: keys.otherFiles, Providers: []string{"capacity"}, PoolIDs: []string{pool},
			},
		},
	}
	m := c.Admin.GarmManagement
	for _, interval := range []int{0, 60, 3600, -1, 1, 59, 3601} {
		m.PolicyCapIntervalSeconds = interval
		valid := interval == 0 || interval >= 60 && interval <= 3600
		if err := checkStaticConfig(c, false); (err == nil) != valid {
			t.Fatalf("static configuration accepted the wrong cap interval shape (%d): %v", interval, err)
		}
	}
	m.PolicyCapIntervalSeconds = 60
	for name, change := range map[string]func(){
		"owner write opt-in absent":   func() { c.Admin.EnableExperimentalManagement = false },
		"stock write opt-in absent":   func() { c.Admin.EnableExperimentalGarmManagement = false },
		"supervisor not selected":     func() { m.ManagedSupervisor = false },
		"wrong supervisor controller": func() { m.ControllerID = pool },
	} {
		t.Run(name, func(t *testing.T) {
			oldAdmin, oldM := *c.Admin, *m
			defer func() { *c.Admin, *m = oldAdmin, oldM }()
			change()
			if err := validatePolicyCapConfiguration(c); err == nil {
				t.Fatal("invalid selected cap shape accepted")
			}
			if err := checkStaticConfig(c, false); err == nil {
				t.Fatal("static entry point accepted invalid cap shape")
			}
		})
	}
	for _, path := range []string{c.Database, c.IdentityFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("static cap check created runtime state", err)
		}
	}
}
