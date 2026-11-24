package reuse

import (
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// qualifiedConclusions are the terminal job conclusions accepted as positive
// completion evidence. They mirror the scoped lifecycle observation contract.
var qualifiedConclusions = map[string]bool{
	"success": true, "failure": true, "neutral": true, "cancelled": true,
	"skipped": true, "timed_out": true, "action_required": true, "stale": true,
}

// WorkloadProof is positive, scoped lifecycle evidence: the exact assigned
// job completed on the exact registered runner, observed through the
// authenticated source. A missing job, a queued or in-progress assignment,
// a different runner identity or an unscoped read never validate. Timeout,
// delete acknowledgement, depleted pages and missing rows are not workload
// evidence and cannot mint this proof.
type WorkloadProof struct {
	RepositoryHost  string    `json:"repository_host"`
	RepositoryOwner string    `json:"repository_owner"`
	RepositoryName  string    `json:"repository_name"`
	JobID           int64     `json:"job_id"`
	RunID           int64     `json:"run_id"`
	RunAttempt      int64     `json:"run_attempt"`
	RunnerID        int64     `json:"runner_id"`
	RunnerName      string    `json:"runner_name"`
	Conclusion      string    `json:"conclusion"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at"`
	ObservedAt      time.Time `json:"observed_at"`
	// SourceDigest binds the exact observation authority (the authenticated
	// API origin digest). RegistrationDigest binds the stock registration
	// record this workload ran under.
	SourceDigest       string `json:"source_digest"`
	RegistrationDigest string `json:"registration_digest"`
}

func (p WorkloadProof) valid() bool {
	if !segment(p.RepositoryHost) || !segment(p.RepositoryOwner) || !segment(p.RepositoryName) ||
		p.JobID <= 0 || p.RunID <= 0 || p.RunAttempt <= 0 || p.RunnerID <= 0 ||
		!label(p.RunnerName) || !qualifiedConclusions[p.Conclusion] {
		return false
	}
	if p.StartedAt.IsZero() || p.CompletedAt.Before(p.StartedAt) || p.ObservedAt.Before(p.CompletedAt) {
		return false
	}
	return digest64(p.SourceDigest) && digest64(p.RegistrationDigest)
}

// Valid is grammar and chronology only; matching against the charge happens
// in TerminalEvidence.matches.
func (p WorkloadProof) Valid() bool { return p.valid() }

// InstanceTeardownProof binds the immutable incarnation observation, the
// captured original terminal DELETE operation for that same incarnation and
// the two-pass post-delete project absence read. The delete acknowledgement
// alone, a later 404, or a single read never validate: absence must span
// two matching passes over collections that reject the subject incarnation.
type InstanceTeardownProof struct {
	Identity incusapi.InstanceIdentityObservation `json:"identity"`
	Delete   incusapi.TerminalOperationRecord     `json:"delete"`
	Absence  incusapi.ProjectAbsenceRecord        `json:"absence"`
}

func (p InstanceTeardownProof) valid() bool {
	identity, teardown, absence := p.Identity, p.Delete, p.Absence
	subject := identity.Subject
	if !digest64(subject.BackendKeyFingerprint) || !digest64(subject.OriginFingerprint) ||
		!digest64(subject.ScopeFingerprint) || !label(subject.ServerName) ||
		subject.ServerVersion != "6.0.4" || !label(subject.Project) ||
		!instanceName(subject.Name) || !incarnationID(subject.UUID) ||
		!incarnationID(subject.Generation) || subject.CreatedAt.IsZero() ||
		!label(subject.ControllerID) || !label(subject.PoolID) ||
		!digest64(subject.ReservationKey) || subject.CPUMillis <= 0 || subject.RAMMiB <= 0 {
		return false
	}
	if identity.ObservedAt.IsZero() || canonicalDigest(subject) != identity.SubjectFingerprint {
		return false
	}
	if teardown.Kind != incusapi.RecoveryDelete || teardown.Class != "task" ||
		teardown.Description != "Deleting instance" || teardown.StatusCode != 200 ||
		!operationUUID(teardown.OperationID) || teardown.CreatedAt.IsZero() ||
		teardown.UpdatedAt.Before(teardown.CreatedAt) || teardown.CapturedAt.IsZero() ||
		!teardown.RequiresExclusiveFence {
		return false
	}
	if teardown.Identity.SubjectFingerprint != identity.SubjectFingerprint ||
		teardown.Identity.Subject != subject {
		return false
	}
	if !absence.ProjectOnly || absence.SnapshotAtomic || absence.MatchingPasses != 2 ||
		!digest64(absence.InstanceInventoryFingerprint) ||
		!digest64(absence.OperationInventoryFingerprint) ||
		absence.StartedAt.IsZero() || !absence.CompletedAt.After(absence.StartedAt) ||
		absence.Identity.SubjectFingerprint != identity.SubjectFingerprint ||
		absence.Identity.Subject != subject {
		return false
	}
	// Chronology: the incarnation is observed before the delete is captured,
	// and the delete is captured before the post-delete absence completes.
	if teardown.CapturedAt.Before(identity.ObservedAt) ||
		absence.StartedAt.Before(identity.ObservedAt) ||
		absence.CompletedAt.Before(teardown.CapturedAt) {
		return false
	}
	return true
}

// Valid is grammar, binding and chronology only; matching against the charge
// happens in TerminalEvidence.matches.
func (p InstanceTeardownProof) Valid() bool { return p.valid() }

// CapabilityKind is an audited provider capability. Capabilities are
// recorded only from an audited provider claim, never assumed for all
// providers or inferred from behavior.
type CapabilityKind string

// CapabilityInstanceStorageInseparable records that the audited provider
// proved an instance and its storage are inseparable: the terminal instance
// delete removes the charged storage with it.
const CapabilityInstanceStorageInseparable CapabilityKind = "instance_storage_inseparable"

// AuditedCapability is one provider capability with the exact scope it was
// audited under. It never transfers across providers, daemons or versions.
type AuditedCapability struct {
	Kind        CapabilityKind `json:"kind"`
	ScopeDigest string         `json:"scope_digest"`
	AuditedAt   time.Time      `json:"audited_at"`
}

// VolumeAbsence is the positive observation that one exact charged volume is
// absent from the authenticated project collection. A missing row in an
// unverified identity, a depleted page or a failed read never validates.
type VolumeAbsence struct {
	Name        string    `json:"name"`
	ScopeDigest string    `json:"scope_digest"`
	ObservedAt  time.Time `json:"observed_at"`
}

// CleanupProof is the exact cleanup state of the applicable charged
// allocation: every charged volume positively observed absent, or an audited
// provider capability that proves instance and storage are inseparable.
// Where a provider verifies only some resources, the unverified dimension
// stays charged.
type CleanupProof struct {
	Volumes    []VolumeAbsence    `json:"volumes"`
	Capability *AuditedCapability `json:"capability,omitempty"`
}

func (p CleanupProof) valid() bool {
	if len(p.Volumes) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range p.Volumes {
		if !volumeName(v.Name) || !digest64(v.ScopeDigest) || v.ObservedAt.IsZero() || seen[v.Name] {
			return false
		}
		seen[v.Name] = true
	}
	if p.Capability != nil {
		if p.Capability.Kind != CapabilityInstanceStorageInseparable ||
			!digest64(p.Capability.ScopeDigest) || p.Capability.AuditedAt.IsZero() {
			return false
		}
	}
	return len(p.Volumes) > 0 || p.Capability != nil
}

// Valid is grammar only; matching against the charge happens in
// TerminalEvidence.matches.
func (p CleanupProof) Valid() bool { return p.valid() }

// TerminalEvidence is one delivered teardown evidence bundle for exactly one
// original charge. It authorizes nothing by itself: Assess decides which
// dimensions the bundle qualifies, deterministically and fail-closed.
type TerminalEvidence struct {
	Charge   ChargeIdentity         `json:"charge"`
	Workload *WorkloadProof         `json:"workload,omitempty"`
	Teardown *InstanceTeardownProof `json:"teardown,omitempty"`
	Cleanup  *CleanupProof          `json:"cleanup,omitempty"`
	// CapturedAt is local receipt time of this bundle; it orders delivery
	// only and never attests remote lifecycle or clock authority.
	CapturedAt time.Time `json:"captured_at"`
}

// components reports which proofs are present and valid.
func (e TerminalEvidence) components() (workload, teardown, cleanup bool) {
	return e.Workload != nil && e.Workload.valid(),
		e.Teardown != nil && e.Teardown.valid(),
		e.Cleanup != nil && e.Cleanup.valid()
}

// Valid reports whether at least one component validates; full qualification
// is decided by Assess against the anchored charge.
func (e TerminalEvidence) Valid() bool {
	if e.Charge.valid() && !e.CapturedAt.IsZero() {
		w, t, c := e.components()
		return w || t || c
	}
	return false
}

// matches reports whether this evidence addresses the anchored charge: same
// charge, and every present component bound into it.
func (e TerminalEvidence) matches(charge ChargeIdentity) bool {
	if !e.Charge.valid() || !SameCharge(e.Charge, charge) {
		return false
	}
	if w, _, _ := e.components(); w {
		proof := e.Workload
		host, owner, name, ok := parseGitHubURL(charge.Stock.GitHubURL)
		if !ok || proof.RepositoryHost != host || proof.RepositoryOwner != owner ||
			proof.RepositoryName != name ||
			proof.RunnerID != charge.Stock.RunnerID || proof.RunnerName != charge.Stock.RunnerName ||
			proof.RegistrationDigest != charge.Stock.RegistrationDigest {
			return false
		}
	}
	if _, t, _ := e.components(); t {
		subject := e.Teardown.Identity.Subject
		if subject.Name != charge.Incarnation.Name || subject.UUID != charge.Incarnation.UUID ||
			subject.Generation != charge.Incarnation.Generation ||
			!subject.CreatedAt.Equal(charge.Incarnation.CreatedAt) ||
			subject.BackendKeyFingerprint != charge.Provider.BackendKeyFingerprint ||
			subject.OriginFingerprint != charge.Provider.OriginFingerprint ||
			subject.ScopeFingerprint != charge.Provider.ScopeFingerprint ||
			subject.Project != charge.Provider.Project ||
			subject.PoolID != charge.Stock.PoolID || subject.ControllerID != charge.Stock.ControllerID ||
			subject.ReservationKey != charge.Ref.Key ||
			subject.CPUMillis != charge.Ref.CPU || subject.RAMMiB != charge.Ref.RAM {
			return false
		}
	}
	if _, _, c := e.components(); c {
		for _, v := range e.Cleanup.Volumes {
			if v.ScopeDigest != charge.Provider.ScopeFingerprint {
				return false
			}
		}
		if e.Cleanup.Capability != nil && e.Cleanup.Capability.ScopeDigest != charge.Provider.ScopeFingerprint {
			return false
		}
	}
	return true
}

// Digest identifies the bundle for dedupe and exactly-once delivery.
func (e TerminalEvidence) Digest() string { return canonicalDigest(e) }
