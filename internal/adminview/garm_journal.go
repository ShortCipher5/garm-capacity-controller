package adminview

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

type journalledGarm struct {
	manager             *garmmanagement.Manager
	writes              bool
	pools, repositories map[string]bool
}

// NewJournalledGarm exposes only the durable journal wrapper, never the raw
// remote client. Its explicit target allowlists also constrain recorded replays.
func NewJournalledGarm(manager *garmmanagement.Manager, writes bool, poolIDs, repositoryIDs []string) (GarmManager, error) {
	if manager == nil || len(poolIDs)+len(repositoryIDs) == 0 || len(poolIDs)+len(repositoryIDs) > 1000 {
		return nil, ErrConfiguration
	}
	adapter := &journalledGarm{manager: manager, writes: writes, pools: map[string]bool{}, repositories: map[string]bool{}}
	for _, id := range poolIDs {
		adapter.pools[id] = true
	}
	for _, id := range repositoryIDs {
		adapter.repositories[id] = true
	}
	return adapter, nil
}
func (j *journalledGarm) Snapshot(ctx context.Context) (GarmOverview, error) {
	// Durable status remains visible when remote discovery is unavailable. A read
	// does not reconcile, clear, replay or otherwise change an unresolved intent.
	status, err := j.manager.Status(ctx)
	if err != nil {
		return GarmOverview{}, err
	}
	view := GarmOverview{Settings: garmapi.ManagementSnapshot{Pools: []garmapi.ManagedPool{}, Repositories: []garmapi.ManagedRepository{}}, WriteBlocked: status.WriteBlocked}
	setPending := func(status garmmanagement.Status) error {
		view.Pending = nil
		view.WriteBlocked = status.WriteBlocked
		if len(status.Pending) > 0 {
			pending := status.Pending[0]
			at, err := time.Parse(time.RFC3339Nano, pending.CreatedAt)
			if err != nil {
				return err
			}
			target := pending.Target
			if (pending.Kind == "pool" && !j.pools[target]) || (pending.Kind == "repository" && !j.repositories[target]) {
				target = "outside current scope"
			}
			view.Pending = &GarmPending{State: pendingDisplayState(pending.State), RequestID: pending.RequestID, Kind: pending.Kind, Target: target, StartedAt: at, ResolutionAvailable: j.writes && j.pools[pending.Target] && j.manager.CanResolvePoolIntent(pending)}
		}
		return nil
	}
	if err := setPending(status); err != nil {
		return GarmOverview{}, err
	}
	poolIDs, repositoryIDs := []string{}, []string{}
	for id := range j.pools {
		poolIDs = append(poolIDs, id)
	}
	for id := range j.repositories {
		repositoryIDs = append(repositoryIDs, id)
	}
	history, err := j.manager.RecentScopedIntents(ctx, 26, poolIDs, repositoryIDs)
	if err != nil {
		return GarmOverview{}, err
	}
	view.History = []GarmChangeEvent{}
	historyBytes := 0
	for _, event := range history {
		item := GarmChangeEvent{RequestID: event.RequestID, Kind: event.Kind, Target: event.Target, State: event.State, Reason: event.Reason, CreatedAt: event.CreatedAt, Before: event.Before, Desired: event.Desired, Result: event.Result}
		encoded, err := json.Marshal(item)
		if err != nil {
			return GarmOverview{}, err
		}
		if len(view.History) >= 25 || historyBytes+len(encoded) > 512<<10 {
			view.HistoryTruncated = true
			break
		}
		view.History = append(view.History, item)
		historyBytes += len(encoded)
	}
	// Discovery does not expand write scope. Preserve it even when an existing
	// configured target disappears and the editable snapshot cannot be read.
	discoveryCtx, cancelDiscovery := boundedPoolDiscoveryContext(ctx)
	inventory, discoveryErr := j.manager.DiscoverPools(discoveryCtx)
	cancelDiscovery()
	if discoveryErr == nil {
		view.PoolInventory = &inventory
	}
	view.ScaleSetInventory = observeScaleSets(ctx, j.manager)
	snapshot, err := j.manager.Snapshot(ctx)
	if err != nil {
		return view, nil
	}
	view.Settings = garmapi.ManagementSnapshot{Pools: []garmapi.ManagedPool{}, Repositories: []garmapi.ManagedRepository{}, WritesEnabled: snapshot.WritesEnabled}
	for _, pool := range snapshot.Pools {
		if j.pools[pool.ID] {
			view.Settings.Pools = append(view.Settings.Pools, pool)
		}
	}
	for _, repository := range snapshot.Repositories {
		if j.repositories[repository.ID] {
			view.Settings.Repositories = append(view.Settings.Repositories, repository)
		}
	}
	view.SettingsAvailable = true
	view.WritesEnabled = j.writes && snapshot.WritesEnabled
	if err := setPending(snapshot.Status); err != nil {
		return GarmOverview{}, err
	}
	return view, nil
}
func garmControlError(err error) error {
	if errors.Is(err, garmmanagement.ErrBlocked) || errors.Is(err, garmmanagement.ErrRequestConflict) {
		return garmapi.ErrManagementConflict
	}
	if errors.Is(err, garmmanagement.ErrOutcomeUnknown) {
		return garmapi.ErrManagementOutcomeUnknown
	}
	return err
}
func (j *journalledGarm) UpdatePool(ctx context.Context, request, id, fingerprint string, update garmapi.PoolUpdate, reason string) (garmapi.ManagedPool, error) {
	if !j.writes {
		return garmapi.ManagedPool{}, garmapi.ErrManagementDisabled
	}
	if !j.pools[id] {
		return garmapi.ManagedPool{}, garmapi.ErrManagementUnsafe
	}
	result, err := j.manager.UpdatePool(ctx, request, id, fingerprint, update, reason)
	return result, garmControlError(err)
}

func (j *journalledGarm) ResolvePool(ctx context.Context, request string) (garmapi.ManagedPool, error) {
	if !j.writes {
		return garmapi.ManagedPool{}, garmapi.ErrManagementDisabled
	}
	status, err := j.manager.Status(ctx)
	if err != nil {
		return garmapi.ManagedPool{}, garmControlError(err)
	}
	allowed := false
	for _, pending := range status.Pending {
		if pending.RequestID == request && pending.Kind == "pool" && j.pools[pending.Target] {
			allowed = true
		}
	}
	if !allowed {
		return garmapi.ManagedPool{}, garmapi.ErrManagementConflict
	}
	result, err := j.manager.ResolvePool(ctx, request)
	return result, garmControlError(err)
}
func (j *journalledGarm) UpdateRepositoryBalancer(ctx context.Context, request, id, fingerprint, value, reason string) (garmapi.ManagedRepository, error) {
	if !j.writes {
		return garmapi.ManagedRepository{}, garmapi.ErrManagementDisabled
	}
	if !j.repositories[id] {
		return garmapi.ManagedRepository{}, garmapi.ErrManagementUnsafe
	}
	result, err := j.manager.UpdateRepositoryBalancer(ctx, request, id, fingerprint, value, reason)
	return result, garmControlError(err)
}

// Optional collection discovery must not spend the entire overview deadline and
// incorrectly hide independently reachable configured settings. Give it at most
// three seconds and half the remaining caller budget, without extending either.
// No background request, cached success or mutation is introduced.
func boundedPoolDiscoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := 3 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline) / 2
		if remaining < budget {
			budget = remaining
		}
	}
	return context.WithTimeout(ctx, budget)
}
