package retirement

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/reuse"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
)

// ErrTeardownEvidence identifies authentic observations that cannot be bound
// into the verified reuse contract. Every binding failure stays charged.
var ErrTeardownEvidence = errors.New("teardown observations do not bind to the original charge")

// StockRegistration binds one authenticated stock JIT registration record into
// the reuse contract. The digest covers controller, pool, name, runner ID,
// repository URL and metadata origin, so a registration observed through any
// other source or scope never validates.
func StockRegistration(record stockjit.Record) (reuse.StockBinding, error) {
	if !recordValid(record) {
		return reuse.StockBinding{}, ErrTeardownEvidence
	}
	encoding, err := json.Marshal(struct {
		ControllerID, PoolID, Name, GitHubURL, MetadataOriginDigest string
		RunnerID                                                    int64
	}{record.ControllerID, record.PoolID, record.Name, record.GitHubURL, record.MetadataOriginDigest, record.RunnerID})
	if err != nil {
		return reuse.StockBinding{}, ErrTeardownEvidence
	}
	sum := sha256.Sum256(encoding)
	return reuse.StockBinding{
		RegistrationDigest: hex.EncodeToString(sum[:]),
		ControllerID:       record.ControllerID,
		PoolID:             record.PoolID,
		GitHubURL:          record.GitHubURL,
		RunnerName:         record.Name,
		RunnerID:           record.RunnerID,
	}, nil
}

func recordValid(r stockjit.Record) bool {
	if r.ControllerID == "" || r.PoolID == "" || r.Name == "" || r.GitHubURL == "" || r.MetadataOriginDigest == "" || r.RunnerID <= 0 {
		return false
	}
	for _, v := range []string{r.ControllerID, r.PoolID, r.Name, r.GitHubURL, r.MetadataOriginDigest} {
		if len(v) > 256 || strings.IndexFunc(v, func(r rune) bool { return r < 33 || r == 127 }) >= 0 {
			return false
		}
	}
	decoded, err := hex.DecodeString(r.MetadataOriginDigest)
	return err == nil && len(decoded) == 32
}

// ChargeFromCreation anchors the original charge in durable ledger history:
// the exact reservation, the authenticated original-create incarnation and
// the stock registration. Volumes are the applicable exact charged allocation
// supplied by the trusted assembler; they are validated, never discovered here.
func ChargeFromCreation(req admission.Request, creation admission.CreateEvidence, stock reuse.StockBinding, volumes []reuse.VolumeCharge) (reuse.ChargeIdentity, error) {
	subject := creation.Observation.Identity.Subject
	if creation.Request != req || subject.ReservationKey != req.Key || subject.Name == "" {
		return reuse.ChargeIdentity{}, ErrTeardownEvidence
	}
	if stock.ControllerID != subject.ControllerID || stock.PoolID != subject.PoolID || stock.RunnerName != subject.Name {
		return reuse.ChargeIdentity{}, ErrTeardownEvidence
	}
	charge := reuse.ChargeIdentity{
		Ref: reuse.ChargeRef{
			Key:     req.Key,
			Pool:    req.Pool,
			Host:    req.Host,
			CPU:     req.Resources.CPU,
			RAM:     req.Resources.RAM,
			Disk:    req.Resources.Disk,
			Runners: req.Resources.Runners,
		},
		Stock: stock,
		Provider: reuse.ProviderBinding{
			BackendKeyFingerprint: subject.BackendKeyFingerprint,
			OriginFingerprint:     subject.OriginFingerprint,
			ScopeFingerprint:      subject.ScopeFingerprint,
			ServerName:            subject.ServerName,
			ServerVersion:         subject.ServerVersion,
			Project:               subject.Project,
			Member:                "",
		},
		Incarnation: reuse.IncarnationBinding{
			Name:       subject.Name,
			UUID:       subject.UUID,
			Generation: subject.Generation,
			CreatedAt:  subject.CreatedAt,
		},
		Volumes: volumes,
	}
	if !charge.Valid() {
		return reuse.ChargeIdentity{}, ErrTeardownEvidence
	}
	return charge, nil
}

// WorkloadProofFromLifecycle converts one positive scoped lifecycle
// observation into the reuse workload proof, binding it to the exact stock
// registration. An unscoped, unassigned or unverified observation never
// converts.
func WorkloadProofFromLifecycle(observation demandrepair.JobLifecycleObservation, stock reuse.StockBinding) (reuse.WorkloadProof, error) {
	if !observation.Valid() {
		return reuse.WorkloadProof{}, ErrTeardownEvidence
	}
	record := observation.Record()
	runner := record.Runner
	if runner.RunnerID != stock.RunnerID || runner.Name != stock.RunnerName || runner.GitHubURL != stock.GitHubURL ||
		runner.ControllerID != stock.ControllerID || runner.PoolID != stock.PoolID {
		return reuse.WorkloadProof{}, ErrTeardownEvidence
	}
	if record.Repository.Host == "" || record.Repository.Owner == "" || record.Repository.Name == "" {
		return reuse.WorkloadProof{}, ErrTeardownEvidence
	}
	proof := reuse.WorkloadProof{
		RepositoryHost:     record.Repository.Host,
		RepositoryOwner:    record.Repository.Owner,
		RepositoryName:     record.Repository.Name,
		JobID:              record.JobID,
		RunID:              record.RunID,
		RunAttempt:         record.RunAttempt,
		RunnerID:           record.RunnerID,
		RunnerName:         record.RunnerName,
		StartedAt:          record.StartedAt,
		ObservedAt:         record.ObservedAt,
		RegistrationDigest: stock.RegistrationDigest,
		SourceDigest:       record.SourceIdentity,
	}
	if record.CompletedAt != nil {
		proof.CompletedAt = *record.CompletedAt
		proof.Conclusion = record.Conclusion
	}
	if !proof.Valid() {
		return reuse.WorkloadProof{}, ErrTeardownEvidence
	}
	return proof, nil
}

// TeardownProofFromObservations binds the incarnation observation, the
// captured original terminal DELETE operation and the two-pass post-delete
// project absence into one teardown proof. Observations under any other
// incarnation, scope or version never validate.
func TeardownProofFromObservations(identity incusapi.InstanceIdentityObservation, delete incusapi.TerminalOperationObservation, absence incusapi.ProjectAbsenceObservation) (reuse.InstanceTeardownProof, error) {
	if !delete.Valid() || !absence.Valid() {
		return reuse.InstanceTeardownProof{}, ErrTeardownEvidence
	}
	proof := reuse.InstanceTeardownProof{
		Identity: identity,
		Delete:   delete.Record(),
		Absence:  absence.Record(),
	}
	if !proof.Valid() {
		return reuse.InstanceTeardownProof{}, ErrTeardownEvidence
	}
	return proof, nil
}

// TerminalEvidence assembles one delivered bundle for an anchored charge. A
// nil component simply means that evidence has not been delivered; Assess
// decides what stays charged.
func TerminalEvidence(charge reuse.ChargeIdentity, workload *reuse.WorkloadProof, teardown *reuse.InstanceTeardownProof, cleanup *reuse.CleanupProof, capturedAt time.Time) reuse.TerminalEvidence {
	return reuse.TerminalEvidence{Charge: charge, Workload: workload, Teardown: teardown, Cleanup: cleanup, CapturedAt: capturedAt}
}
