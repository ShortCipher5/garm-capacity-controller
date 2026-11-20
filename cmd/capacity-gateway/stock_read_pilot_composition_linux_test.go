//go:build linux && stock_read_pilot_harness

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// Synthetic fixture only. Neither verifies where independent expectations
// came from nor opens a ledger, credentials, listener or configuration path.
func stockPilotCompositionDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type stockPilotSyntheticAdminBase struct {
	Listen    string               `json:"listen"`
	TLS       securefiles.TLSFiles `json:"tls"`
	TokenFile string               `json:"token_file"`
}
type stockPilotSyntheticGatewayBase struct {
	Listen       string                        `json:"listen"`
	Controller   string                        `json:"controller"`
	Database     string                        `json:"database"`
	Identity     string                        `json:"identity_file"`
	Hosts        map[string]hostConfiguration  `json:"hosts"`
	Pools        map[string]gateway.PoolPolicy `json:"pools"`
	ServerTLS    securefiles.TLSFiles          `json:"server_tls"`
	ProviderPins []string                      `json:"provider_fingerprints"`
	Admin        stockPilotSyntheticAdminBase  `json:"admin"`
}
type stockPilotSyntheticGatewayOverlay struct {
	Observation bool `json:"managed_inventory_observation"`
	Garm        struct {
		Managed   bool                 `json:"managed_proxy"`
		Endpoint  string               `json:"endpoint"`
		Pin       string               `json:"proxy_server_key_pin"`
		Providers []string             `json:"providers"`
		TLS       securefiles.TLSFiles `json:"tls"`
	} `json:"garm"`
}

func checkStockPilotSyntheticComposition(baseRaw, overlayRaw []byte, basePin, overlayPin, serverPin, dormantProviderPin string, policy admission.Config) error {
	_, err := composeStockPilotSyntheticComposition(baseRaw, overlayRaw, basePin, overlayPin, serverPin, dormantProviderPin, policy)
	return err
}

// Return only the final bytes accepted by the real decoder and the existing
// inventory-only constraints. This helper does not place or start a fixture.
func composeStockPilotSyntheticComposition(baseRaw, overlayRaw []byte, basePin, overlayPin, serverPin, dormantProviderPin string, policy admission.Config) ([]byte, error) {
	if len(baseRaw) > 16384 || len(overlayRaw) > 16384 ||
		stockPilotCompositionDigest(baseRaw) != basePin || stockPilotCompositionDigest(overlayRaw) != overlayPin {
		return nil, errStaticConfig
	}
	for _, pin := range []string{serverPin, dormantProviderPin} {
		b, err := hex.DecodeString(pin)
		if err != nil || len(b) != 32 || hex.EncodeToString(b) != pin {
			return nil, errStaticConfig
		}
	}
	if serverPin == dormantProviderPin {
		return nil, errStaticConfig
	}
	var base stockPilotSyntheticGatewayBase
	var overlay stockPilotSyntheticGatewayOverlay
	if gatewayrpc.Decode(bytes.NewReader(baseRaw), &base) != nil || gatewayrpc.Decode(bytes.NewReader(overlayRaw), &overlay) != nil {
		return nil, errStaticConfig
	}
	const mount = "/run/capacity-private/"
	roleTLS := func(prefix string) securefiles.TLSFiles {
		return securefiles.TLSFiles{CA: mount + prefix + "-ca.pem", Certificate: mount + prefix + ".pem", Key: mount + prefix + ".key"}
	}
	host := hostConfiguration{Endpoint: "https://127.0.0.1:9553", Project: "runners", TLS: roleTLS("dormant-host")}
	pool := gateway.PoolPolicy{Host: "testhost", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: "amd64"}
	expectedBase := stockPilotSyntheticGatewayBase{
		Listen: "127.0.0.1:9443", Controller: "stock-read-pilot-only", Database: "/var/lib/capacity-gateway/ledger.db", Identity: mount + "ledger.identity",
		Hosts: map[string]hostConfiguration{"testhost": host}, Pools: map[string]gateway.PoolPolicy{"testpool": pool},
		ServerTLS: roleTLS("gateway-server"), ProviderPins: []string{dormantProviderPin},
		Admin: stockPilotSyntheticAdminBase{Listen: "127.0.0.1:9444", TLS: roleTLS("gateway-server"), TokenFile: mount + "admin.token"},
	}
	seed := admission.Config{
		Hosts: map[string]admission.Host{"testhost": {Mode: "disabled", ObservedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}},
		Pools: map[string]admission.Pool{"testpool": {Hosts: []string{"testhost"}}}, MaxObservationAge: 3 * time.Second,
	}
	if !reflect.DeepEqual(base, expectedBase) || !reflect.DeepEqual(policy, seed) || !overlay.Observation || !overlay.Garm.Managed ||
		overlay.Garm.Endpoint != "https://127.0.0.1:9445" || overlay.Garm.Pin != serverPin ||
		!reflect.DeepEqual(overlay.Garm.Providers, []string{"capacity"}) ||
		overlay.Garm.TLS != (securefiles.TLSFiles{CA: mount + "gateway-read-ca.pem", Certificate: mount + "gateway-read-client.pem", Key: mount + "gateway-read-client.key"}) {
		return nil, errStaticConfig
	}
	var merged, overlayFields map[string]json.RawMessage
	if json.Unmarshal(baseRaw, &merged) != nil || json.Unmarshal(overlayRaw, &overlayFields) != nil || len(merged) != 9 || len(overlayFields) != 2 {
		return nil, errStaticConfig
	}
	for key, value := range overlayFields {
		if _, exists := merged[key]; exists {
			return nil, errStaticConfig
		}
		merged[key] = value
	}
	final, err := json.Marshal(merged)
	var cfg configuration
	if err != nil || gatewayrpc.Decode(bytes.NewReader(final), &cfg) != nil || !cfg.ManagedInventoryObservation ||
		!managedInventoryObservationAllowed(cfg, false) || managedInventoryObservationAllowed(cfg, true) ||
		cfg.Listen != base.Listen || cfg.Controller != base.Controller || cfg.Database != base.Database || cfg.IdentityFile != base.Identity ||
		!reflect.DeepEqual(cfg.Hosts, base.Hosts) || !reflect.DeepEqual(cfg.Pools, base.Pools) || cfg.ServerTLS != base.ServerTLS ||
		!reflect.DeepEqual(cfg.ProviderFingerprints, base.ProviderPins) ||
		cfg.Admin.Listen != base.Admin.Listen || cfg.Admin.TLS != base.Admin.TLS || cfg.Admin.TokenFile != base.Admin.TokenFile ||
		cfg.Garm.Endpoint != overlay.Garm.Endpoint || cfg.Garm.ProxyServerKeyPin != overlay.Garm.Pin ||
		!reflect.DeepEqual(cfg.Garm.Providers, overlay.Garm.Providers) || cfg.Garm.TLS != overlay.Garm.TLS {
		return nil, errStaticConfig
	}
	return final, nil
}

func TestStockPilotSyntheticBaseOverlayComposition(t *testing.T) {
	base := []byte(`{
 "listen":"127.0.0.1:9443","controller":"stock-read-pilot-only","database":"/var/lib/capacity-gateway/ledger.db","identity_file":"/run/capacity-private/ledger.identity",
 "hosts":{"testhost":{"endpoint":"https://127.0.0.1:9553","project":"runners","tls":{"ca":"/run/capacity-private/dormant-host-ca.pem","certificate":"/run/capacity-private/dormant-host.pem","key":"/run/capacity-private/dormant-host.key"}}},
 "pools":{"testpool":{"host":"testhost","cpu_millis":1000,"ram_mib":1024,"flavor":"small","image":"linux","image_fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","profiles":["base"],"type":"container","arch":"amd64"}},
 "server_tls":{"ca":"/run/capacity-private/gateway-server-ca.pem","certificate":"/run/capacity-private/gateway-server.pem","key":"/run/capacity-private/gateway-server.key"},
 "provider_fingerprints":["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"],
 "admin":{"listen":"127.0.0.1:9444","tls":{"ca":"/run/capacity-private/gateway-server-ca.pem","certificate":"/run/capacity-private/gateway-server.pem","key":"/run/capacity-private/gateway-server.key"},"token_file":"/run/capacity-private/admin.token"}
}`)
	overlay, err := os.ReadFile("../../internal/managedgarm/testdata/stock-pilot-gateway-overlay.json")
	if err != nil {
		t.Fatal("missing synthetic overlay fixture")
	}
	var policy admission.Config
	if err := gatewayrpc.Decode(strings.NewReader(`{"global":{},"hosts":{"testhost":{"budget":{},"mode":"disabled","healthy":false,"observed_at":"2000-01-01T00:00:00Z"}},"pools":{"testpool":{"budget":{},"hosts":["testhost"]}},"max_observation_age_ns":3000000000}`), &policy); err != nil {
		t.Fatal("synthetic seed schema invalid")
	}
	serverPin, providerPin := strings.Repeat("a", 64), strings.Repeat("b", 64)
	basePin, overlayPin := stockPilotCompositionDigest(base), stockPilotCompositionDigest(overlay) // fixture hashes, not approved pins
	check := func(b, o []byte, bp, op string, p admission.Config) error {
		return checkStockPilotSyntheticComposition(b, o, bp, op, serverPin, providerPin, p)
	}
	if check(base, overlay, basePin, overlayPin, policy) != nil {
		t.Fatal("fixed disposable composition refused")
	}
	if check(base, overlay, serverPin, overlayPin, policy) == nil || check(base, overlay, basePin, providerPin, policy) == nil {
		t.Fatal("unmatched independent pin accepted")
	}
	for _, mut := range []struct{ from, to string }{
		{`"admin":{`, `"admin":{"management_token_file":"/unreviewed",`},
		{`"admin":{`, `"allocator_loop":{},"admin":{`},
		{`"admin":{`, `"garm":{},"admin":{`},
		{`127.0.0.1:9553`, `example.invalid:9553`},
		{`"host":"testhost"`, `"host":"other"`},
		{`ledger.identity`, `different.identity`},
	} {
		b := []byte(strings.Replace(string(base), mut.from, mut.to, 1))
		if bytes.Equal(b, base) || check(b, overlay, stockPilotCompositionDigest(b), overlayPin, policy) == nil {
			t.Fatal("changed base admitted despite closed fixture", mut.from)
		}
	}
	badOverlay := []byte(strings.Replace(string(overlay), "9445", "9444", 1))
	if check(base, badOverlay, basePin, stockPilotCompositionDigest(badOverlay), policy) == nil {
		t.Fatal("changed overlay endpoint admitted")
	}
	h := policy.Hosts["testhost"]
	h.Mode = "enabled"
	policy.Hosts["testhost"] = h
	if check(base, overlay, basePin, overlayPin, policy) == nil {
		t.Fatal("different ledger seed admitted")
	}
}
