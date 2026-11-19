package main

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"time"
)

// This configuration is independent of allocator_loop: no lifecycle transition
// or grant writer is reachable through this reader.
type allocatorAdvisoryConfiguration struct {
	RepositoryIDs   []string `json:"repository_ids"`
	IntervalSeconds int      `json:"interval_seconds"`
}

func configuredAllocatorAdvisory(ctx context.Context, c configuration, store *admission.SQLiteStore) (*brokerinput.AdvisoryLoop, func(), error) {
	noop := func() {}
	if c.AllocatorAdvisory == nil {
		return nil, noop, nil
	}
	source, err := allocatorAdvisorySource(c)
	if err != nil {
		return nil, noop, err
	}
	reader, err := brokerinput.NewAdvisoryReader(store, source)
	if err != nil {
		source.Close()
		return nil, noop, err
	}
	loop, err := brokerinput.StartAdvisoryLoop(ctx, reader, time.Duration(c.AllocatorAdvisory.IntervalSeconds)*time.Second)
	if err != nil {
		source.Close()
		return nil, noop, err
	}
	return loop, func() { loop.Close(); source.Close() }, nil
}

// Local-only constructor: does not Observe, poll, open ledger, or grant authority.
func allocatorAdvisorySource(c configuration) (*garmapi.DemandClient, error) {
	if c.AllocatorAdvisory == nil {
		return nil, nil
	}
	if c.AllocatorAdvisory.IntervalSeconds < 15 || c.AllocatorAdvisory.IntervalSeconds > 60 || c.Admin == nil || len(c.AllocatorAdvisory.RepositoryIDs) == 0 || !c.Garm.ManagedProxy || c.Garm.TokenFile != "" {
		return nil, brokerinput.ErrRuntimeAuthority
	}
	trust, err := securefiles.LoadTLS(c.Garm.TLS)
	if err != nil {
		return nil, err
	}
	source, err := garmapi.NewManagedDemand(c.Garm.Endpoint, c.Garm.ProxyServerKeyPin, c.Controller, trust, c.Garm.Providers, c.AllocatorAdvisory.RepositoryIDs)
	if err != nil {
		return nil, err
	}
	return source, nil
}
