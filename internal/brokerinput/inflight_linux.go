package brokerinput

import (
	"context"
	"errors"
	"strconv"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

var errVisibleStockInventoryUnmatched = errors.New("scoped stock runner inventory could not be matched")

// PlanStock reads coherent real ledger/policy input and deduplicates scoped stock
// identities before planning. It never grants, revokes, mutates GARM or releases
// capacity. Missing/unknown in-flight outcomes defer demand, not satisfy it.
func PlanStock(ctx context.Context, store *admission.SQLiteStore, profiles map[string]LedgerProfile, observation garmapi.DemandObservation, opt capacitybroker.Options) (capacitybroker.JobPlan, admission.BrokerGrantState, error) {
	snapshot, state, err := Ledger(ctx, store, profiles, opt)
	if err != nil {
		return capacitybroker.JobPlan{}, admission.BrokerGrantState{}, err
	}
	if err = inflight(&snapshot, state, observation); err != nil {
		return capacitybroker.JobPlan{}, admission.BrokerGrantState{}, err
	}
	plan, err := Stock(snapshot, state.Policy, observation, opt)
	if err != nil {
		return capacitybroker.JobPlan{}, admission.BrokerGrantState{}, err
	}
	return plan, state, nil
}

// key uses length prefixes, never ambiguous delimiter concatenation.
func runnerKey(provider, pool, name string) string {
	return strconv.Itoa(len(provider)) + ":" + provider + strconv.Itoa(len(pool)) + ":" + pool + name
}
func inflight(snapshot *capacitybroker.Snapshot, state admission.BrokerGrantState, observation garmapi.DemandObservation) error {
	if err := matchStockInflight(snapshot, state, observation); err != nil {
		return capacitybroker.ErrSnapshot
	}
	return nil
}

func matchStockInflight(snapshot *capacitybroker.Snapshot, state admission.BrokerGrantState, observation garmapi.DemandObservation) error {
	if len(observation.Runners) > 10000 {
		return capacitybroker.ErrSnapshot
	}
	rows := map[string]garmapi.ObservedRunner{}
	ids := map[string]bool{}
	counts := map[string]garmapi.RunnerSupply{}
	for _, row := range observation.Runners {
		key := runnerKey(row.ProviderName, row.PoolID, row.Name)
		if row.RowID == "" || row.Name == "" || row.ProviderName == "" || ids[row.RowID] {
			return capacitybroker.ErrSnapshot
		}
		if _, exists := rows[key]; exists {
			return capacitybroker.ErrSnapshot
		}
		ids[row.RowID], rows[key] = true, row
		c := counts[row.PoolID]
		c.PoolID = row.PoolID
		switch row.Kind {
		case "idle":
			c.Idle++
		case "busy":
			c.Busy++
		case "creating":
			c.Creating++
		case "other":
			c.Other++
		default:
			return capacitybroker.ErrSnapshot
		}
		counts[row.PoolID] = c
	}
	for _, supply := range observation.Supply {
		c := counts[supply.PoolID]
		c.PoolID = supply.PoolID
		if c != supply {
			return capacitybroker.ErrSnapshot
		}
		delete(counts, supply.PoolID)
	}
	if len(counts) != 0 {
		return capacitybroker.ErrSnapshot
	}
	known := map[string]bool{}
	for _, grant := range state.Grants {
		if grant.State != "consumed" || state.Released[grant.RequestKey] {
			continue
		}
		reservation, active := state.Reservations[grant.RequestKey]
		if !active {
			continue
		} // canonical store has already checked cancellation.
		plan, found := state.ExecutionPlans[grant.RequestKey]
		if !found || plan.Request != reservation.Request || plan.Execution.Name == "" || plan.Execution.Controller != grant.Spec.Authority.Controller {
			return capacitybroker.ErrSnapshot
		}
		key := runnerKey(grant.Spec.Authority.ProviderName, reservation.Request.Pool, plan.Execution.Name)
		if known[key] {
			return capacitybroker.ErrSnapshot
		}
		known[key] = true
		row, observed := rows[key]
		delete(rows, key)
		// A positively observed registered idle/busy row is counted by Stock,
		// never again as ledger startup supply. This is dedup, not release.
		if observed && (row.Kind == "idle" || row.Kind == "busy") {
			continue
		}
		if reservation.State == "creating" || reservation.State == "unknown" {
			p, exists := snapshot.Pools[reservation.Request.Pool]
			if !exists || reservation.Request.Resources.Runners != 1 {
				return capacitybroker.ErrSnapshot
			}
			p.Inflight++
			p.PendingWarm++
			snapshot.Pools[reservation.Request.Pool] = p
		}
	}
	// Unaccounted stock identities cannot be covered by unrelated grant charges.
	if len(rows) != 0 {
		// The read-only diagnostic can name the check only when it also
		// failed for a pool already shown by the saved-policy preview. A
		// source-scoped but undisplayed pool never supplies this reason.
		unmatchedPools := make(map[string]bool, len(rows))
		for _, row := range rows {
			unmatchedPools[row.PoolID] = true
		}
		for _, repo := range state.Policy.Repositories {
			for _, pool := range repo.Pools {
				if unmatchedPools[pool.ID] {
					return errVisibleStockInventoryUnmatched
				}
			}
		}
		return capacitybroker.ErrSnapshot
	}
	// Legacy/unidentified startup reservations cannot be deduplicated safely.
	covered := map[string]bool{}
	for _, g := range state.Grants {
		if g.State == "consumed" {
			covered[g.RequestKey] = true
		}
	}
	for key, r := range state.Reservations {
		if !state.Released[key] && (r.State == "creating" || r.State == "unknown") && !covered[key] {
			return capacitybroker.ErrSnapshot
		}
	}
	return nil
}
