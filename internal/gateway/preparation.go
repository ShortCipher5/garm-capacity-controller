package gateway

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// garmPreparation only selects a candidate. Reconcile must additionally collect
// complete physical/operation inventory, and CompleteReconciliation must recheck
// provenance under the same durable fence before omitting this nonphysical row.
func (f *Facade) garmPreparation(ctx context.Context, fence admission.Fence, state admission.SQLiteSnapshot, row GarmInstance, req admission.Request, observedAt time.Time) (admission.GarmPreparation, bool, error) {
	if !row.PreparationPending || row.State != "pending_create" || row.ProviderID != "" || row.Busy || row.Idle {
		return admission.GarmPreparation{}, false, nil
	}
	reservation, exists := state.Reservations[req.Key]
	if !exists || reservation.Request != req || reservation.State != "creating" {
		return admission.GarmPreparation{}, false, nil
	}
	for _, old := range state.Inventory {
		if old.ID == row.Name || old.ReservationKey == req.Key {
			return admission.GarmPreparation{}, false, nil
		}
	}
	plan, _, err := f.planned(ctx, row.Name, row.PoolID)
	if err != nil || plan.Request != req {
		return admission.GarmPreparation{}, false, nil
	}
	_, err = f.store.LookupDispatchReadOnly(ctx, req.Key)
	if err == nil {
		return admission.GarmPreparation{}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return admission.GarmPreparation{}, false, err
	}
	return admission.GarmPreparation{Fence: fence, Request: req, Name: row.Name, HostBindingSequence: plan.HostBindingSequence, ObservedAt: observedAt}, true, nil
}
