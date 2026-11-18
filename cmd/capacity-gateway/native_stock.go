package main

import (
	"context"
	"crypto/tls"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
)

type nativeStockHostConfiguration struct {
	DomainBindingSequence int64                `json:"domain_binding_sequence"`
	Policy                nativeapi.PolicySpec `json:"policy"`
	OwnerEndpoint         string               `json:"owner_endpoint"`
	OwnerTLS              securefiles.TLSFiles `json:"owner_tls"`
	OwnerClientLeaf       string               `json:"owner_client_leaf"`
	OwnerServerLeaf       string               `json:"owner_server_leaf"`
}
type nativeStockPoolConfiguration struct {
	MetadataURL string                                  `json:"metadata_url"`
	GitHubURL   string                                  `json:"github_url"`
	Hosts       map[string]nativeStockHostConfiguration `json:"hosts"`
}

// buildNativeStock wires real implementations, but no flag or configuration
// substitutes for production authority. Active runtime stores are refused before
// reading owner key files or contacting metadata/native owners.
func buildNativeStock(ctx context.Context, store *admission.SQLiteStore, c configuration, token string, trust *tls.Config) (*gateway.NativeStock, func(), error) {
	closeAll := func() {}
	if len(c.NativeStock) == 0 {
		return nil, closeAll, nil
	}
	if !c.EnableStockProvider || store == nil || store.RuntimeLineageBound() {
		return nil, closeAll, gatewayrpc.ErrConfiguration
	}
	if trust == nil {
		var e error
		trust, e = securefiles.LoadTLS(c.Garm.TLS)
		if e != nil {
			return nil, closeAll, gatewayrpc.ErrConfiguration
		}
	}
	scopes := map[string]gatewayrpc.StockScope{}
	for _, scope := range c.StockProviderScopes {
		if _, duplicate := scopes[scope.PoolID]; duplicate {
			return nil, closeAll, gatewayrpc.ErrConfiguration
		}
		scopes[scope.PoolID] = scope
	}
	if len(scopes) != len(c.NativeStock) {
		return nil, closeAll, gatewayrpc.ErrConfiguration
	}
	clients := []*stockjit.Client{}
	cleanup := func() {
		for _, client := range clients {
			client.Close()
		}
	}
	pools := map[string]gateway.NativeStockPool{}
	for id, configured := range c.NativeStock {
		scope, ok := scopes[id]
		if !ok || scope.Controller != c.Controller {
			cleanup()
			return nil, closeAll, gatewayrpc.ErrConfiguration
		}
		metadata, e := stockjit.New(stockjit.Policy{MetadataURL: configured.MetadataURL, ControllerID: c.Controller, PoolID: id, GitHubURL: configured.GitHubURL}, trust)
		if e != nil {
			cleanup()
			return nil, closeAll, gatewayrpc.ErrConfiguration
		}
		clients = append(clients, metadata)
		pool := gateway.NativeStockPool{Metadata: metadata, Ownership: stockidentity.Configuration{Endpoint: c.Garm.Endpoint, Token: token, Provider: scope.ProviderName, Pool: id, TLS: trust}, Hosts: map[string]nativecontroller.Configuration{}}
		for host, config := range configured.Hosts {
			policy, e := nativeapi.NewPolicy(config.Policy)
			if e != nil || !policy.Protected() || policy.Controller() != c.Controller || policy.Host() != host {
				cleanup()
				return nil, closeAll, gatewayrpc.ErrConfiguration
			}
			credentials, e := securefiles.LoadTLS(config.OwnerTLS)
			if e != nil {
				cleanup()
				return nil, closeAll, gatewayrpc.ErrConfiguration
			}
			pool.Hosts[host] = nativecontroller.Configuration{Store: store, Policy: policy, DomainBindingSequence: config.DomainBindingSequence, Owner: nativeidentity.Configuration{Endpoint: config.OwnerEndpoint, TLS: credentials, DomainID: policy.DomainID(), PolicyDigest: policy.Digest(), OwnerClientLeaf: config.OwnerClientLeaf, OwnerServerLeaf: config.OwnerServerLeaf}}
		}
		pools[id] = pool
	}
	facade, e := gateway.NewNativeStock(ctx, store, c.Controller, pools)
	if e != nil {
		cleanup()
		return nil, closeAll, gatewayrpc.ErrConfiguration
	}
	return facade, cleanup, nil
}
