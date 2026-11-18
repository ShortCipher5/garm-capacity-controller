package gateway

import (
	"context"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// DeletionOutcome is the durable disposition of one verified retirement
// request. Only DeletionReleased confirms the charge was freed; the other
// outcomes never claim deletion and are safe for the provider to retry.
type DeletionOutcome string

const (
	// DeletionReleased is retained for wire compatibility with historical clients.
	// No current physical retirement path produces it.
	DeletionReleased DeletionOutcome = "released"
	// DeletionAlreadyReleased is returned only for exact never-dispatched
	// cancellation tombstones. It maps to provider-common NotFound.
	DeletionAlreadyReleased DeletionOutcome = "already_released"
	// DeletionStillCharged is retained for historical wire compatibility.
	// Blocked physical retirement returns ErrUnavailable without a receipt.
	DeletionStillCharged DeletionOutcome = "release_failed_still_charged"
)

// DeletionReceipt is bounded outcome data only: no bootstrap, credentials,
// operation metadata or raw provider errors.
type DeletionReceipt struct {
	Outcome    DeletionOutcome
	Request    admission.Request
	ReleasedAt time.Time
}

// Delete recognizes exact never-dispatched cancellation only. Physical retirement
// is blocked: project-scoped absence reads do not establish job completion,
// exclusive incarnation ownership, or a durable old-writer/post-delete fence.
// No startup flag or provider request may turn that observation into authority.
// Existing release tombstones are historical records, not safe deletion proof.
func (f *Facade) Delete(ctx context.Context, controller, pool, name string) (DeletionReceipt, error) {
	if controller == "" || controller != f.controller || name == "" || strings.ContainsRune(name, 0) {
		return DeletionReceipt{}, ErrPolicy
	}
	if _, ok := f.pools[pool]; !ok {
		return DeletionReceipt{}, ErrPolicy
	}
	state, err := f.store.Snapshot(ctx)
	if err != nil {
		return DeletionReceipt{}, ErrUnavailable
	}
	req, err := f.accountingRequest(ctx, state, name, pool, "")
	if err != nil {
		return DeletionReceipt{}, ErrPolicy
	}
	reservation, exists := state.Reservations[req.Key]
	if !exists {
		// Idempotent replay is answered from immutable tombstones only.
		if cancelled, err := f.store.LookupCancellation(ctx, req.Key); err == nil && cancelled.Request == req {
			// CancelUndispatched atomically proved this identity was never
			// dispatched and no resource was ever linked, so nothing exists
			// to delete; report the same idempotent absence as a release.
			return DeletionReceipt{Outcome: DeletionAlreadyReleased, Request: req}, nil
		}
		return DeletionReceipt{}, ErrNotFound
	}
	if reservation.Request != req {
		return DeletionReceipt{}, ErrUnavailable
	}
	linked := 0
	for _, resource := range state.Inventory {
		if resource.ReservationKey == req.Key {
			if resource.ID != name || resource.Host != req.Host || resource.Pool != pool {
				return DeletionReceipt{}, ErrUnavailable
			}
			linked++
		}
	}
	if linked != 1 {
		// Never dispatched, failed before physical proof, or already released:
		// this slice refuses to delete what the ledger cannot place.
		return DeletionReceipt{}, ErrNotFound
	}
	// Do not even mark deleting or call the backend. A name-based DELETE can
	// race job assignment or incarnation replacement before absence is read.
	return DeletionReceipt{}, ErrUnavailable
}
