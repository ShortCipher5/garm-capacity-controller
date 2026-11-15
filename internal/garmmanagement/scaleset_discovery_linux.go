package garmmanagement

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"time"
)

// Read-only collection observation does not expand any management target scope.
func (m *Manager) DiscoverScaleSets(ctx context.Context) (garmapi.ScaleSetDiscovery, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return garmapi.ScaleSetDiscovery{}, err
	}
	defer m.leave()
	remote, ok := m.remote.(interface {
		DiscoverScaleSets(context.Context) (garmapi.ScaleSetDiscovery, error)
	})
	if !ok {
		return garmapi.ScaleSetDiscovery{}, garmapi.ErrManagement
	}
	return remote.DiscoverScaleSets(ctx)
}
