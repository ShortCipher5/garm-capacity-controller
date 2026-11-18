package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"strings"
	"testing"
)

func TestManagedInventoryConfigurationRequiresNoStockCredential(t *testing.T) {
	keys := credentials(t)
	c := configuration{Garm: garmConfiguration{ManagedProxy: true, Endpoint: "https://127.0.0.1:9445", ProxyServerKeyPin: strings.Repeat("a", 64), TLS: keys.otherFiles, Providers: []string{"owned"}}}
	client, token, err := inventoryClient(c, false)
	if err != nil || len(token) != 0 {
		t.Fatalf("managed local construction: %v", err)
	}
	client.Close()
	for _, mode := range []string{"token", "pin", "provision", "preparation", "writer", "remote"} {
		t.Run(mode, func(t *testing.T) {
			copy := c
			provision := false
			switch mode {
			case "token":
				copy.Garm.TokenFile = "/must-not-be-read"
			case "pin":
				copy.Garm.ProxyServerKeyPin = ""
			case "provision":
				provision = true
			case "preparation":
				copy.PreparationPools = map[string]gateway.PreparationPoolPolicy{"pool": {}}
			case "writer":
				copy.Admin = &adminConfiguration{EnableExperimentalGarmManagement: true}
			case "remote":
				copy.Garm.Endpoint = "https://remote.invalid"
			}
			if got, _, err := inventoryClient(copy, provision); err == nil {
				got.Close()
				t.Fatal("unsupported managed configuration accepted")
			}
		})
	}
}
