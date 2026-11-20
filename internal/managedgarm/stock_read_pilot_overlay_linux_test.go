//go:build linux && stock_read_pilot_harness

package managedgarm

import (
	"bytes"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// This is a closed overlay, not a complete gateway configuration. Use the
// gateway's actual decoder, with a narrower schema excluding credentials,
// writer options, listeners and any override of the independently pinned base.
func checkStockPilotGatewayOverlay(raw []byte, serverSPKI string) error {
	var overlay struct {
		Inventory bool `json:"managed_inventory_observation"`
		Garm      struct {
			ManagedProxy bool                 `json:"managed_proxy"`
			Endpoint     string               `json:"endpoint"`
			Pin          string               `json:"proxy_server_key_pin"`
			Providers    []string             `json:"providers"`
			TLS          securefiles.TLSFiles `json:"tls"`
		} `json:"garm"`
	}
	if !digest.MatchString(serverSPKI) || gatewayrpc.Decode(bytes.NewReader(raw), &overlay) != nil ||
		!overlay.Inventory || !overlay.Garm.ManagedProxy || overlay.Garm.Endpoint != "https://127.0.0.1:9445" ||
		overlay.Garm.Pin != serverSPKI || len(overlay.Garm.Providers) != 1 || overlay.Garm.Providers[0] != "capacity" ||
		overlay.Garm.TLS != (securefiles.TLSFiles{
			CA:          "/run/capacity-private/gateway-read-ca.pem",
			Certificate: "/run/capacity-private/gateway-read-client.pem",
			Key:         "/run/capacity-private/gateway-read-client.key",
		}) {
		return ErrBoundary
	}
	return nil
}
