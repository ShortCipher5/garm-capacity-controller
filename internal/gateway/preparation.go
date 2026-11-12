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
	if ((!f.PreparationConfigured() && !f.RuntimeOriginalPreparationRequired()) && (!row.PreparationPending || row.State != "pending_create")) || ((f.PreparationConfigured() || f.RuntimeOriginalPreparationRequired()) && (!row.SourcePreparationPending || (row.State != "pending_create" && row.State != "creating"))) || row.ProviderID != "" || row.Busy || row.Idle {
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
	proof := admission.GarmPreparation{Fence: fence, Request: req, Name: row.Name, HostBindingSequence: plan.HostBindingSequence, ObservedAt: observedAt}
	if f.PreparationConfigured() || f.RuntimeOriginalPreparationRequired() {
		receipt, err := f.store.LookupOriginalPreparation(ctx, req.Key)
		if err != nil {
			return admission.GarmPreparation{}, false, nil
		}
		a, err := f.preparationAuthority(req.Pool, receipt.Source.ClientFingerprint)
		if err != nil {
			return admission.GarmPreparation{}, false, nil
		}
		_, poolErr := f.preparationPoolMetadata(ctx, receipt.Original, a)
		if poolErr != nil {
			return admission.GarmPreparation{}, false, nil
		}
		receipt, err = f.store.ValidateOriginalPreparation(ctx, receipt.Original, a)
		if err != nil || !row.IdentityFieldsKnown || row.RowID != receipt.Original.InstanceID || row.AgentID != receipt.Original.AgentID || row.Generation != receipt.Original.Generation || row.ProviderName != receipt.Original.ProviderName || row.ScaleSetID != 0 {
			return admission.GarmPreparation{}, false, nil
		}
		proof.OriginalPreparationSequence = receipt.RecordSequence
		proof.OriginalRow = admission.GarmRowObservation{RequestKey: req.Key, SourceScopeFingerprint: a.SourceScopeFingerprint, InstanceID: row.RowID, Name: row.Name, PoolID: row.PoolID, ProviderName: row.ProviderName, AgentID: row.AgentID, Generation: row.Generation, ObservedAt: observedAt, PreparationShape: true}
	}
	return proof, true, nil
}
