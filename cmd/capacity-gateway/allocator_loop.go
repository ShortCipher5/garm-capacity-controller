package main

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"time"
)

// Configuration requests lifecycle supervision, not permission to allocate.
// Runtime native/management authority is deliberately unavailable in this
// version; a requested service reports blocked without issuing live grants.
type allocatorLoopConfiguration struct {
	IntervalSeconds int `json:"interval_seconds"`
	GrantTTLSeconds int `json:"grant_ttl_seconds"`
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
	noop := func() {}
	if c == nil {
		return nil, noop, nil
	}
	if validateAllocatorLoop(c) != nil || ctx == nil || store == nil || !store.RuntimeLineageBound() {
		return nil, noop, brokerinput.ErrRuntimeAuthority
	}
	service, err := brokerinput.NewService(store, nil, capacitybroker.Options{}, time.Duration(c.GrantTTLSeconds)*time.Second, time.Duration(c.IntervalSeconds)*time.Second)
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
