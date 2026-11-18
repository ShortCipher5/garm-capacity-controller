package main

import (
	"context"
	"strings"
	"testing"
)

func TestAllocatorStartupCannotBeEnabledByConfiguration(t *testing.T) {
	for _, v := range []allocatorLoopConfiguration{{IntervalSeconds: 0, GrantTTLSeconds: 0}, {IntervalSeconds: 59, GrantTTLSeconds: 60}, {IntervalSeconds: 60, GrantTTLSeconds: 3601}, {IntervalSeconds: 3601, GrantTTLSeconds: 60}} {
		if validateAllocatorLoop(&v) == nil {
			t.Fatal("unbounded config accepted")
		}
	}
	c := &allocatorLoopConfiguration{IntervalSeconds: 60, GrantTTLSeconds: 300}
	if validateAllocatorLoop(c) != nil {
		t.Fatal("bounded config refused")
	}
	if _, _, err := startAllocatorLoop(context.Background(), c, nil); err == nil {
		t.Fatal("configuration minted runtime store")
	}
	service, close, err := startAllocatorLoop(context.Background(), nil, nil)
	if err != nil || service != nil {
		t.Fatal("absent config became active")
	}
	close()
}

func TestManagedAllocatorSourceIsConcreteButDoesNotInventIdentity(t *testing.T) {
	keys := credentials(t)
	c := configuration{Controller: "11111111-1111-4111-8111-111111111111", AllocatorLoop: &allocatorLoopConfiguration{IntervalSeconds: 60, GrantTTLSeconds: 300, RepositoryIDs: []string{"22222222-2222-4222-8222-222222222222"}}, Garm: garmConfiguration{ManagedProxy: true, Endpoint: "https://127.0.0.1:9445", ProxyServerKeyPin: strings.Repeat("a", 64), TLS: keys.otherFiles, Providers: []string{"owned"}}}
	source, err := managedAllocatorSource(c)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	if source.SourceIdentity() != "" {
		t.Fatal("configuration invented observed source")
	}
	source.Close()
	for _, mode := range []string{"token", "direct", "controller", "duplicate-repository"} {
		t.Run(mode, func(t *testing.T) {
			copy := c
			cfg := *c.AllocatorLoop
			copy.AllocatorLoop = &cfg
			switch mode {
			case "token":
				copy.Garm.TokenFile = "/no-token"
			case "direct":
				copy.Garm.ManagedProxy = false
			case "controller":
				copy.Controller = "not-a-controller-uuid"
			case "duplicate-repository":
				copy.AllocatorLoop.RepositoryIDs = []string{c.AllocatorLoop.RepositoryIDs[0], c.AllocatorLoop.RepositoryIDs[0]}
			}
			if got, err := managedAllocatorSource(copy); err == nil {
				got.Close()
				t.Fatal("invalid managed source configuration accepted")
			}
		})
	}
}
