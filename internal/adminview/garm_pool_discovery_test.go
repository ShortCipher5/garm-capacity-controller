package adminview

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"testing"
)

type discoveryOnlyFixture struct{ calls int }

func (f *discoveryOnlyFixture) DiscoverPools(context.Context) (garmapi.PoolDiscovery, error) {
	f.calls++
	return garmapi.PoolDiscovery{Pools: []garmapi.DiscoveredPool{{ManagedPool: garmapi.ManagedPool{ID: "existing"}, Configured: false}}, MissingConfiguredPoolIDs: []string{}}, nil
}
func TestZeroEnrolledPoolDiscoveryHasNoWriteCapability(t *testing.T) {
	f := &discoveryOnlyFixture{}
	v, e := NewPoolDiscoveryView(f)
	if e != nil {
		t.Fatal(e)
	}
	s, e := v.Snapshot(context.Background())
	if e != nil || s.PoolInventory == nil || len(s.PoolInventory.Pools) != 1 || s.WritesEnabled || s.Settings.WritesEnabled || s.SettingsAvailable {
		t.Fatal("invalid read only view", e)
	}
	if _, e = v.UpdatePool(context.Background(), "", "", "", garmapi.PoolUpdate{}, ""); e != garmapi.ErrManagementDisabled {
		t.Fatal("write enabled")
	}
	if _, e = v.UpdateRepositoryBalancer(context.Background(), "", "", "", "", ""); e != garmapi.ErrManagementDisabled {
		t.Fatal("write enabled")
	}
	if f.calls != 1 {
		t.Fatal("write consulted remote")
	}
}
