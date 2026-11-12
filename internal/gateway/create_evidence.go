package gateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// Only the credential-owning gateway supplies this optional read-only capability.
// A provider/bootstrap caller cannot construct an authenticated observation.
type createEvidenceBackend interface {
	ObserveInstanceIdentity(context.Context, incusapi.IdentityExpectation) (incusapi.VerifiedInstanceIdentity, error)
	CaptureTerminalOperation(context.Context, incusapi.VerifiedInstanceIdentity, string, incusapi.RecoveryOperationKind) (incusapi.TerminalOperationObservation, error)
}

type createEvidenceCollection uint8

const (
	createEvidenceUnavailable createEvidenceCollection = iota
	createEvidenceStored
	createEvidenceWriterBlocked
)
const createEvidenceDiagnostic = "create operation evidence collection unconfirmed; allocation remains charged"

// collectCreateEvidence is called ONLY inside the original successful dispatch
// callback after durable submitted correlation, Wait and exact running readback.
// It does not run on failed/unknown creates or confirmed replays. No redispatch,
// DELETE, release, job-idle claim or dispatch promotion is performed here.
//
// A shared independent five-second context deadline covers reads and append,
// surviving a caller disconnect. It is not a hard real-time bound on SQLite
// lock/fsync completion and cannot extend Incus retention. No write worker is
// abandoned to manufacture a prompt success. Any missing,
// expired, malformed or refused proof remains missing. The journal rechecks exact
// original operation/request and immutable full binding provenance atomically.
func (f *Facade) collectCreateEvidence(ctx context.Context, req admission.Request, created incusapi.Created) createEvidenceCollection {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	missing := func(outcome createEvidenceCollection) createEvidenceCollection {
		// Fixed diagnostic only: never raw error bodies, bootstrap, metadata, URLs,
		// provider identifiers or caller-controlled values. An append reply may be lost
		// AFTER committing, so "unconfirmed" must not assert that no proof was stored.
		slog.WarnContext(ctx, createEvidenceDiagnostic)
		return outcome
	}
	source, ok := f.hosts[req.Host].(createEvidenceBackend)
	if !ok {
		return missing(createEvidenceUnavailable)
	}
	identity, err := source.ObserveInstanceIdentity(ctx, incusapi.IdentityExpectation{Name: created.ResourceID, ControllerID: f.controller, PoolID: req.Pool, ReservationKey: req.Key, CPUMillis: req.Resources.CPU, RAMMiB: req.Resources.RAM})
	if err != nil {
		return missing(createEvidenceUnavailable)
	}
	observation, err := source.CaptureTerminalOperation(ctx, identity, created.OperationID, incusapi.RecoveryCreate)
	if err != nil {
		return missing(createEvidenceUnavailable)
	}
	if _, err := f.store.StoreCreateObservation(ctx, req, observation); err != nil {
		if errors.Is(err, admission.ErrUncertain) || errors.Is(err, admission.ErrRuntimeLineage) {
			return missing(createEvidenceWriterBlocked)
		}
		return missing(createEvidenceUnavailable)
	}
	return createEvidenceStored
}
