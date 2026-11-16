package garmmanagement

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"testing"
)

type discoveryRemote struct {
	*fakeRemote
	calls int
}

func (d *discoveryRemote) DiscoverPools(context.Context) (garmapi.PoolDiscovery, error) {
	d.calls++
	return garmapi.PoolDiscovery{Pools: []garmapi.DiscoveredPool{{ManagedPool: d.pool, Configured: true}}, MissingConfiguredPoolIDs: []string{}}, nil
}
func TestManagerOptionalDiscovery(t *testing.T) {
	m, j, f, _ := setup(t)
	if _, err := m.DiscoverPools(context.Background()); err == nil {
		t.Fatal("unsupported discovery hidden")
	}
	d := &discoveryRemote{fakeRemote: f}
	m, err := New(d, j)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.DiscoverPools(context.Background())
	if err != nil || len(result.Pools) != 1 || d.calls != 1 || f.writes != 0 {
		t.Fatalf("discovery %v %+v", err, result)
	}
}
