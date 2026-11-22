package cihealthreport

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const MaxComparisonObservations = 2000

// Refuse a limit change that would let a fully timed cohort overflow Duration.
const _ time.Duration = MaxComparisonObservations * MaxDuration

var ErrCaseComparison = errors.New("case comparison lacks bounded comparable evidence")

// ReviewedCaseCohort contains out-of-band canonical identifiers. All IDs
// must be positive; none may derive from report text, position, or observed
// outcome. Compare one source, repository, workflow, revision, configuration,
// runner class, and shard at a time. ShardKey must have been independently
// bound to ShardID by the reviewer; it never leaves this API as output.
type ReviewedCaseCohort struct {
	Source          string
	RepositoryID    int64
	WorkflowID      int64
	CaseID          int64
	ConfigurationID int64
	RevisionID      int64
	RunnerClassID   int64
	ShardID         int64
	ShardKey        string
}

// ReviewedOccurrence is supplied only by the separately authorized review
// source after binding this canonical case to the exact artifact contents,
// run attempt, producer job, shard, and retry. Occurrence locates a row in
// that exact report; it is not a canonical test identity across reports.
// These are validated caller facts, not credentials or a trust minting API.
type ReviewedOccurrence struct {
	Cohort     ReviewedCaseCohort
	ArtifactID int64
	Job        cihealth.JobKey
	Occurrence int
	Retry      uint16 // zero means a positively reviewed initial test execution
	// ExpectedReportSHA256 must be independently obtained for the exact
	// extracted XML entry. An enclosing archive hash cannot stand in for it.
	ExpectedReportSHA256 [sha256.Size]byte
}

type ReviewedCaseObservation struct {
	Report Report
	Review *ReviewedOccurrence
}

// Frequency returns exact integer counts. Denominator zero means
// "insufficient observations," not a measured zero rate.
type Frequency struct {
	Numerator, Denominator int
}

type CaseComparisonStats struct {
	Summary
	// TotalTimedDuration is the exact sum of timed occurrences. It is
	// unavailable when Timed is zero, and is not elapsed workflow time.
	TotalTimedDuration      time.Duration
	FailureAmongPassFail    Frequency
	ErrorAmongPassFailError Frequency
	SlowAmongTimed          Frequency // populated only for a supplied positive threshold
}

// CaseComparison exposes fixed computed data only. A first execution means a
// positively reviewed GitHub attempt 1 with test retry 0. Any other reviewed
// execution is repeat evidence, never another independent run. Independent
// contrast is a suspected intermittent result, not a causal flaky diagnosis.
type CaseComparison struct {
	All, First, Repeat            CaseComparisonStats
	DistinctRuns                  int
	DistinctRunsWithRepeat        int
	IndependentContrast           bool      // at least one distinct first-pass run and one distinct first-failure run
	RecoveryAmongRetestedFailures Frequency // first failures with a later decisive repeat, same run and shard
}

type comparisonRun struct {
	first     Outcome
	follow    bool
	decisive  bool
	recovered bool
}

type executionSlot struct {
	run, attempt int64
	retry        uint16
}

// Within one source/repository category the numeric job ID identifies one
// GitHub job, not one job per run or attempt. Distinct framework retries and
// case occurrences in that same job still share its original attempt key.
type reviewedJobAttempts map[int64]cihealth.AttemptKey

func (jobs reviewedJobAttempts) accept(job cihealth.JobKey) bool {
	if attempt, seen := jobs[job.JobID]; seen && attempt != job.Attempt {
		return false
	}
	jobs[job.JobID] = job.Attempt
	return true
}

// CompareReviewedCase validates the supplied review against the opaque report
// projection's exact independently supplied binding. It does not fetch,
// authenticate, or discover such reviews: GitHub artifact metadata alone is
// insufficient to use this function. Missing, conflicting, duplicate, or
// foreign observations reject the entire requested comparison. slowAtOrAbove
// zero leaves the slow frequency unavailable; positive thresholds are inclusive.
func CompareReviewedCase(cohort ReviewedCaseCohort, observations []ReviewedCaseObservation, slowAtOrAbove time.Duration) (CaseComparison, error) {
	if !validCaseCohort(cohort) || len(observations) == 0 || len(observations) > MaxComparisonObservations ||
		slowAtOrAbove < 0 || slowAtOrAbove > MaxDuration {
		return CaseComparison{}, ErrCaseComparison
	}
	slots := make(map[executionSlot]bool, len(observations))
	artifacts := make(map[int64]bool, len(observations))
	jobs := make(reviewedJobAttempts, len(observations))
	runs := make(map[int64]comparisonRun, len(observations))
	var all, first, repeat comparisonBatch
	for _, input := range observations {
		c, slot, artifact, ok := comparableOccurrence(cohort, input)
		// One canonical occurrence per positively identified shard and
		// attempt/retry slot. An artifact cannot assign two records to
		// this canonical case; there is no silent coalescing or ordering.
		if !ok || slots[slot] || artifacts[artifact] || !jobs.accept(input.Review.Job) {
			return CaseComparison{}, ErrCaseComparison
		}
		slots[slot], artifacts[artifact] = true, true
		run := runs[slot.run]
		all.add(c, slowAtOrAbove)
		if slot.attempt == 1 && slot.retry == 0 {
			first.add(c, slowAtOrAbove)
			run.first = c.Outcome
		} else {
			repeat.add(c, slowAtOrAbove)
			run.follow = true
			if c.Outcome == Pass || c.Outcome == Failure {
				run.recovered = run.recovered || c.Outcome == Pass
				// Distinguish a repeat attempt with no decisive test result.
				run.decisive = true
			}
		}
		runs[slot.run] = run
	}

	out := CaseComparison{
		All: all.stats(slowAtOrAbove), First: first.stats(slowAtOrAbove), Repeat: repeat.stats(slowAtOrAbove),
		DistinctRuns: len(runs), IndependentContrast: first.totals.Passed > 0 && first.totals.Failed > 0,
	}
	for _, run := range runs {
		if run.follow {
			out.DistinctRunsWithRepeat++
		}
		if run.first == Failure && run.decisive {
			out.RecoveryAmongRetestedFailures.Denominator++
			if run.recovered {
				out.RecoveryAmongRetestedFailures.Numerator++
			}
		}
	}
	return out, nil
}

func validCaseCohort(c ReviewedCaseCohort) bool {
	return sourceAtom(c.Source) && sourceAtom(c.ShardKey) && c.RepositoryID > 0 &&
		c.WorkflowID > 0 && c.CaseID > 0 && c.ConfigurationID > 0 &&
		c.RevisionID > 0 && c.RunnerClassID > 0 && c.ShardID > 0
}

func comparableOccurrence(cohort ReviewedCaseCohort, in ReviewedCaseObservation) (Case, executionSlot, int64, bool) {
	fail := func() (Case, executionSlot, int64, bool) { return Case{}, executionSlot{}, 0, false }
	if in.Review == nil {
		return fail()
	}
	review := *in.Review
	b := in.Report.binding
	j := review.Job
	a := j.Attempt
	n := review.Occurrence
	if review.Cohort != cohort || n <= 0 || n > len(in.Report.cases) ||
		review.ExpectedReportSHA256 == ([sha256.Size]byte{}) ||
		subtle.ConstantTimeCompare(review.ExpectedReportSHA256[:], in.Report.digest[:]) != 1 ||
		review.ArtifactID <= 0 || review.ArtifactID != b.ArtifactID || j.JobID <= 0 ||
		a.Source != cohort.Source || a.RepositoryID != cohort.RepositoryID ||
		a.WorkflowID != cohort.WorkflowID || a.RunID <= 0 || a.Attempt <= 0 ||
		b.Attempt != a || b.Job == nil || *b.Job != j ||
		b.Shard != cohort.ShardKey || b.Retry == nil || *b.Retry != review.Retry {
		return fail()
	}
	c := in.Report.cases[n-1]
	if c.Occurrence != n || c.Duration < 0 || c.Duration > MaxDuration ||
		!c.Timed && c.Duration != 0 || c.Outcome == Skipped && c.Timed {
		return fail()
	}
	switch c.Outcome {
	case Pass, Failure, Error, Skipped:
	default:
		return fail()
	}
	return c, executionSlot{a.RunID, a.Attempt, review.Retry}, review.ArtifactID, true
}

type comparisonBatch struct {
	totals Totals
	cases  []Case
	slow   int
	total  time.Duration
}

func (b *comparisonBatch) add(c Case, threshold time.Duration) {
	b.cases = append(b.cases, c)
	b.totals.addCase(c.Outcome)
	if c.Timed {
		b.total += c.Duration
		if threshold > 0 && c.Duration >= threshold {
			b.slow++
		}
	}
}

func (b comparisonBatch) stats(threshold time.Duration) CaseComparisonStats {
	s := summarize(b.cases, b.totals)
	decisive := s.Passed + s.Failed
	out := CaseComparisonStats{
		Summary:                 s,
		TotalTimedDuration:      b.total,
		FailureAmongPassFail:    Frequency{s.Failed, decisive},
		ErrorAmongPassFailError: Frequency{s.Errors, decisive + s.Errors},
	}
	if threshold > 0 {
		out.SlowAmongTimed = Frequency{b.slow, s.Timed}
	}
	return out
}
