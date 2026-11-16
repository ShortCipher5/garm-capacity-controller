package adminview

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"os"
	"path/filepath"
	"testing"
)

type inventoryRemote struct {
	boundedJournalRemote
	inventory garmapi.PoolDiscovery
}

func (r *inventoryRemote) DiscoverPools(context.Context) (garmapi.PoolDiscovery, error) {
	return r.inventory, nil
}
func TestInventorySurvivesMissingEditablePoolWithoutRecreation(t *testing.T) {
	ctx := context.Background()
	id := "00000000-0000-4000-8000-000000000001"
	remote := &inventoryRemote{boundedJournalRemote: boundedJournalRemote{unavailable: true}, inventory: garmapi.PoolDiscovery{Pools: []garmapi.DiscoveredPool{{ManagedPool: garmapi.ManagedPool{ID: "00000000-0000-4000-8000-000000000002", ProviderName: "capacity"}, Configured: false}}, MissingConfiguredPoolIDs: []string{id}}}
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	journal, e := garmmanagement.InitializeJournal(filepath.Join(dir, "journal.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer journal.Close()
	manager, e := garmmanagement.New(remote, journal)
	if e != nil {
		t.Fatal(e)
	}
	view, e := NewJournalledGarm(manager, true, []string{id}, nil)
	if e != nil {
		t.Fatal(e)
	}
	got, e := view.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if got.PoolInventory == nil || len(got.PoolInventory.Pools) != 1 || got.PoolInventory.MissingConfiguredPoolIDs[0] != id || got.SettingsAvailable || got.WritesEnabled {
		t.Fatalf("wrong discovery/hold: %+v", got)
	}
	pending, e := journal.Pending(ctx)
	if e != nil || len(pending) != 0 {
		t.Fatal("read created intent", e)
	}
	priority := uint64(2)
	if _, e = view.UpdatePool(ctx, "11111111-1111-4111-8111-111111111111", got.PoolInventory.Pools[0].ID, "", garmapi.PoolUpdate{Priority: &priority}, "not authorized"); e == nil {
		t.Fatal("discovery expanded edit scope")
	}
}
