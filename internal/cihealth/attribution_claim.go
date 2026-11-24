package cihealth

import (
	"errors"
	"time"
)

// ErrAttributionUnsupported marks a job attribution claim that the documented
// capability matrix cannot verify. It is a fail-closed verdict, never a
// partial proof: unverifiable joins, absent origins and contradictory
// evidence all land here, and no resource number is derived from them.
var ErrAttributionUnsupported = errors.New("job attribution unsupported by documented source capabilities")

// StockRowEvidence is the exact authenticated GARM row observation for one
// running instance, mapped into this package's contract. Upstream packages
// hold the sealed short-lived reports; this struct carries only the released
// numeric/digest projection the join validator needs.
type StockRowEvidence struct {
	// SourceIdentity is the authenticated managed source identity digest.
	SourceIdentity string
	ControllerID   string
	// RepositoryID and PoolID are the exact GARM scope UUIDs; RowID is the
	// exact instance row UUID. None of them identifies a provider allocation.
	RepositoryID, PoolID, RowID string
	ProviderName                string
	OwnershipFingerprint        string
	AgentID                     int64
	ObservedAt                  time.Time
}

// EnrolledRowScope is the caller's separately approved exact enrollment for
// one GitHub source and immutable repository ID mapped to one exact GARM
// source/controller/repository/pool/provider. Constructing it is not
// enrollment; upstream approval authority mints it. A row observation only
// counts when it matches this scope exactly — matching fields alone never
// substitute for enrollment.
type EnrolledRowScope struct {
	GitHubSource       string
	GitHubRepositoryID int64

	SourceIdentity, ControllerID       string
	RepositoryID, PoolID               string
	ProviderName, OwnershipFingerprint string
}

func (e EnrolledRowScope) matches(job JobKey, row StockRowEvidence) bool {
	if !validSource(e.GitHubSource) || e.GitHubRepositoryID <= 0 ||
		e.GitHubSource != job.Attempt.Source || e.GitHubRepositoryID != job.Attempt.RepositoryID {
		return false
	}
	return e == row.scopeFor(e.GitHubSource, e.GitHubRepositoryID)
}

func (row StockRowEvidence) scopeFor(githubSource string, githubRepositoryID int64) EnrolledRowScope {
	return EnrolledRowScope{
		GitHubSource: githubSource, GitHubRepositoryID: githubRepositoryID,
		SourceIdentity: row.SourceIdentity, ControllerID: row.ControllerID, RepositoryID: row.RepositoryID,
		PoolID: row.PoolID, ProviderName: row.ProviderName, OwnershipFingerprint: row.OwnershipFingerprint,
	}
}

// VerifiedProviderOrigin is the documented shape of independently verified
// original provider allocation evidence: positive proof that one exact stock
// row's original allocation created one immutable Incus incarnation. No
// repository runtime currently produces this value. The stock row reports
// only a mutable provider name/ID for its allocation, and an Incus read
// proves only its chosen subject, so live claims cannot satisfy this
// contract today; the capability matrix reports the join as unsupported.
type VerifiedProviderOrigin struct {
	Row         StockRowEvidence
	Incarnation StockRunnerIncarnation
	// InstanceCreatedAt is the immutable instance creation time reported by
	// the authenticated daemon for the originally allocated incarnation.
	InstanceCreatedAt time.Time
	// AllocationObservedAt is when the original allocation was positively
	// verified, not when this record was projected.
	AllocationObservedAt time.Time
}

// JobAttributionClaim assembles the separately authenticated evidence reads
// for one exact job. Every member must have been authenticated by its own
// source; this package only checks internal consistency and the documented
// join. The zero Origin carries no provider-origin proof and the zero
// Enrolled carries no approved repository scope: both are mandatory.
type JobAttributionClaim struct {
	Job         JobKey
	Enrolled    EnrolledRowScope
	Execution   StockJobExecutionObservation
	Row         StockRowEvidence
	Incarnation StockRunnerIncarnation
	Origin      VerifiedProviderOrigin
}

// VerifiedAttribution is a claim the matrix and validators accepted: exact
// job, positive registration, exact enrolled row, and one immutable
// incarnation all proven consistent, with positive original-allocation
// evidence.
type VerifiedAttribution struct {
	Job           JobKey
	Enrolled      EnrolledRowScope
	AgentID       int64
	SourceID      string
	PoolID        string
	RowID         string
	Incarnation   StockRunnerIncarnation
	Origin        VerifiedProviderOrigin
	StartedAt     time.Time
	ExecutionEnds bool
}

func validRunnerIncarnationFacts(r StockRunnerIncarnation) bool {
	return stockJobDigest(r.PeerFingerprint) && stockJobLocator(r.Member) && stockJobLocator(r.Project) &&
		stockJobLocator(r.InstanceName) && stockJobUUID(r.UUID) && stockJobUUID(r.Generation) &&
		stockJobUUID(r.BootID) && r.ProcessStartTicks != 0 && validTime(r.StartedAt.UTC())
}

func wellFormedStockRowEvidence(row StockRowEvidence) bool {
	return stockJobDigest(row.SourceIdentity) && stockJobUUID(row.ControllerID) &&
		stockJobUUID(row.RepositoryID) && stockJobUUID(row.PoolID) && stockJobUUID(row.RowID) &&
		stockJobLocator(row.ProviderName) && stockJobDigest(row.OwnershipFingerprint) &&
		row.AgentID > 0 && validTime(row.ObservedAt.UTC())
}

func validStockRowEvidence(row StockRowEvidence, now time.Time) bool {
	return wellFormedStockRowEvidence(row) && !row.ObservedAt.UTC().After(now.UTC())
}

func validVerifiedProviderOrigin(o VerifiedProviderOrigin, now time.Time) bool {
	if o.Row == (StockRowEvidence{}) || o.Incarnation == (StockRunnerIncarnation{}) {
		return false
	}
	return validStockRowEvidence(o.Row, now) && validRunnerIncarnationFacts(o.Incarnation) &&
		validTime(o.InstanceCreatedAt.UTC()) && validTime(o.AllocationObservedAt.UTC()) &&
		!o.InstanceCreatedAt.UTC().After(o.Incarnation.StartedAt.UTC()) &&
		!o.AllocationObservedAt.UTC().Before(o.InstanceCreatedAt.UTC()) &&
		!o.AllocationObservedAt.UTC().After(now.UTC())
}

// VerifyJobAttribution validates one assembled claim against the documented
// capability matrix and fails closed. The exact job read must prove the job
// identity and positive registration; the row evidence must prove the exact
// enrolled row and agent, positively observed no earlier than the job start;
// the incarnation must be well-formed; the agent identities must agree; and
// the provider-origin join must be positively proven. An absent or zero
// Origin cannot be replaced by a name, mutable provider label, timing, or the
// separate TLS authentication of the two reads, and a zero or mismatched
// Enrolled scope cannot be replaced by fields that happen to match.
func VerifyJobAttribution(matrix AttributionCapabilityMatrix, claim JobAttributionClaim, now time.Time) (VerifiedAttribution, error) {
	var none VerifiedAttribution
	if !matrix.Supports(FactExactJobIdentity) || !matrix.Supports(FactPositiveRunnerRegistration) ||
		!matrix.Supports(FactExactStockRow) || !matrix.Supports(FactPositiveStockRowAgent) ||
		!matrix.Supports(FactIncusIncarnation) || !matrix.Supports(FactProviderOriginJoin) || now.IsZero() {
		return none, ErrAttributionUnsupported
	}
	job := claim.Job
	execution, row, incarnation := claim.Execution, claim.Row, claim.Incarnation
	started, observed := execution.StartedAt.UTC(), execution.ObservedAt.UTC()
	if !validKey(job.Attempt) || job.JobID <= 0 || execution.Job != job || execution.RunnerAgentID <= 0 ||
		execution.State == StockJobExecutionUnknown || !validTime(started) || !validTime(observed) ||
		observed.Before(started) || observed.UTC().After(now.UTC()) {
		return none, ErrAttributionUnsupported
	}
	ended := execution.EndedAt.UTC()
	if execution.State == StockJobObservedEnded && (!validTime(ended) || !ended.After(started) || ended.After(observed)) {
		return none, ErrAttributionUnsupported
	}
	if execution.State == StockJobObservedExecuting && !ended.IsZero() {
		return none, ErrAttributionUnsupported
	}
	// The exact enrolled repository scope is mandatory: a row observed under
	// any other enrollment, or before the job started, cannot positively
	// place this runner on this job no matter how well the fields match.
	if !claim.Enrolled.matches(job, row) ||
		row.ObservedAt.UTC().Before(started) {
		return none, ErrAttributionUnsupported
	}
	if !validStockRowEvidence(row, now) || row.AgentID != execution.RunnerAgentID {
		return none, ErrAttributionUnsupported
	}
	if !validRunnerIncarnationFacts(incarnation) {
		return none, ErrAttributionUnsupported
	}
	// Cross-runner, cross-attempt and cross-repository claims die here: only
	// the positive origin can join this exact row to this exact incarnation,
	// and it must match the supplied evidence field for field.
	origin := claim.Origin
	if !validVerifiedProviderOrigin(origin, now) || origin.Row != row || origin.Incarnation != incarnation {
		return none, ErrAttributionUnsupported
	}
	return VerifiedAttribution{
		Job: job, Enrolled: claim.Enrolled, AgentID: row.AgentID, SourceID: row.SourceIdentity,
		PoolID: row.PoolID, RowID: row.RowID, Incarnation: incarnation, Origin: origin,
		StartedAt: started, ExecutionEnds: execution.State == StockJobObservedEnded,
	}, nil
}
