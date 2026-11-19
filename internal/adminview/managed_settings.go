package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsettings"
	"time"
)

type managedSettingsView struct {
	client *managedsettings.Client
	caps   *brokerinput.CapService
	writes bool
	pools  map[string]bool
}

func NewManagedSettingsView(c *managedsettings.Client, writes bool, pools []string) (GarmManager, error) {
	if c == nil || len(pools) == 0 {
		return nil, ErrConfiguration
	}
	v := &managedSettingsView{client: c, writes: writes, pools: map[string]bool{}}
	for _, p := range pools {
		v.pools[p] = true
	}
	return v, nil
}
func (v *managedSettingsView) SetPolicyCapService(s *brokerinput.CapService) { v.caps = s }
func (v *managedSettingsView) Snapshot(ctx context.Context) (GarmOverview, error) {
	status, e := v.client.Status(ctx)
	if e != nil {
		return GarmOverview{}, e
	}
	out := GarmOverview{MaximumReductionOnly: true, History: []GarmChangeEvent{}, HistoryTruncated: true, Settings: garmapi.ManagementSnapshot{Pools: []garmapi.ManagedPool{}, Repositories: []garmapi.ManagedRepository{}}}
	if v.caps != nil {
		state := v.caps.Snapshot()
		out.PolicyCapReconciler = &state
	}
	apply := func(s garmmanagement.Status) error {
		out.Pending = nil
		out.WriteBlocked = s.WriteBlocked
		if len(s.Pending) > 0 {
			p := s.Pending[0]
			at, e := time.Parse(time.RFC3339Nano, p.CreatedAt)
			if e != nil {
				return e
			}
			target := p.Target
			if !v.pools[target] {
				target = "outside current scope"
			}
			out.Pending = &GarmPending{RequestID: p.RequestID, Kind: p.Kind, Target: target, StartedAt: at, ResolutionAvailable: false}
		}
		return nil
	}
	if e = apply(status); e != nil {
		return GarmOverview{}, e
	}
	snapshot, snapshotErr := v.client.Snapshot(ctx)
	if snapshotErr == nil {
		out.Settings = snapshot.ManagementSnapshot
		out.SettingsAvailable = true
		out.WritesEnabled = v.writes && snapshot.WritesEnabled
		if e = apply(snapshot.Status); e != nil {
			return GarmOverview{}, e
		}
	}
	// History is optional: it must not consume the required settings read's
	// deadline or turn an independently successful snapshot into unavailable.
	budget := 3 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) / 2; remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return out, nil
	}
	historyCtx, cancelHistory := context.WithTimeout(ctx, budget)
	defer cancelHistory()
	if history, e := v.client.History(historyCtx); e == nil {
		out.ManagedHistoryAvailable = true
		out.HistoryTruncated = history.Truncated
		for _, event := range history.Events {
			before, _ := json.Marshal(event.Before)
			desired, _ := json.Marshal(event.Desired)
			var result json.RawMessage
			if event.Result != nil {
				result, _ = json.Marshal(event.Result)
			}
			out.History = append(out.History, GarmChangeEvent{RequestID: event.RequestID, Kind: event.Kind, Target: event.Target, State: event.State, Reason: event.Reason, CreatedAt: event.CreatedAt, Before: before, Desired: desired, Result: result})
		}
	}
	return out, nil
}
func (v *managedSettingsView) UpdatePool(ctx context.Context, request, id, fp string, u garmapi.PoolUpdate, reason string) (garmapi.ManagedPool, error) {
	if !v.writes || !v.pools[id] {
		return garmapi.ManagedPool{}, garmapi.ErrManagementDisabled
	}
	if u.MaxRunners == nil || u.Tags != nil || u.Priority != nil || u.MinIdleRunners != nil || u.Enabled != nil {
		return garmapi.ManagedPool{}, garmapi.ErrManagementUnsafe
	}
	return v.client.ReduceMaximum(ctx, request, id, fp, *u.MaxRunners, reason)
}
func (*managedSettingsView) UpdateRepositoryBalancer(context.Context, string, string, string, string, string) (garmapi.ManagedRepository, error) {
	return garmapi.ManagedRepository{}, garmapi.ErrManagementDisabled
}
func (*managedSettingsView) ResolvePool(context.Context, string) (garmapi.ManagedPool, error) {
	return garmapi.ManagedPool{}, garmapi.ErrManagementDisabled
}
