package main

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"time"
)

// Configuration requests lifecycle supervision, not permission to allocate.
// Runtime native/management authority is deliberately unavailable in this
// version; a requested service reports blocked without issuing live grants.
type allocatorLoopConfiguration struct {
	RepositoryIDs   []string `json:"repository_ids,omitempty"`
	IntervalSeconds int      `json:"interval_seconds"`
	GrantTTLSeconds int      `json:"grant_ttl_seconds"`
}

func validateAllocatorLoop(c *allocatorLoopConfiguration) error {
	if c == nil {
		return nil
	}
	if c.IntervalSeconds < 60 || c.IntervalSeconds > 3600 || c.GrantTTLSeconds < 60 || c.GrantTTLSeconds > 3600 {
		return brokerinput.ErrRuntimeAuthority
	}
	return nil
}
func startAllocatorLoop(ctx context.Context, c *allocatorLoopConfiguration, store *admission.SQLiteStore) (*brokerinput.Service, func(), error) {
	return startAllocatorLoopSource(ctx, c, store, nil)
}
func startAllocatorLoopSource(ctx context.Context, c *allocatorLoopConfiguration, store *admission.SQLiteStore, source *garmapi.DemandClient) (*brokerinput.Service, func(), error) {
	noop := func() {}
	if c == nil {
		return nil, noop, nil
	}
	if validateAllocatorLoop(c) != nil || ctx == nil || store == nil || !store.RuntimeLineageBound() {
		return nil, noop, brokerinput.ErrRuntimeAuthority
	}
	service, err := brokerinput.NewService(store, source, capacitybroker.Options{MaxAge: time.Minute, MaxAssignments: 1000}, time.Duration(c.GrantTTLSeconds)*time.Second, time.Duration(c.IntervalSeconds)*time.Second)
	if err != nil {
		return nil, noop, err
	}
	// Run synchronously to record the actual blocked startup before serving status.
	// This branch cannot become a background grant writer by setting a config bit.
	// When genuine authority is implemented it must replace this explicit boundary,
	// not silently convert a previously blocked installation into a live writer.
	err = service.Run(ctx)
	if !errors.Is(err, brokerinput.ErrRuntimeAuthority) {
		return nil, noop, err
	}
	return service, noop, nil
}

// managedAllocatorSource constructs a real read-only transport without observing,
// activating or writing. Its identity stays empty until an authenticated read.
func managedAllocatorSource(c configuration) (*garmapi.DemandClient, error) {
	if c.AllocatorLoop == nil || len(c.AllocatorLoop.RepositoryIDs) == 0 {
		return nil, nil
	}
	if !c.Garm.ManagedProxy || c.Garm.TokenFile != "" {
		return nil, brokerinput.ErrRuntimeAuthority
	}
	trust, err := securefiles.LoadTLS(c.Garm.TLS)
	if err != nil {
		return nil, err
	}
	return garmapi.NewManagedDemand(c.Garm.Endpoint, c.Garm.ProxyServerKeyPin, c.Controller, trust, c.Garm.Providers, c.AllocatorLoop.RepositoryIDs)
}
func startConfiguredAllocatorLoop(ctx context.Context, c configuration, store *admission.SQLiteStore) (*brokerinput.Service, func(), error) {
	source, err := managedAllocatorSource(c)
	if err != nil {
		return nil, func() {}, err
	}
	service, stop, err := startAllocatorLoopSource(ctx, c.AllocatorLoop, store, source)
	if err != nil {
		if source != nil {
			source.Close()
		}
		return nil, func() {}, err
	}
	return service, func() {
		stop()
		if source != nil {
			source.Close()
		}
	}, nil
}
