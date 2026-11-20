//go:build linux && stock_read_pilot_harness

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
)

func TestStockPilotOverlayMatchesActualInventoryConfiguration(t *testing.T) {
	raw, err := os.ReadFile("../../internal/managedgarm/testdata/stock-pilot-gateway-overlay.json")
	if err != nil {
		t.Fatal("missing shared synthetic overlay fixture")
	}
	// Parse the very same bytes into the actual command configuration type.
	// Admin is intentionally supplied by the independently pinned base,
	// never by the overlay. No static file loader or server is invoked here.
	cfg := configuration{Admin: &adminConfiguration{}}
	if gatewayrpc.Decode(bytes.NewReader(raw), &cfg) != nil || !cfg.ManagedInventoryObservation ||
		!managedInventoryObservationAllowed(cfg, false) || managedInventoryObservationAllowed(cfg, true) ||
		cfg.Garm.Endpoint != "https://127.0.0.1:9445" || len(cfg.Garm.Providers) != 1 || cfg.Garm.Providers[0] != "capacity" ||
		cfg.Garm.ProxyServerKeyPin != strings.Repeat("a", 64) ||
		cfg.Garm.TLS.CA != "/run/capacity-private/gateway-read-ca.pem" || cfg.Garm.TLS.Certificate != "/run/capacity-private/gateway-read-client.pem" ||
		cfg.Garm.TLS.Key != "/run/capacity-private/gateway-read-client.key" || cfg.Garm.TokenFile != "" {
		t.Fatal("pilot overlay drifted from actual read-only gateway configuration")
	}
}
