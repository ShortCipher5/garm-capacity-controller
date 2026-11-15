package adminview

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// PoolDiscoveryReader grants no setting mutation capability. It can observe
// existing pools without enrolling targets or initializing a management journal.
type PoolDiscoveryReader interface {
	DiscoverPools(context.Context) (garmapi.PoolDiscovery, error)
}
type poolDiscoveryView struct{ reader PoolDiscoveryReader }

func NewPoolDiscoveryView(reader PoolDiscoveryReader) (GarmManager, error) {
	if reader == nil {
		return nil, ErrConfiguration
	}
	return &poolDiscoveryView{reader: reader}, nil
}
func (v *poolDiscoveryView) Snapshot(ctx context.Context) (GarmOverview, error) {
	inventory, e := v.reader.DiscoverPools(ctx)
	if e != nil {
		return GarmOverview{}, e
	}
	return GarmOverview{DiscoveryOnly: true, History: []GarmChangeEvent{}, Settings: garmapi.ManagementSnapshot{Pools: []garmapi.ManagedPool{}, Repositories: []garmapi.ManagedRepository{}}, PoolInventory: &inventory}, nil
}
func (*poolDiscoveryView) UpdatePool(context.Context, string, string, string, garmapi.PoolUpdate, string) (garmapi.ManagedPool, error) {
	return garmapi.ManagedPool{}, garmapi.ErrManagementDisabled
}
func (*poolDiscoveryView) UpdateRepositoryBalancer(context.Context, string, string, string, string, string) (garmapi.ManagedRepository, error) {
	return garmapi.ManagedRepository{}, garmapi.ErrManagementDisabled
}
