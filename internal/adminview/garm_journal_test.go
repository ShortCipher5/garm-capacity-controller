package adminview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

type boundedJournalRemote struct {
	pool                 garmapi.ManagedPool
	unknown, unavailable bool
}

func (r *boundedJournalRemote) ManagementIdentity() string { return strings.Repeat("b", 64) }
func (r *boundedJournalRemote) Snapshot(context.Context) (garmapi.ManagementSnapshot, error) {
	if r.unavailable {
		return garmapi.ManagementSnapshot{}, errors.New("unavailable")
	}
	return garmapi.ManagementSnapshot{Pools: []garmapi.ManagedPool{r.pool}, Repositories: []garmapi.ManagedRepository{}, WritesEnabled: true}, nil
}
func (r *boundedJournalRemote) Pool(context.Context, string) (garmapi.ManagedPool, error) {
	return r.pool, nil
}
func (r *boundedJournalRemote) Repository(context.Context, string) (garmapi.ManagedRepository, error) {
	return garmapi.ManagedRepository{}, garmapi.ErrManagementUnsafe
}
func (r *boundedJournalRemote) UpdateRepositoryBalancer(context.Context, string, string, string) (garmapi.ManagedRepository, error) {
	return garmapi.ManagedRepository{}, garmapi.ErrManagementUnsafe
}
func (r *boundedJournalRemote) UpdatePool(_ context.Context, _, _ string, u garmapi.PoolUpdate) (garmapi.ManagedPool, error) {
	if u.Priority != nil {
		r.pool.Priority = *u.Priority
	}
	r.pool.Fingerprint = fmt.Sprintf("%064x", r.pool.Priority+1)
	if r.unknown {
		return garmapi.ManagedPool{}, garmapi.ErrManagementOutcomeUnknown
	}
	return r.pool, nil
}

func TestGarmJournalLargeHistoryPreservesStatusAndNarrowedScopePrivacy(t *testing.T) {
	ctx := context.Background()
	poolID := "00000000-0000-4000-8000-000000000001"
	repoID := "00000000-0000-4000-8000-000000000002"
	tags := []string{}
	for i := 0; i < 100; i++ {
		tags = append(tags, strings.Repeat("\\\"", 125)+fmt.Sprintf("%06d", i))
	}
	remote := &boundedJournalRemote{pool: garmapi.ManagedPool{ID: poolID, ProviderName: "capacity", Fingerprint: fmt.Sprintf("%064x", 1), Enabled: true, Priority: 0, MaxRunners: 4, MinIdleRunners: 1, Generation: 1, Tags: tags}}
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
	for i := 1; i <= 25; i++ {
		priority := uint64(i)
		request := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		if _, err := manager.UpdatePool(ctx, request, poolID, remote.pool.Fingerprint, garmapi.PoolUpdate{Priority: &priority}, "bounded history test"); err != nil {
			t.Fatal(err)
		}
	}
	remote.unknown = true
	priority := uint64(26)
	request := "00000026-0000-4000-8000-000000000000"
	if _, err := manager.UpdatePool(ctx, request, poolID, remote.pool.Fingerprint, garmapi.PoolUpdate{Priority: &priority}, "uncertain test"); err == nil {
		t.Fatal("uncertain fixture failed")
	}
	adapter, err := NewJournalledGarm(manager, true, []string{poolID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	view, err := adapter.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 2<<20 || !view.HistoryTruncated || len(view.History) == 0 || len(view.History) >= 25 || !view.WriteBlocked || view.Pending == nil || view.History[0].State != "unknown" {
		t.Fatalf("large history hid journal safety status: bytes=%d history=%d view=%+v", len(encoded), len(view.History), view)
	}
	remote.unavailable = true
	view, err = adapter.Snapshot(ctx)
	if err != nil || !view.WriteBlocked || view.SettingsAvailable || view.Pending == nil {
		t.Fatal("upstream outage hid durable block")
	}
	remote.unavailable = false
	narrowed, err := NewJournalledGarm(manager, true, nil, []string{repoID})
	if err != nil {
		t.Fatal(err)
	}
	view, err = narrowed.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.History) != 0 || len(view.Settings.Pools) != 0 || !view.WriteBlocked || view.Pending == nil || view.Pending.Target != "outside current scope" {
		t.Fatal("narrowed scope exposed former settings or lost global pending block")
	}
}
