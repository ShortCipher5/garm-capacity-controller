package garmmanagement

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"time"
)

// DiscoverPools is read-only even while unresolved writes block future edits.
// Legacy Remote implementations need not implement collection discovery; lack
// of support is explicit, never silently synthesized from a partial snapshot.
func (m *Manager) DiscoverPools(ctx context.Context) (garmapi.PoolDiscovery, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return garmapi.PoolDiscovery{}, err
	}
	defer m.leave()
	remote, ok := m.remote.(interface {
		DiscoverPools(context.Context) (garmapi.PoolDiscovery, error)
	})
	if !ok {
		return garmapi.PoolDiscovery{}, garmapi.ErrManagement
	}
	return remote.DiscoverPools(ctx)
}
