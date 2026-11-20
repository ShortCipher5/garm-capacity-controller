package cihealth

import (
	"errors"
	"math/bits"
	"slices"
	"time"
)

const MaxTrendAttempts = 1000

var ErrTrendFacts = errors.New("invalid or oversized CI trend facts")

// TrendScope accepts one source, repository and stable workflow ID. The
// caller selects a comparable cohort within that scope; this reducer does not
// assign groups, combine sources, or infer equivalent workflow definitions.
type TrendScope struct {
	Source                   string
	RepositoryID, WorkflowID int64
}

type TrendCoverage uint8

const (
	TrendCoverageUnknown TrendCoverage = iota
	TrendCoveragePartial
	TrendCoverageComplete
)

type TrendOutcome uint8

const (
	TrendOutcomeUnknown TrendOutcome = iota
	TrendOutcomeSuccess
	TrendOutcomeFailure
	TrendOutcomeCanceled
	TrendOutcomeTimedOut
	TrendOutcomeOtherTerminal
)

// TrendAttempt carries just the attempt-level fields of the timing evidence.
// Outcome is an independently supplied classification of a terminal result;
// timestamps and Phase never imply success or failure.
type TrendAttempt struct {
	Key     AttemptKey
	AsOf    time.Time
	Clock   Clock
	Outcome TrendOutcome
}

type TrendCohort struct {
	Scope TrendScope
	// Coverage describes the caller-selected cohort, not global workflow
	// history. Missing rows are never filled or used to infer completeness.
	Coverage TrendCoverage
	Attempts []TrendAttempt
}

type TrendResultCounts struct {
	Success, Failure, Canceled, TimedOut, OtherTerminal, Unknown int
}

// TrendDurations summarizes successful attempts with exact wall endpoints.
// Mean is truncated to a nanosecond; P95 is the nearest-rank percentile.
type TrendDurations struct {
	Samples   int
	Mean, P95 time.Duration
}

type TrendBucket struct {
	Observed, Queued, Running, Skipped, UnknownPhase int
	Results                                          TrendResultCounts
	SuccessWallMissing                               int
	// Nil means no comparable samples; an observed zero is a non-nil result.
	SuccessWall *TrendDurations
}

type TrendSummary struct {
	Scope            TrendScope
	Coverage         TrendCoverage
	Initial, Retried TrendBucket // attempt 1 vs every separately observed attempt >1
}

// SummarizeTrend reduces one bounded cohort without extrapolating from an
// incomplete or unknown observation. Failed and canceled runs can end early:
// report their results but do not mix them into successful wall-time stats.
// The entire cohort is refused on invalid, duplicate or foreign evidence.
func SummarizeTrend(in TrendCohort) (TrendSummary, error) {
	if !validSource(in.Scope.Source) || in.Scope.RepositoryID <= 0 || in.Scope.WorkflowID <= 0 ||
		in.Coverage > TrendCoverageComplete || len(in.Attempts) > MaxTrendAttempts {
		return TrendSummary{}, ErrTrendFacts
	}
	out := TrendSummary{Scope: in.Scope, Coverage: in.Coverage}
	seen := make(map[AttemptKey]bool, len(in.Attempts))
	var initial, retried []time.Duration
	for _, row := range in.Attempts {
		key := row.Key
		if !validKey(key) || key.Source != in.Scope.Source || key.RepositoryID != in.Scope.RepositoryID || key.WorkflowID != in.Scope.WorkflowID ||
			seen[key] || !validTime(row.AsOf) || row.Outcome > TrendOutcomeOtherTerminal ||
			(row.Clock.Phase != Completed && row.Outcome != TrendOutcomeUnknown) {
			return TrendSummary{}, ErrTrendFacts
		}
		wall, _, ok := measureClock(row.Clock, row.AsOf)
		if !ok {
			return TrendSummary{}, ErrTrendFacts
		}
		seen[key] = true
		bucket, durations := &out.Initial, &initial
		if key.Attempt > 1 {
			bucket, durations = &out.Retried, &retried
		}
		bucket.Observed++
		switch row.Clock.Phase {
		case Unknown:
			bucket.UnknownPhase++
		case Queued:
			bucket.Queued++
		case Running:
			bucket.Running++
		case Skipped:
			bucket.Skipped++
		case Completed:
			switch row.Outcome {
			case TrendOutcomeUnknown:
				bucket.Results.Unknown++
			case TrendOutcomeSuccess:
				bucket.Results.Success++
				if wall.Basis == ObservedEndpoints {
					*durations = append(*durations, wall.Duration)
				} else {
					bucket.SuccessWallMissing++
				}
			case TrendOutcomeFailure:
				bucket.Results.Failure++
			case TrendOutcomeCanceled:
				bucket.Results.Canceled++
			case TrendOutcomeTimedOut:
				bucket.Results.TimedOut++
			case TrendOutcomeOtherTerminal:
				bucket.Results.OtherTerminal++
			}
		}
	}
	out.Initial.SuccessWall = summarizeTrendDurations(initial)
	out.Retried.SuccessWall = summarizeTrendDurations(retried)
	return out, nil
}

func summarizeTrendDurations(owned []time.Duration) *TrendDurations {
	if len(owned) == 0 {
		return nil
	}
	// Every duration is nonnegative and <= MaxInt64. The sum may exceed 64
	// bits, but the mean fits int64, so hi < divisor as bits.Div64 requires.
	var hi, lo, carry uint64
	for _, d := range owned {
		lo, carry = bits.Add64(lo, uint64(d), 0)
		hi += carry
	}
	n := len(owned)
	mean, _ := bits.Div64(hi, lo, uint64(n))
	slices.Sort(owned)
	rank := (95*n + 99) / 100
	return &TrendDurations{Samples: n, Mean: time.Duration(mean), P95: owned[rank-1]}
}
