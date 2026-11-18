package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"testing"
)

func TestManagedReductionRoutingDoesNotAuthorizeOtherAPIs(t *testing.T) {
	pool := "11111111-1111-4111-8111-111111111111"
	m := &garmManagementConfiguration{ManagedSupervisor: true, ControllerID: "controller", ProxyServerKeyPin: "pin", Endpoint: "https://127.0.0.1:9445", Providers: []string{"capacity"}, PoolIDs: []string{pool}}
	c := configuration{Controller: "controller", Pools: map[string]gateway.PoolPolicy{pool: {}}, Admin: &adminConfiguration{GarmManagement: m}, Garm: garmConfiguration{ManagedProxy: true, ProxyServerKeyPin: "pin", Endpoint: m.Endpoint, Providers: []string{"capacity"}}}
	if e := validateManagedSettingsRouting(c); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(){func() { m.ControllerID = "foreign" }, func() { m.ProxyServerKeyPin = "foreign" }, func() { m.Endpoint = "https://127.0.0.1:9446" }, func() { m.PoolIDs = []string{"unknown"} }, func() { m.Providers = []string{"foreign"} }} {
		copy := *m
		change()
		if validateManagedSettingsRouting(c) == nil {
			t.Fatal("foreign managed source accepted")
		}
		*m = copy
	}
	for _, change := range []func(){func() { m.TokenFile = "private-stock-secret" }, func() { m.Journal = "second-journal" }, func() { m.QualifiedAdmissionRouting = true }, func() { m.ExclusiveSettingsWriter = true }, func() { m.RepositoryIDs = []string{"repo"} }, func() { m.ExperimentalConditionalSettings = true }} {
		copy := *m
		change()
		if _, e := managedSettingsClient(c.Admin); e == nil {
			t.Fatal("broad managed configuration accepted")
		}
		*m = copy
	}
}
