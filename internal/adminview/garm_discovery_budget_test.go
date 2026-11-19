package adminview

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

type delayedInventoryRemote struct {
	boundedJournalRemote
	settingsReads atomic.Int32
	deadlineSeen  time.Time
	startedAt     time.Time
}

func (r *delayedInventoryRemote) DiscoverPools(ctx context.Context) (garmapi.PoolDiscovery, error) {
	r.startedAt = time.Now()
	r.deadlineSeen, _ = ctx.Deadline()
	<-ctx.Done()
	return garmapi.PoolDiscovery{}, ctx.Err()
}
func (r *delayedInventoryRemote) Snapshot(ctx context.Context) (garmapi.ManagementSnapshot, error) {
	r.settingsReads.Add(1)
	if ctx.Err() != nil {
		return garmapi.ManagementSnapshot{}, ctx.Err()
	}
	return r.boundedJournalRemote.Snapshot(ctx)
}
func TestSlowDiscoveryDoesNotHideReachableSettings(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	remote := &delayedInventoryRemote{boundedJournalRemote: boundedJournalRemote{pool: garmapi.ManagedPool{ID: id, ProviderName: "capacity", MaxRunners: 4}}}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := garmmanagement.InitializeJournal(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	manager, err := garmmanagement.New(remote, journal)
	if err != nil {
		t.Fatal(err)
	}
	view, err := NewJournalledGarm(manager, true, []string{id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Leave scheduling headroom under the aggregate race suite, while still
	// exercising actual child-context expiry and enforcing the same half-budget.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	got, err := view.Snapshot(ctx)
	if err != nil || !got.SettingsAvailable || len(got.Settings.Pools) != 1 || got.PoolInventory != nil || remote.settingsReads.Load() != 1 {
		t.Fatalf("discovery poisoned settings: %+v %v", got, err)
	}
	if !remote.deadlineSeen.Before(parentDeadline) || remote.deadlineSeen.After(remote.startedAt.Add(parentDeadline.Sub(remote.startedAt)/2)) || ctx.Err() != nil {
		t.Fatal("discovery consumed parent budget")
	}
	pending, err := journal.Pending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatal("read mutated journal")
	}
}
func TestDiscoveryBudgetNeverExtendsCaller(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	child, done := boundedPoolDiscoveryContext(parent)
	defer done()
	if child.Err() == nil {
		t.Fatal("cancellation lost")
	}
	child2, done2 := boundedPoolDiscoveryContext(context.Background())
	defer done2()
	deadline, ok := child2.Deadline()
	if !ok || time.Until(deadline) > 3*time.Second {
		t.Fatal("unbounded discovery")
	}
}
