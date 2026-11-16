package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// Each collection is an independent complete observation. Unsupported or failed
// scaleset discovery is nil/unavailable, never an invented empty success.
func observeScaleSets(ctx context.Context, reader any) *garmapi.ScaleSetDiscovery {
	if ctx == nil || ctx.Err() != nil {
		return nil
	}
	remote, ok := reader.(interface {
		DiscoverScaleSets(context.Context) (garmapi.ScaleSetDiscovery, error)
	})
	if !ok {
		return nil
	}
	ctx, cancel := boundedPoolDiscoveryContext(ctx)
	defer cancel()
	inventory, err := remote.DiscoverScaleSets(ctx)
	if err != nil || ctx.Err() != nil {
		return nil
	}
	return &inventory
}

// Optional scalesets must not evict essential settings or durable journal status
// from the existing bounded overview. Omit the whole optional observation; never
// truncate a collection or fabricate successful emptiness.
func withBoundedScaleSetInventory(view GarmOverview) GarmOverview {
	if view.ScaleSetInventory == nil {
		return view
	}
	data, err := json.Marshal(view)
	if err != nil || len(data) > 2<<20 {
		view.ScaleSetInventory = nil
	}
	return view
}
