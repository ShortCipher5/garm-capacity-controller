package gateway

import (
	"context"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// DeletionOutcome is the durable disposition of one verified retirement
// request. Only DeletionReleased confirms the charge was freed; the other
// outcomes never claim deletion and are safe for the provider to retry.
type DeletionOutcome string

const (
	// DeletionReleased: absence was confirmed by two matching project-scoped
	// reads and the ledger released the charge in one transaction.
	DeletionReleased DeletionOutcome = "released"
	// DeletionAlreadyReleased: an earlier verified call already retired this
	// exact identity. The provider maps this to provider-common NotFound so
	// stock GARM treats the retry as idempotent.
	DeletionAlreadyReleased DeletionOutcome = "already_released"
	// DeletionStillCharged: the request was verified and the durable 'deleting'
	// mark exists, but delete transport, wait or confirmed-absence failed. The
	// charge stays; re-dispatch is idempotent and re-runs the whole flow.
	DeletionStillCharged DeletionOutcome = "release_failed_still_charged"
)

// DeletionReceipt is bounded outcome data only: no bootstrap, credentials,
// operation metadata or raw provider errors.
type DeletionReceipt struct {
	Outcome    DeletionOutcome
	Request    admission.Request
	ReleasedAt time.Time
}

// deleteEvidenceBackend is the optional verified-observation capability the
// credential-owning gateway supplies. Only an authenticated transport can
// implement it; a provider request, browser JSON or operator boolean cannot
// manufacture either observation.
type deleteEvidenceBackend interface {
	ObserveInstanceIdentity(context.Context, incusapi.IdentityExpectation) (incusapi.VerifiedInstanceIdentity, error)
	ObserveProjectAbsence(context.Context, incusapi.VerifiedInstanceIdentity) (incusapi.ProjectAbsenceObservation, error)
}

// Delete retires exactly one ledger-verified, physically linked, incarnation-
// bound runner, and releases its charge only after confirmed absence. The
// request identity is verified against the ledger like Create (stored
// reservation request, linked inventory row, immutable host binding, original
// create evidence); unknown, unlinked, unevidenced or incarnation-drifted
// runners are refused with a typed error and never reach the backend.
//
// Ordering is the safety contract: the live incarnation is observed and bound
// to the original-create evidence, the reservation is durably marked
// 'deleting', and only then is exactly one DELETE issued and waited to a
// terminal state. Release happens only when two matching project-scoped reads
// afterwards prove the observed incarnation gone; a successful DELETE, a 404
// or a single read never releases. Any failure after the mark leaves the
// reservation 'deleting' and fully charged: the operator resolves it by
// re-dispatch (idempotent mark, whole flow re-runs). A re-dispatch cannot
// rebind a guest that is already gone (the live identity read fails closed),
// so a crash between a successful delete and the release requires operator
// evidence surgery; capacity is never reclaimed on restart alone.
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
	// A released identity answers from the immutable tombstone before any
	// ledger-state inspection: the retained reservation row is history only.
	// This path never touches the backend.
	if release, err := f.store.LookupRelease(ctx, req.Key); err == nil && release.Request == req {
		return DeletionReceipt{Outcome: DeletionAlreadyReleased, Request: req, ReleasedAt: release.ReleasedAt}, nil
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
	if _, err := f.boundBackend(ctx, req.Host); err != nil {
		return DeletionReceipt{}, ErrUnavailable
	}
	backend, ok := f.hosts[req.Host]
	if !ok {
		return DeletionReceipt{}, ErrUnavailable
	}
	source, ok := backend.(deleteEvidenceBackend)
	if !ok {
		// Without verified-observation capability absence can never be
		// confirmed, so no delete is issued at all.
		return DeletionReceipt{}, ErrUnavailable
	}
	// The original-create evidence binds the exact incarnation (UUID,
	// generation, created-at) this retirement must prove absent.
	evidence, err := f.store.LookupCreateEvidence(ctx, req.Key)
	if err != nil {
		return DeletionReceipt{}, ErrUnavailable
	}
	identity, err := source.ObserveInstanceIdentity(ctx, incusapi.IdentityExpectation{Name: name, ControllerID: f.controller, PoolID: pool, ReservationKey: req.Key, CPUMillis: req.Resources.CPU, RAMMiB: req.Resources.RAM})
	if err != nil {
		return DeletionReceipt{}, ErrUnavailable
	}
	if identity.Observation().SubjectFingerprint != evidence.Observation.Identity.SubjectFingerprint {
		// The live incarnation is not the evidenced one: external delete and
		// recreate, restore, or rename interference. Refuse; the charge stays.
		return DeletionReceipt{}, ErrUnavailable
	}
	// Durable visibility mark BEFORE the destructive call; replay is a no-op.
	if _, err := f.store.MarkDeleting(ctx, req); err != nil {
		return DeletionReceipt{}, ErrUnavailable
	}
	stillCharged := DeletionReceipt{Outcome: DeletionStillCharged, Request: req}
	deletion, err := backend.Delete(ctx, name)
	if err != nil {
		return stillCharged, nil
	}
	if err := backend.WaitDeletion(ctx, deletion); err != nil {
		return stillCharged, nil
	}
	// Confirmed absence: two matching project-scoped passes that reject any
	// row matching the observed incarnation. A successful DELETE or a single
	// read is never enough.
	absence, err := source.ObserveProjectAbsence(ctx, identity)
	if err != nil || !absence.Valid() {
		return stillCharged, nil
	}
	release, err := f.store.ReleaseConfirmed(ctx, req, admission.ReleaseEvidence{Absence: absence})
	if err != nil {
		// The charge definitely remains; the tombstone check makes an exact
		// re-dispatch replay safely instead of releasing twice.
		return stillCharged, nil
	}
	return DeletionReceipt{Outcome: DeletionReleased, Request: req, ReleasedAt: release.ReleasedAt}, nil
}
