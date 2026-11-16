package main

import (
	"context"
	"testing"
)

func TestBrowserGarmRealCollectionDiscovery(t *testing.T) {
	manager, closeManager, err := browserGarm(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager()
	first, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.PoolInventory == nil || len(first.PoolInventory.Pools) != 1 {
		t.Fatalf("expected complete pool observation")
	}
	pool := first.PoolInventory.Pools[0]
	if pool.ID != browserPool || pool.OwnerKind != "repository" || pool.OwnerID != browserRepository || !pool.Configured || pool.Fingerprint != "" {
		t.Fatal("unexpected identity or permission projection")
	}
	second, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.History) != 0 || second.WriteBlocked || len(second.Settings.Pools) != 1 || second.Settings.Pools[0].Fingerprint != first.Settings.Pools[0].Fingerprint {
		t.Fatal("read changed managed state or journal")
	}
}
