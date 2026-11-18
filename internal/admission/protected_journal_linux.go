package admission

import (
	"context"
	"database/sql"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"time"
)

// ProtectedJobCorrelation is read-only, explicitly never retirement authority.
type ProtectedJobCorrelation struct {
	PermitID, ResourceID, JournalState string
	JobID, RunID, RunAttempt           int64
	JobConclusion                      string
	RootJournalAndTerminalJobObserved  bool
	AcceptanceFenceQualified           bool
	PhysicalDeletionProven             bool
	ReleaseAuthorized                  bool
	Blockers                           []string
}

func (s *SQLiteStore) CorrelateProtectedJournal(ctx context.Context, observation nativeidentity.ProtectedJournalObservation, policy nativeapi.Policy) (ProtectedJobCorrelation, error) {
	if !observation.Valid() {
		return ProtectedJobCorrelation{}, ErrNativeJob
	}
	var out ProtectedJobCorrelation
	e := s.readTransaction(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = s.correlateProtectedJournal(tx, observation, policy)
		return e
	})
	return out, e
}
func (s *SQLiteStore) correlateProtectedJournal(tx *sql.Tx, observation nativeidentity.ProtectedJournalObservation, policy nativeapi.Policy) (ProtectedJobCorrelation, error) {
	r := observation.Record()
	var out ProtectedJobCorrelation
	e := func() error {
		binding, e := nativeRunnerBinding(tx, r.PermitID)
		if e != nil {
			return e
		}
		env, eh, e := nativeEnvelopeRecord(tx, r.PermitID)
		if e != nil {
			return e
		}
		domain, e := nativeDomainRecord(tx, env.DomainBindingSequence)
		if e != nil {
			return e
		}
		image, ok := policy.ProtectedImage(env.Pool)
		if !ok || policy.Digest() != env.PolicyDigest || r.InitSHA256 != image.InitSHA256 || r.DomainID != env.DomainID || r.PolicyDigest != env.PolicyDigest || r.EnvelopeDigest != eh || r.ResourceID != binding.ResourceID || r.NativeName != env.NativeName || r.ImageFingerprint != env.Execution.Fingerprint || observation.OwnerServerLeaf() != domain.Spec.OwnerServerLeaf || observation.ObservedAt().After(s.now()) || s.now().Sub(observation.ObservedAt()) > time.Minute {
			return ErrNativeJob
		}
		_, ready, epoch, _, e := sqliteMetadata(tx)
		if e != nil || !ready || epoch != env.ReconciliationEpoch {
			return ErrFence
		}
		start, e := nativeOperationByStep(tx, r.PermitID, NativeStart)
		if e != nil || r.RecordAt.Before(start.ClaimedAt) {
			return ErrNativeJob
		}
		job, e := nativeJobRecord(tx, r.PermitID, "completed")
		if e != nil {
			return e
		}
		out = ProtectedJobCorrelation{PermitID: r.PermitID, ResourceID: r.ResourceID, JournalState: r.State, JobID: job.Observation.JobID, RunID: job.Observation.RunID, RunAttempt: job.Observation.RunAttempt, JobConclusion: job.Observation.Conclusion, RootJournalAndTerminalJobObserved: true, Blockers: []string{"guest_containment_unqualified", "job_acceptance_fence_unqualified", "physical_deletion_unproven"}}
		if r.State != "process_tree_exited_unqualified" {
			out.Blockers = append(out.Blockers, "guest_process_tree_exit_not_observed")
		}
		// Retain the exact journal classification; even a zero exit is not completion
		// authority. No writes, lease renewal, claim or charge changes occur here.
		return nil
	}()
	if e != nil {
		return ProtectedJobCorrelation{}, e
	}
	return out, nil
}
