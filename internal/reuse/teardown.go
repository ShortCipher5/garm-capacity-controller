package reuse

import (
	"errors"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// ErrTeardownEvidence identifies authentic observations that cannot be bound
// into this contract. Every binding failure keeps the charge held.
var ErrTeardownEvidence = errors.New("teardown observations do not bind to the original charge")

// TeardownProofFromObservations binds one incarnation observation, the
// captured original terminal DELETE operation for that same incarnation and
// the two-pass post-delete project absence read into one teardown proof.
// Observations under any other incarnation, scope, daemon or version never
// validate.
func TeardownProofFromObservations(identity incusapi.InstanceIdentityObservation, delete incusapi.TerminalOperationObservation, absence incusapi.ProjectAbsenceObservation) (InstanceTeardownProof, error) {
	if !delete.Valid() || !absence.Valid() {
		return InstanceTeardownProof{}, ErrTeardownEvidence
	}
	proof := InstanceTeardownProof{Identity: identity, Delete: delete.Record(), Absence: absence.Record()}
	if !proof.Valid() {
		return InstanceTeardownProof{}, ErrTeardownEvidence
	}
	return proof, nil
}
