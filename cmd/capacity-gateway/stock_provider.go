package main

import (
	"encoding/hex"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"net/http"
)

func validateStockProviderScopes(c configuration) error {
	if c.EnableStockProvider && len(c.StockProviderScopes) == 0 {
		return gatewayrpc.ErrConfiguration
	}
	occupied := map[string]bool{}
	for _, fp := range append(append([]string{}, c.ProviderFingerprints...), c.SourceFingerprints...) {
		occupied[fp] = true
	}
	providers := map[string]bool{}
	for _, p := range c.Garm.Providers {
		providers[p] = true
	}
	for _, s := range c.StockProviderScopes {
		raw, e := hex.DecodeString(s.ClientFingerprint)
		_, pool := c.Pools[s.PoolID]
		if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != s.ClientFingerprint || occupied[s.ClientFingerprint] || s.Controller != c.Controller || !pool || !providers[s.ProviderName] {
			return gatewayrpc.ErrConfiguration
		}
		occupied[s.ClientFingerprint] = true
	}
	return nil
}
func stockProviderHandler(c configuration, base *gatewayrpc.Handler, native ...*gateway.NativeStock) (http.Handler, error) {
	if e := validateStockProviderScopes(c); e != nil {
		return nil, e
	}
	if !c.EnableStockProvider {
		if len(c.NativeStock) > 0 {
			return nil, gatewayrpc.ErrConfiguration
		}
		return base, nil
	}
	var stock *gatewayrpc.StockHandler
	var e error
	if len(c.NativeStock) > 0 {
		if len(native) != 1 || native[0] == nil {
			return nil, gatewayrpc.ErrConfiguration
		}
		stock, e = gatewayrpc.NewNativeStockHandler(base, native[0], c.StockProviderScopes)
	} else {
		if len(native) > 1 || (len(native) == 1 && native[0] != nil) {
			return nil, gatewayrpc.ErrConfiguration
		}
		stock, e = gatewayrpc.NewStockHandler(base, c.StockProviderScopes)
	}
	if e != nil {
		return nil, e
	}
	// Exact path dispatch only. Stock credentials have no access to original
	// preparation or administrative APIs, including through the fallback handler.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stock/create" {
			stock.ServeHTTP(w, r)
		} else {
			base.ServeHTTP(w, r)
		}
	}), nil
}
