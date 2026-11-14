package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	common "github.com/cloudbase/garm-provider-common/params"
)

func TestStaticConfigCheckIsLocalAndDoesNotCreateDatabase(t *testing.T) {
	keys := credentials(t)
	dir := t.TempDir()
	token := filepath.Join(dir, "garm.token")
	if e := os.WriteFile(token, []byte("fixture-token"), 0600); e != nil {
		t.Fatal(e)
	}
	db := filepath.Join(dir, "must-not-exist.db")
	c := configuration{Listen: "127.0.0.1:8443", Controller: "controller-test", Database: db, IdentityFile: filepath.Join(dir, "must-not-exist.identity"), Pools: map[string]gateway.PoolPolicy{"pool-a": {Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}}, Hosts: map[string]hostConfiguration{"host-a": {Endpoint: "https://incus.invalid", Project: "runners", TLS: keys.otherFiles}}, Garm: garmConfiguration{Endpoint: "https://garm.invalid", TokenFile: token, TLS: keys.otherFiles, Providers: []string{"capacity"}}, ServerTLS: keys.serverFiles, ProviderFingerprints: []string{strings.Repeat("b", 64)}}
	if e := checkStaticConfig(c, false); e != nil {
		t.Fatal("valid scoped static config refused", e)
	}
	if _, e := os.Stat(db); !os.IsNotExist(e) {
		t.Fatal("preflight touched database", e)
	}
	if _, e := os.Stat(c.IdentityFile); !os.IsNotExist(e) {
		t.Fatal("preflight touched identity", e)
	}
	c.Listen = "0.0.0.0:8443"
	if checkStaticConfig(c, false) == nil {
		t.Fatal("non-loopback accepted")
	}
	c.Listen = "127.0.0.1:8443"
	c.Listen = "127.0.0.1:notaport"
	if checkStaticConfig(c, false) == nil {
		t.Fatal("invalid port accepted")
	}
	c.Listen = "127.0.0.1:8443"
	p := c.Pools["pool-a"]
	p.Arch = "unsupported"
	c.Pools["pool-a"] = p
	if checkStaticConfig(c, false) == nil {
		t.Fatal("unsupported arch accepted")
	}
	p.Arch = common.Amd64
	c.Pools["pool-a"] = p
	c.PreparationPools = map[string]gateway.PreparationPoolPolicy{"wrong-pool": {}}
	if checkStaticConfig(c, false) == nil {
		t.Fatal("invalid preparation routing accepted")
	}
	c.PreparationPools = nil
	c.SourceFingerprints = []string{strings.Repeat("b", 64)}
	if checkStaticConfig(c, false) == nil {
		t.Fatal("overlapping provider/source role accepted")
	}
}
