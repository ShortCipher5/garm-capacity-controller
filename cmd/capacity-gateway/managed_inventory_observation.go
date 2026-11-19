package main

import (
	"context"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
)

const (
	managedInventoryReadTimeout = 5 * time.Second
	managedInventoryInterval    = 15 * time.Second
	managedInventoryFreshness   = 30 * time.Second
)

func managedInventoryObservationAllowed(c configuration, provision bool) bool {
	if !c.ManagedInventoryObservation {
		return true
	}
	return !provision && c.Garm.ManagedProxy && c.Garm.TokenFile == "" && c.Garm.ProxyServerKeyPin != "" &&
		c.Admin != nil && c.Admin.GarmManagement == nil && !c.Admin.EnableExperimentalGarmManagement &&
		!c.Admin.EnableExperimentalManagement && c.AllocatorLoop == nil && !c.EnableStockProvider &&
		len(c.NativeStock) == 0 && c.NativeClaims == nil && c.NativeRetirement == nil &&
		!c.EnableNativeRetirement && len(c.PreparationPools) == 0
}

// The observer keeps only a scoped count and start time in memory. It accepts
// the existing pinned managed inventory client's read method, not a store.
type managedInventoryObserver struct {
	ctx  context.Context
	read func(context.Context) ([]gateway.GarmInstance, error)

	mu        sync.RWMutex
	sampledAt time.Time
	count     int
}

func (o *managedInventoryObserver) sample() {
	at := time.Now().UTC()
	ctx, cancel := context.WithTimeout(o.ctx, managedInventoryReadTimeout)
	defer cancel()
	rows, err := o.read(ctx)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sampledAt, o.count = time.Time{}, 0
	if err == nil && ctx.Err() == nil {
		o.sampledAt, o.count = at, len(rows)
	}
}

func (o *managedInventoryObserver) run() {
	for o.ctx.Err() == nil {
		o.sample()
		if o.ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(managedInventoryInterval)
		select {
		case <-o.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (o *managedInventoryObserver) snapshot(now time.Time) adminview.ManagedInventoryObservation {
	o.mu.RLock()
	defer o.mu.RUnlock()
	at, count := o.sampledAt, o.count
	if o.ctx.Err() != nil || at.IsZero() || at.After(now) || !at.Add(managedInventoryFreshness).After(now) {
		return adminview.ManagedInventoryObservation{}
	}
	return adminview.ManagedInventoryObservation{Available: true, SampledAt: &at, Count: &count}
}
