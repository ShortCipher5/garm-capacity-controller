package cihealth

import (
	"math"
	"reflect"
	"slices"
	"testing"
	"time"
)

func trendScopeFixture() TrendScope {
	return TrendScope{Source: "fixture-source", RepositoryID: 9007199254740993, WorkflowID: 7}
}

func trendAttemptFixture(runID, attempt int64, duration time.Duration) TrendAttempt {
	scope := trendScopeFixture()
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	end := start.Add(duration)
	return TrendAttempt{
		Key:     AttemptKey{Source: scope.Source, RepositoryID: scope.RepositoryID, WorkflowID: scope.WorkflowID, RunID: runID, Attempt: attempt},
		AsOf:    end.Add(time.Second),
		Clock:   Clock{Phase: Completed, StartedAt: start, CompletedAt: end},
		Outcome: TrendOutcomeSuccess,
	}
}

func TestTrendSuccessfulWallAndResultCountsSeparateRetries(t *testing.T) {
	in := TrendCohort{Scope: trendScopeFixture(), Coverage: TrendCoveragePartial}
	for i := 20; i >= 1; i-- {
		in.Attempts = append(in.Attempts, trendAttemptFixture(int64(i), 1, time.Duration(i)))
	}
	// The first and third attempts of one run are distinct. Lack of attempt 2
	// is not extrapolated. Exact zero is an observed sample, not missing time.
	in.Attempts = append(in.Attempts, trendAttemptFixture(1, 3, 0))
	for i, outcome := range []TrendOutcome{TrendOutcomeFailure, TrendOutcomeCanceled, TrendOutcomeTimedOut, TrendOutcomeOtherTerminal, TrendOutcomeUnknown} {
		a := trendAttemptFixture(int64(100+i), 1, time.Hour)
		a.Outcome = outcome
		in.Attempts = append(in.Attempts, a)
	}
	missing := trendAttemptFixture(200, 1, time.Hour)
	missing.Clock.StartedAt = time.Time{}
	in.Attempts = append(in.Attempts, missing)
	for i, phase := range []Phase{Unknown, Queued, Running, Skipped} {
		a := trendAttemptFixture(int64(300+i), 1, time.Hour)
		a.Outcome = TrendOutcomeUnknown
		switch phase {
		case Running:
			a.Clock = Clock{Phase: phase, StartedAt: a.Clock.StartedAt}
		case Skipped:
			a.Clock.Phase = phase // Even provided endpoints are not a timing sample.
		default:
			a.Clock = Clock{Phase: phase}
		}
		in.Attempts = append(in.Attempts, a)
	}
	original := slices.Clone(in.Attempts)
	got, err := SummarizeTrend(in)
	want := TrendSummary{
		Scope: in.Scope, Coverage: TrendCoveragePartial,
		Initial: TrendBucket{
			Observed: 30, UnknownPhase: 1, Queued: 1, Running: 1, Skipped: 1,
			Results:            TrendResultCounts{Success: 21, Failure: 1, Canceled: 1, TimedOut: 1, OtherTerminal: 1, Unknown: 1},
			SuccessWallMissing: 1, SuccessWall: &TrendDurations{Samples: 20, Mean: 10, P95: 19},
		},
		Retried: TrendBucket{Observed: 1, Results: TrendResultCounts{Success: 1}, SuccessWall: &TrendDurations{Samples: 1}},
	}
	if err != nil || !reflect.DeepEqual(got, want) || !slices.Equal(in.Attempts, original) {
		t.Fatalf("got %+v, %v; want %+v without input mutation", got, err, want)
	}
	slices.Reverse(in.Attempts)
	reordered, err := SummarizeTrend(in)
	if err != nil || !reflect.DeepEqual(got, reordered) {
		t.Fatalf("row order changed results: %+v, %v", reordered, err)
	}
}

func TestTrendCoverageAndMissingAreNotMeasuredZero(t *testing.T) {
	for _, coverage := range []TrendCoverage{TrendCoverageUnknown, TrendCoveragePartial, TrendCoverageComplete} {
		got, err := SummarizeTrend(TrendCohort{Scope: trendScopeFixture(), Coverage: coverage})
		if err != nil || got.Coverage != coverage || got.Initial != (TrendBucket{}) || got.Retried != (TrendBucket{}) {
			t.Fatalf("coverage=%v: %+v, %v", coverage, got, err)
		}
	}
	// A fully observed failed run does not manufacture a successful baseline.
	failed := trendAttemptFixture(41, 1, 0)
	failed.Outcome = TrendOutcomeFailure
	got, err := SummarizeTrend(TrendCohort{Scope: trendScopeFixture(), Attempts: []TrendAttempt{failed}})
	if err != nil || got.Initial.Results.Failure != 1 || got.Initial.SuccessWall != nil || got.Coverage != TrendCoverageUnknown {
		t.Fatalf("failed run produced baseline or completeness: %+v, %v", got, err)
	}
}

func TestTrendMeanNeverOverflowsAndAttemptLimitIsExact(t *testing.T) {
	in := TrendCohort{Scope: trendScopeFixture(), Coverage: TrendCoverageComplete}
	for i := 1; i <= MaxTrendAttempts; i++ {
		d := time.Duration(math.MaxInt64)
		if i == 1 {
			d--
		}
		in.Attempts = append(in.Attempts, trendAttemptFixture(int64(i), 1, d))
	}
	got, err := SummarizeTrend(in)
	if err != nil || got.Initial.SuccessWall == nil || *got.Initial.SuccessWall != (TrendDurations{Samples: MaxTrendAttempts, Mean: math.MaxInt64 - 1, P95: math.MaxInt64}) {
		t.Fatalf("large exact mean or p95 incorrect: %+v, %v", got, err)
	}
	in.Attempts = append(in.Attempts, trendAttemptFixture(MaxTrendAttempts+1, 1, 1))
	if got, err := SummarizeTrend(in); err != ErrTrendFacts || got != (TrendSummary{}) {
		t.Fatalf("oversize returned partial result: %+v, %v", got, err)
	}
}

func TestTrendRefusesForeignDuplicateOrInvalidEvidenceAtomically(t *testing.T) {
	for name, change := range map[string]func(*TrendCohort){
		"unconfigured source": func(c *TrendCohort) { c.Scope.Source = "https://fixture-invalid" },
		"foreign source":      func(c *TrendCohort) { c.Attempts[1].Key.Source = "other" },
		"foreign repo":        func(c *TrendCohort) { c.Attempts[1].Key.RepositoryID++ },
		"foreign workflow":    func(c *TrendCohort) { c.Attempts[1].Key.WorkflowID++ },
		"duplicate attempt":   func(c *TrendCohort) { c.Attempts[1].Key = c.Attempts[0].Key },
		"invalid retry":       func(c *TrendCohort) { c.Attempts[1].Key.Attempt = 0 },
		"invalid coverage":    func(c *TrendCohort) { c.Coverage = 255 },
		"invalid outcome":     func(c *TrendCohort) { c.Attempts[1].Outcome = 255 },
		"skipped success":     func(c *TrendCohort) { c.Attempts[1].Clock.Phase = Skipped },
		"unknown success":     func(c *TrendCohort) { c.Attempts[1].Clock.Phase = Unknown },
		"reverse timestamps":  func(c *TrendCohort) { r := &c.Attempts[1]; r.Clock.CompletedAt = r.Clock.StartedAt.Add(-time.Second) },
		"future timestamps":   func(c *TrendCohort) { r := &c.Attempts[1]; r.Clock.CompletedAt = r.AsOf.Add(time.Second) },
		"unknown as-of":       func(c *TrendCohort) { c.Attempts[1].AsOf = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			in := TrendCohort{Scope: trendScopeFixture(), Attempts: []TrendAttempt{trendAttemptFixture(1, 1, 1), trendAttemptFixture(1, 2, 1)}}
			change(&in)
			if got, err := SummarizeTrend(in); err != ErrTrendFacts || got != (TrendSummary{}) {
				t.Fatalf("invalid cohort returned partial result: %+v, %v", got, err)
			}
		})
	}
}

func TestTrendUsesExplicitClockConclusionAndRejectsConflicts(t *testing.T) {
	in := TrendCohort{Scope: trendScopeFixture()}
	for i, c := range []Conclusion{ConclusionSuccess, ConclusionFailure, ConclusionCancelled, ConclusionTimedOut, ConclusionOtherTerminal, ConclusionUnknown} {
		a := trendAttemptFixture(int64(i+1), 1, 0)
		a.Outcome = TrendOutcomeUnknown
		a.Clock.Conclusion = c
		in.Attempts = append(in.Attempts, a)
	}
	got, err := SummarizeTrend(in)
	if err != nil || got.Initial.Results != (TrendResultCounts{1, 1, 1, 1, 1, 1}) ||
		got.Initial.SuccessWall == nil || *got.Initial.SuccessWall != (TrendDurations{Samples: 1}) {
		t.Fatalf("explicit results did not reach trend: %+v / %v", got, err)
	}
	in.Attempts[0].Outcome = TrendOutcomeFailure
	if got, err := SummarizeTrend(in); err != ErrTrendFacts || got != (TrendSummary{}) {
		t.Fatalf("contradictory result reached trend: %+v / %v", got, err)
	}
}
