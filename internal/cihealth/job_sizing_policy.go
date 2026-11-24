package cihealth

import (
	"cmp"
	"slices"
	"time"
)

// JobSizingCohortPolicyVersion identifies the explicit comparability policy
// applied before the sizing reducer runs. A recommendation is only
// reproducible under the policy version that produced it.
const JobSizingCohortPolicyVersion = "job-sizing-cohort-policy/v1"

// SizingRejectionReason names why one offered candidate is not eligible for
// the reviewed cohort. Nothing is silently dropped: every rejected candidate
// stays visible in the projection with its exact job key.
type SizingRejectionReason string

const (
	SizingRejectionUnverifiableIdentity SizingRejectionReason = "unverifiable_identity"
	SizingRejectionForeignScope         SizingRejectionReason = "foreign_scope"
	SizingRejectionIncomparableIdentity SizingRejectionReason = "incomparable_identity"
	SizingRejectionProfileChanged       SizingRejectionReason = "profile_changed"
	SizingRejectionStale                SizingRejectionReason = "stale"
	SizingRejectionDuplicate            SizingRejectionReason = "duplicate"
)

// SizingCohortIdentity is the trusted comparability identity a verified
// producer attaches to each sample under its exact job key. The policy only
// checks presence and equality; it cannot establish that the labels are
// true, and it never infers identity from job names, labels or timing.
type SizingCohortIdentity struct {
	// Matrix is the trusted opaque matrix leg identity. Empty means the
	// reviewed workflow selection has no matrix axis.
	Matrix string
	// Profile is the installed runner profile identity the sample executed
	// under. At least one of Profile or Revision is required.
	Profile string
	// Revision is the relevant runner profile or configuration revision.
	Revision string
}

// SizingCandidate is one verified sample offered for cohort eligibility
// review together with the comparability identity retained under the same
// exact job key. Candidates are never joined across identity, scope, shape
// or window boundaries.
type SizingCandidate struct {
	Identity SizingCohortIdentity
	Sample   JobSizingSample
}

// SizingRejection keeps one rejected candidate visible with its reason.
type SizingRejection struct {
	Job    JobKey
	Reason SizingRejectionReason
}

// SizingCohortProjection is the deterministic result of applying the
// eligibility policy to the offered candidates. The cohort contains exactly
// the eligible samples for the requested scope, identity and installed
// shape; Incomparable counts in-scope candidates that belong to a different
// trusted identity and were not combined.
type SizingCohortProjection struct {
	Version      string
	Scope        TrendScope
	Identity     SizingCohortIdentity
	Coverage     TrendCoverage
	Cohort       JobSizingCohort
	Rejected     []SizingRejection
	Incomparable int
}

// ProjectJobSizingCohort applies the explicit eligibility policy before the
// sizing reducer runs. A candidate is eligible only when its exact job key is
// well formed, it belongs to the requested source/repository/workflow scope,
// it carries the requested trusted matrix/profile/revision identity, it
// matches the reviewed installed shape, it falls fully inside the review
// window, and it is not a duplicate execution. Stale, foreign, unverifiable
// or incomparable candidates are reported, never joined and never silently
// dropped, so sparse or cross-tenant input cannot manufacture a confident
// cohort. Eligible samples are sorted by exact job key, making the
// projection deterministic for any input order.
func ProjectJobSizingCohort(scope TrendScope, identity SizingCohortIdentity, installed JobSizingShape, coverage TrendCoverage, reviewed bool, window JobSizingReviewWindow, candidates []SizingCandidate) (SizingCohortProjection, error) {
	if !validSource(scope.Source) || scope.RepositoryID <= 0 || scope.WorkflowID <= 0 ||
		coverage > TrendCoverageComplete || installed.CPUMilli == 0 || installed.RAMBytes == 0 ||
		!validSizingIdentity(identity) || len(candidates) > MaxTrendAttempts || !validSizingReviewWindow(window) {
		return SizingCohortProjection{}, ErrJobSizingFacts
	}
	out := SizingCohortProjection{
		Version:  JobSizingCohortPolicyVersion,
		Scope:    scope,
		Identity: identity,
		Coverage: coverage,
		Cohort: JobSizingCohort{
			Scope:         scope,
			Comparability: JobSizingComparabilityUnknown,
			Coverage:      coverage,
			Shape:         installed,
		},
		Rejected: []SizingRejection{},
	}
	if reviewed {
		out.Cohort.Comparability = JobSizingReviewedComparable
	}
	window.From, window.AsOf = window.From.UTC(), window.AsOf.UTC()
	seenKey, seenJob := make(map[AttemptKey]bool, len(candidates)), make(map[int64]bool, len(candidates))
	samples := make([]JobSizingSample, 0, len(candidates))
	for _, candidate := range candidates {
		job := candidate.Sample.Resources.Scope.Job
		reject := func(reason SizingRejectionReason) {
			out.Rejected = append(out.Rejected, SizingRejection{Job: job, Reason: reason})
		}
		if !validKey(job.Attempt) || job.JobID <= 0 || !validSizingIdentity(candidate.Identity) {
			reject(SizingRejectionUnverifiableIdentity)
			continue
		}
		key := job.Attempt
		if key.Source != scope.Source || key.RepositoryID != scope.RepositoryID || key.WorkflowID != scope.WorkflowID {
			reject(SizingRejectionForeignScope)
			continue
		}
		if candidate.Identity != identity {
			reject(SizingRejectionIncomparableIdentity)
			out.Incomparable++
			continue
		}
		if candidate.Sample.Shape != installed {
			reject(SizingRejectionProfileChanged)
			continue
		}
		s := candidate.Sample.Resources.Scope
		if sizingCandidateStale(s, candidate.Sample.Completion, window) {
			reject(SizingRejectionStale)
			continue
		}
		if seenKey[key] || seenJob[job.JobID] {
			reject(SizingRejectionDuplicate)
			continue
		}
		seenKey[key], seenJob[job.JobID] = true, true
		samples = append(samples, candidate.Sample)
	}
	slices.SortFunc(samples, func(a, b JobSizingSample) int { return compareJobKey(a.Resources.Scope.Job, b.Resources.Scope.Job) })
	slices.SortFunc(out.Rejected, func(a, b SizingRejection) int { return compareJobKey(a.Job, b.Job) })
	out.Cohort.Samples = samples
	return out, nil
}

// validSizingIdentity accepts the trusted labels only in the bounded opaque
// label form and requires at least the installed profile or its revision so
// a cohort can never form around an unknown profile.
func validSizingIdentity(identity SizingCohortIdentity) bool {
	valid := func(label string) bool {
		if len(label) > 128 {
			return false
		}
		for i, c := range label {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_' || c == '.') {
				continue
			}
			return false
		}
		return true
	}
	return valid(identity.Matrix) && valid(identity.Profile) && valid(identity.Revision) &&
		(identity.Profile != "" || identity.Revision != "")
}

// validSizingHostLabel accepts a display host identity in bounded locator
// form. It is a label check only, never host authentication.
func validSizingHostLabel(host string) bool {
	if len(host) == 0 || len(host) > 128 {
		return false
	}
	for i, c := range host {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_' || c == '.') {
			continue
		}
		return false
	}
	return true
}

func validSizingReviewWindow(w JobSizingReviewWindow) bool {
	span := w.AsOf.Sub(w.From)
	return validTime(w.From) && validTime(w.AsOf) && span > 0 && span <= MaxJobSizingReviewWindow &&
		w.LatestCompletionMaxAge > 0 && w.LatestCompletionMaxAge <= span
}

// sizingCandidateStale reports whether any part of the candidate falls
// outside the review window. The review consumer refuses out-of-window
// evidence, so eligibility must select the window up front and report what
// it excluded instead of letting one bad candidate invalidate the review.
func sizingCandidateStale(s JobResourceScope, completion JobSizingCompletion, w JobSizingReviewWindow) bool {
	if s.StartedAt.UTC().Before(w.From) || s.EndAt.UTC().After(w.AsOf) || s.AsOf.UTC().After(w.AsOf) {
		return true
	}
	return validTime(completion.ObservedAt) && completion.ObservedAt.UTC().After(w.AsOf)
}

func compareJobKey(a, b JobKey) int {
	if c := compareAttemptKey(a.Attempt, b.Attempt); c != 0 {
		return c
	}
	return cmp.Compare(a.JobID, b.JobID)
}

func compareAttemptKey(a, b AttemptKey) int {
	if c := cmp.Compare(a.Source, b.Source); c != 0 {
		return c
	}
	if c := cmp.Compare(a.RepositoryID, b.RepositoryID); c != 0 {
		return c
	}
	if c := cmp.Compare(a.WorkflowID, b.WorkflowID); c != 0 {
		return c
	}
	if c := cmp.Compare(a.RunID, b.RunID); c != 0 {
		return c
	}
	return cmp.Compare(a.Attempt, b.Attempt)
}

// sizingCleanSample mirrors the reducer's full-window qualification gate for
// one sample: a fully evidenced primary execution ending exactly at its
// resource window end. Retried attempts, risk evidence, missing outcome or
// risk review, an unconfirmed completion, or a report-cutoff prefix all fail
// the gate. Their metrics may remain valid display facts, but they cannot
// qualify for sizing or disk attribution.
func sizingCleanSample(sample JobSizingSample, completion time.Time) bool {
	s := sample.Resources.Scope
	if s.Job.Attempt.Attempt != 1 || sample.Risks != 0 || completion.IsZero() || !sample.RiskKnown ||
		sample.Outcome == TrendOutcomeUnknown || sample.Outcome != TrendOutcomeSuccess ||
		!s.EndAt.UTC().Equal(completion) {
		return false
	}
	return true
}
