package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"strings"
	"testing"
)

func TestStockScopeConfigurationIndependentAndExact(t *testing.T) {
	c := configuration{Controller: "c", Pools: map[string]gateway.PoolPolicy{"p": {}}, Garm: garmConfiguration{Providers: []string{"provider"}}, ProviderFingerprints: []string{strings.Repeat("a", 64)}, SourceFingerprints: []string{strings.Repeat("b", 64)}}
	if validateStockProviderScopes(c) != nil {
		t.Fatal("disabled default failed")
	}
	c.EnableStockProvider = true
	if validateStockProviderScopes(c) == nil {
		t.Fatal("enabled without scopes")
	}
	c.StockProviderScopes = []gatewayrpc.StockScope{{Controller: "c", PoolID: "p", ProviderName: "provider", ClientFingerprint: strings.Repeat("c", 64)}}
	if validateStockProviderScopes(c) != nil {
		t.Fatal("valid explicit scope rejected")
	}
	for _, fp := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		c.StockProviderScopes[0].ClientFingerprint = fp
		if validateStockProviderScopes(c) == nil {
			t.Fatal("identity reused")
		}
	}
	c.StockProviderScopes[0].ClientFingerprint = strings.Repeat("c", 64)
	c.StockProviderScopes[0].PoolID = "other"
	if validateStockProviderScopes(c) == nil {
		t.Fatal("unknown pool")
	}
}
