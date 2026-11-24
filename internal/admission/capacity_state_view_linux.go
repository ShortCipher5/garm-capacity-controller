package admission

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

// CapacityStateView reports charged ledger resources, not physical utilization or execution permission.
// Released historical reservations are excluded. Reappearing released resources
// are charged as orphans rather than being hidden by their old reservation.
type CapacityStateView struct {
	ObservedAt      time.Time                 `json:"observed_at"`
	Source          string                    `json:"source"`
	GrantsAvailable bool                      `json:"grants_available"`
	Rows            []CapacityStateRow        `json:"rows"`
	Total           Resources                 `json:"total"`
	Unknown         *CapacityUnknownBreakdown `json:"unknown_breakdown,omitempty"`
}
type CapacityStateRow struct {
	State     string    `json:"state"`
	Resources Resources `json:"resources"`
}

// CapacityUnknownBreakdown classifies only already charged unknown resources.
// A linked state disagreement takes precedence over an explicit unknown state;
// neither category says whether a runner is physically present.
type CapacityUnknownBreakdown struct {
	RecordedUnknown   Resources `json:"recorded_unknown"`
	StateDisagreement Resources `json:"state_disagreement"`
}

func capacityStates(reservations map[string]Reservation, inventory []InventoryResource, released map[string]bool) ([]CapacityStateRow, Resources, *CapacityUnknownBreakdown, error) {
	states := map[string]Resources{}
	linked := map[string]InventoryResource{}
	var split CapacityUnknownBreakdown
	splitValid := true
	countUnknown := func(state string, disagreement bool, size Resources) {
		if state != "unknown" {
			return
		}
		part := &split.RecordedUnknown
		if disagreement {
			part = &split.StateDisagreement
		}
		if !accumulate(part, size) {
			splitValid = false
		}
	}
	add := func(state string, resources Resources) error {
		v := states[state]
		if !accumulate(&v, resources) {
			return errors.New("capacity state overflow")
		}
		states[state] = v
		return nil
	}
	for _, r := range inventory {
		if r.ReservationKey != "" {
			old, ok := reservations[r.ReservationKey]
			_, duplicate := linked[r.ReservationKey]
			if !ok || duplicate || old.Request.Host != r.Host || old.Request.Pool != r.Pool {
				return nil, Resources{}, nil, errors.New("conflicting inventory binding")
			}
			linked[r.ReservationKey] = r
		}
	}
	for key, r := range reservations {
		if released[key] {
			continue
		}
		size, state := r.Request.Resources, r.State
		disagreement := false
		if physical, ok := linked[key]; ok {
			size.CPU = max(size.CPU, physical.Resources.CPU)
			size.RAM = max(size.RAM, physical.Resources.RAM)
			size.Disk = max(size.Disk, physical.Resources.Disk)
			// Neither record establishes that it is newer than the other.
			// Conflicting states must not conceal failure or uncertainty.
			if state != physical.State {
				state = "unknown"
				disagreement = true
			}
		}
		if err := add(state, size); err != nil {
			return nil, Resources{}, nil, err
		}
		countUnknown(state, disagreement, size)
	}
	for _, r := range inventory {
		if r.ReservationKey != "" && !released[r.ReservationKey] {
			continue
		}
		state := r.State
		if r.ReservationKey != "" {
			state = "orphan"
		}
		if err := add(state, r.Resources); err != nil {
			return nil, Resources{}, nil, err
		}
		countUnknown(state, false, r.Resources)
	}
	rows := []CapacityStateRow{}
	total := Resources{}
	for state, size := range states {
		if !accumulate(&total, size) {
			return nil, Resources{}, nil, errors.New("capacity state overflow")
		}
		rows = append(rows, CapacityStateRow{state, size})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].State < rows[j].State })
	var unknown *CapacityUnknownBreakdown
	if charged, present := states["unknown"]; present && splitValid {
		parts := split.RecordedUnknown
		if accumulate(&parts, split.StateDisagreement) && parts == charged {
			unknown = &split
		}
	}
	return rows, total, unknown, nil
}
func (s *SQLiteStore) ReadCapacityStates(ctx context.Context) (CapacityStateView, error) {
	out := CapacityStateView{Source: "charged-ledger", GrantsAvailable: true, Rows: []CapacityStateRow{}}
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		inventory, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		released, err := sqliteReleasedKeys(tx)
		if err != nil {
			return err
		}
		allocations, err := sqliteAllocationsWithGrants(tx, config, reservations, inventory, released)
		if err != nil {
			return err
		}
		out.Rows, out.Total, out.Unknown, err = capacityStates(reservations, inventory, released)
		if err != nil {
			return err
		}

		grants, err := sqliteBrokerGrants(tx)
		if err != nil {
			return err
		}
		unused := Resources{}
		for _, g := range grants {
			if g.State == "unused" {
				if !accumulate(&unused, g.Spec.Resources) {
					return ErrBrokerGrant
				}
			}
		}
		if unused.Runners > 0 {
			out.Rows = append(out.Rows, CapacityStateRow{State: "granted", Resources: unused})
			if !accumulate(&out.Total, unused) {
				return ErrBrokerGrant
			}
		}
		sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].State < out.Rows[j].State })
		if out.Total != allocations.Global {
			return errors.New("capacity state accounting mismatch")
		}
		out.ObservedAt = s.now().UTC()
		return nil
	})
	return out, err
}
