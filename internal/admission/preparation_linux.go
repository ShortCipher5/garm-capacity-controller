package admission

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GarmPreparation is a trusted collector's narrowly typed observation of a
// Garm database row, NOT a physical resource or a create/retirement permit.
// It is considered only with complete post-barrier physical/operation inventory,
// an exact immutable execution plan, a still-charged reservation and NO dispatch.
// Never accept this structure from provider/browser JSON or cache it across fences.
type GarmPreparation struct {
	Fence               Fence
	Request             Request
	Name                string
	HostBindingSequence int64
	ObservedAt          time.Time
}

// LookupDispatchReadOnly cannot grant a dispatch permit or poison the writer on
// cancellation. Reconciliation still rechecks absence under its commit transaction.
func (s *SQLiteStore) LookupDispatchReadOnly(ctx context.Context, key string) (DispatchRecord, error) {
	var record DispatchRecord
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		record, _, err = dispatchRecord(tx, key)
		return err
	})
	return record, err
}

func sqliteGarmPreparations(tx *sql.Tx, config Config, fence Fence, snapshot InventorySnapshot, reservations map[string]Reservation, previous []InventoryResource, began, now time.Time) error {
	if len(snapshot.Preparations) == 0 {
		return nil
	}
	if len(snapshot.Preparations) > len(reservations) {
		return ErrReconciliation
	}
	hosts := map[string]HostObservation{}
	for _, host := range snapshot.Hosts {
		hosts[host.Host] = host
	}
	keys, names := map[string]bool{}, map[string]bool{}
	for _, preparation := range snapshot.Preparations {
		req := preparation.Request
		host, ok := hosts[req.Host]
		if preparation.Fence != fence || !validRequest(req) || !bindingName(preparation.Name) || preparation.HostBindingSequence <= 0 || !ok || !host.Healthy || !host.Bounded || !host.GarmComplete || !host.IncusComplete || !host.OperationsComplete || !preparation.ObservedAt.Equal(host.ObservedAt) || preparation.ObservedAt.Before(began) || preparation.ObservedAt.After(now) || now.Sub(preparation.ObservedAt) > config.MaxObservationAge || keys[req.Key] || names[preparation.Name] {
			return ErrReconciliation
		}
		keys[req.Key] = true
		names[preparation.Name] = true
		reservation, exists := reservations[req.Key]
		if !exists || reservation.Request != req || reservation.State != "creating" {
			return ErrReconciliation
		}
		plan, err := executionPlanRecord(tx, req.Key)
		if err != nil || plan.Request != req || plan.Execution.Name != preparation.Name || plan.HostBindingSequence != preparation.HostBindingSequence {
			return ErrReconciliation
		}
		binding, err := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", req.Host).Scan)
		if err != nil || binding.Sequence != preparation.HostBindingSequence {
			return ErrReconciliation
		}
		if _, _, err = dispatchRecord(tx, req.Key); !errors.Is(err, sql.ErrNoRows) {
			return ErrReconciliation
		}
		if _, err = cancellationRecord(tx, req.Key); !errors.Is(err, sql.ErrNoRows) {
			return ErrReconciliation
		}
		var legacy bool
		if tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM cancellation_history_exclusions WHERE request_key=? UNION ALL SELECT 1 FROM host_binding_history_exclusions WHERE host=? LIMIT 1)`, req.Key, req.Host).Scan(&legacy) != nil || legacy {
			return ErrReconciliation
		}
		// Absence now must never launder prior ambiguous/physical inventory. A row
		// previously imported without this provenance remains fully charged/blocking.
		for _, resources := range [][]InventoryResource{previous, snapshot.Resources} {
			for _, resource := range resources {
				if resource.ReservationKey == req.Key || resource.ID == preparation.Name {
					return ErrReconciliation
				}
			}
		}
	}
	for _, preparation := range snapshot.Preparations {
		// The plan permanently carries exact request and binding-sequence provenance.
		// This audit is committed with the successful barrier completion. It is NOT a
		// cached absence proof or permission to replay a create or release any charge.
		if err := sqliteAudit(tx, "garm_preparation_verified", preparation.Request.Key, now); err != nil {
			return err
		}
	}
	return nil
}
