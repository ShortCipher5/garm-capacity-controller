package cihealth

import (
	"testing"
	"time"
)

var retainedTestScope = TrendScope{Source: "fixture", RepositoryID: 1, WorkflowID: 2}

func retainedTestFact(run, attempt int64, observed time.Time) AttemptFacts {
	return AttemptFacts{Key: AttemptKey{
		Source: retainedTestScope.Source, RepositoryID: retainedTestScope.RepositoryID, WorkflowID: retainedTestScope.WorkflowID,
		RunID: run, Attempt: attempt,
	}, AsOf: observed}
}

func TestRetainedTrendsWindowAndAttemptBoundaries(t *testing.T) {
	end := time.Date(2026, 5, 21, 0, 0, 0, 0, time.FixedZone("fixture", 5*3600))
	boundary := end.Add(-retainedTrendWindow)
	start := boundary.Add(-retainedTrendWindow)
	rows := []AttemptFacts{
		retainedTestFact(4, 1, start.Add(-1)),
		retainedTestFact(3, 1, start),
		retainedTestFact(5, 1, end),
		retainedTestFact(3, 2, boundary.Add(-1)),
		retainedTestFact(3, 3, boundary),
	}
	got, err := SummarizeRetainedTrends(retainedTestScope, end, rows)
	if err != nil {
		t.Fatal(err)
	}
	if got.Previous.From != start.UTC() || got.Previous.To != boundary.UTC() ||
		got.Current.From != boundary.UTC() || got.Current.To != end.UTC() {
		t.Fatalf("wrong windows: %+v", got)
	}
	for _, b := range []RetainedTrendBucket{got.Previous.Initial, got.Previous.Retries, got.Current.Initial, got.Current.Retries} {
		if b.Attempts != (RetainedTrendMetric{Observed: 1, Unknown: 1}) ||
			b.Jobs != (RetainedTrendMetric{}) || b.Steps != (RetainedTrendMetric{}) || b.Runs != 1 {
			t.Fatalf("unexpected bucket: %+v", b)
		}
	}
}

func TestRetainedTrendsCountsDistinctRunsWithinEachPartition(t *testing.T) {
	end := time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC)
	got, err := SummarizeRetainedTrends(retainedTestScope, end, []AttemptFacts{
		retainedTestFact(5, 1, end), retainedTestFact(5, 2, end), retainedTestFact(5, 3, end),
	})
	if err != nil || got.Current.Initial.Runs != 1 || got.Current.Retries.Runs != 1 || got.Current.Retries.Attempts.Observed != 2 {
		t.Fatalf("reruns blurred exact run or attempt identity: %+v, %v", got, err)
	}
}

func TestRetainedTrendsRecoveryRequiresSameRunAndExplicitAttemptResults(t *testing.T) {
	end := time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC)
	boundary := end.Add(-retainedTrendWindow)
	outside := boundary.Add(-retainedTrendWindow - 1)
	fact := func(run, attempt int64, at time.Time, conclusion Conclusion) AttemptFacts {
		r := retainedTestFact(run, attempt, at)
		r.Clock = Clock{Phase: Completed, Conclusion: conclusion}
		return r
	}
	const previous, current = 9007199254740992, 9007199254740993
	noRunSuccess := fact(14, 2, end, ConclusionUnknown)
	noRunSuccess.Jobs = []JobFacts{{Key: JobKey{noRunSuccess.Key, 7}, Clock: Clock{Phase: Completed, Conclusion: ConclusionSuccess}}}
	rows := []AttemptFacts{
		fact(previous, 3, end, ConclusionSuccess), // Source observations need not be in attempt order.
		fact(previous, 1, boundary.Add(-1), ConclusionFailure),
		fact(previous, 2, boundary, ConclusionSuccess), // Do not count the same run twice.
		fact(previous, 4, end, ConclusionFailure),      // Observed successful retry does not claim latest outcome.
		fact(current, 1, boundary, ConclusionFailure),
		fact(current, 2, boundary.Add(-1), ConclusionSuccess), // Retry seen before a later reread of attempt 1.
		fact(10, 1, end, ConclusionFailure),
		fact(11, 2, end, ConclusionSuccess), // A different run cannot recover run 10.
		fact(12, 1, outside, ConclusionFailure),
		fact(12, 2, end, ConclusionSuccess), // Out-of-window first attempt creates no cohort member.
		fact(13, 1, end, ConclusionFailure),
		fact(13, 2, outside, ConclusionSuccess), // Out-of-window retry is not selected evidence.
		fact(14, 1, end, ConclusionFailure),
		noRunSuccess, // A successful child does not prove the attempt result.
		fact(15, 1, end, ConclusionTimedOut),
		fact(15, 2, end, ConclusionSuccess),
		fact(16, 1, end, ConclusionSuccess),
		fact(16, 2, end, ConclusionSuccess),
	}
	got, err := SummarizeRetainedTrends(retainedTestScope, end, rows)
	if err != nil || got.Previous.RecoveredInitialFailures != 1 || got.Current.RecoveredInitialFailures != 1 ||
		got.Previous.Initial.Attempts.Failure != 1 || got.Current.Initial.Attempts.Failure != 4 {
		t.Fatalf("wrong result/resolution cohorts: %+v, %v", got, err)
	}
	alien := fact(previous, 5, end, ConclusionSuccess)
	alien.Key.WorkflowID++
	got, err = SummarizeRetainedTrends(retainedTestScope, end, []AttemptFacts{rows[1], alien})
	if err != ErrTrendFacts || got != (RetainedTrends{}) {
		t.Fatalf("foreign workflow evidence leaked into recovery result: %+v, %v", got, err)
	}
}

func TestRetainedTrendsExactTimingsIndependentOfResult(t *testing.T) {
	end := time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC)
	r := retainedTestFact(3, 1, end)
	key1, key2, key3 := JobKey{r.Key, 1}, JobKey{r.Key, 2}, JobKey{r.Key, 3}
	r.Clock = Clock{Phase: Completed, Conclusion: ConclusionSuccess}
	r.Jobs = []JobFacts{
		{Key: key1, Clock: Clock{Phase: Completed, Conclusion: ConclusionFailure, StartedAt: end.Add(-4), CompletedAt: end},
			Steps: []StepFacts{
				{Key: StepKey{key1, 1}, Clock: Clock{Phase: Completed, Conclusion: ConclusionFailure, StartedAt: end.Add(-4), CompletedAt: end}},
				{Key: StepKey{key1, 2}, Clock: Clock{Phase: Completed, Conclusion: ConclusionSuccess, StartedAt: end, CompletedAt: end}},
				{Key: StepKey{key1, 3}, Clock: Clock{Phase: Skipped}},
				{Key: StepKey{key1, 4}, Clock: Clock{Phase: Completed}},
			}},
		{Key: key2, Clock: Clock{Phase: Completed, Conclusion: ConclusionSuccess, StartedAt: end, CompletedAt: end}},
		{Key: key3, Clock: Clock{Phase: Completed, Conclusion: ConclusionCancelled}},
	}
	active := retainedTestFact(4, 1, end)
	active.Clock = Clock{Phase: Running, StartedAt: end.Add(-10), ObservedAt: end}
	got, err := SummarizeRetainedTrends(retainedTestScope, end, []AttemptFacts{active, r})
	if err != nil {
		t.Fatal(err)
	}
	gotBucket := got.Current.Initial
	if gotBucket.Attempts != (RetainedTrendMetric{Observed: 2, Success: 1, Active: 1, Completed: 1}) {
		t.Fatalf("attempts: %+v", gotBucket.Attempts)
	}
	want := []RetainedTrendMetric{
		{Observed: 3, Success: 1, Failure: 1, Other: 1, Completed: 3},
		{Observed: 4, Success: 1, Failure: 1, Other: 1, Unknown: 1, Completed: 3},
	}
	for i, m := range []RetainedTrendMetric{gotBucket.Jobs, gotBucket.Steps} {
		if m.Duration == nil || *m.Duration != (TrendDurations{Samples: 2, Mean: 2, P95: 4}) {
			t.Fatalf("metric %d duration: %+v", i, m.Duration)
		}
		m.Duration = nil
		if m != want[i] {
			t.Fatalf("metric %d counters: %+v", i, m)
		}
	}
}

func TestRetainedTrendsRefusesEntireInvalidScope(t *testing.T) {
	end := time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC)
	row := retainedTestFact(3, 1, end)
	foreign := retainedTestFact(4, 1, end)
	foreign.Key.RepositoryID++
	future := retainedTestFact(4, 1, end.Add(1))
	badAncestry := retainedTestFact(4, 1, end.Add(-3*retainedTrendWindow))
	badAncestry.Jobs = []JobFacts{{Key: JobKey{row.Key, 1}}}
	for name, rows := range map[string][]AttemptFacts{
		"duplicate": {row, row}, "foreign": {row, foreign}, "future": {row, future},
		"malformed even outside window": {row, badAncestry}, "over cap": make([]AttemptFacts, 65),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SummarizeRetainedTrends(retainedTestScope, end, rows)
			if err != ErrTrendFacts || got != (RetainedTrends{}) {
				t.Fatalf("expected wholesale refusal, got %+v, %v", got, err)
			}
		})
	}
}

// A sparse later observation of an exact attempt (terminal result, omitted
// endpoints) refreshes the attempt's window and completed counts, keeps the
// previously measured duration visible through the recovery projection, and
// never blocks or double-counts unrelated attempts.
func TestRetainedTrendsSparseLaterObservationRecoversPriorMeasurement(t *testing.T) {
	end := time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC)
	measuredAt := end.Add(-time.Hour)
	key := AttemptKey{retainedTestScope.Source, retainedTestScope.RepositoryID, retainedTestScope.WorkflowID, 20, 1}
	measuredClock := func(started, completed time.Time) Clock {
		return Clock{Phase: Completed, StartedAt: started, CompletedAt: completed, ObservedAt: measuredAt, Conclusion: ConclusionSuccess}
	}
	measured := AttemptFacts{Key: key, AsOf: measuredAt, Clock: measuredClock(measuredAt.Add(-4*time.Minute), measuredAt),
		Jobs: []JobFacts{{
			Key:   JobKey{Attempt: key, JobID: 7},
			Clock: measuredClock(measuredAt.Add(-3*time.Minute), measuredAt.Add(-time.Minute)),
			Steps: []StepFacts{{
				Key:   StepKey{Job: JobKey{Attempt: key, JobID: 7}, Number: 1},
				Clock: measuredClock(measuredAt.Add(-2*time.Minute), measuredAt.Add(-time.Minute)),
			}},
		}}}
	sparse := measured
	sparse.AsOf = end
	sparse.Jobs = append([]JobFacts(nil), measured.Jobs...)
	sparse.Jobs[0].Steps = append([]StepFacts(nil), measured.Jobs[0].Steps...)
	sparse.Clock.StartedAt, sparse.Clock.CompletedAt = time.Time{}, time.Time{}
	sparse.Jobs[0].Clock.StartedAt, sparse.Jobs[0].Clock.CompletedAt = time.Time{}, time.Time{}
	sparse.Jobs[0].Steps[0].Clock.StartedAt, sparse.Jobs[0].Steps[0].Clock.CompletedAt = time.Time{}, time.Time{}
	normal := retainedTestFact(21, 1, end)
	normal.Clock = Clock{Phase: Completed, StartedAt: end.Add(-2 * time.Minute), CompletedAt: end, ObservedAt: end, Conclusion: ConclusionSuccess}
	normal.Jobs = []JobFacts{{Key: JobKey{Attempt: normal.Key, JobID: 8},
		Clock: Clock{Phase: Completed, StartedAt: end.Add(-90 * time.Second), CompletedAt: end, ObservedAt: end, Conclusion: ConclusionSuccess},
		Steps: []StepFacts{{Key: StepKey{Job: JobKey{Attempt: normal.Key, JobID: 8}, Number: 1},
			Clock: Clock{Phase: Completed, StartedAt: end.Add(-time.Minute), CompletedAt: end, ObservedAt: end, Conclusion: ConclusionSuccess}}}}}

	got, err := SummarizeRetainedTrends(retainedTestScope, end, []AttemptFacts{sparse, measured, normal})
	if err != nil {
		t.Fatal(err)
	}
	bucket := got.Current.Initial
	durations := []TrendDurations{
		{Samples: 2, Mean: 3 * time.Minute, P95: 4 * time.Minute},   // Walls: 4m recovered, 2m current.
		{Samples: 2, Mean: 105 * time.Second, P95: 2 * time.Minute}, // Executions: 2m recovered, 90s current.
		{Samples: 2, Mean: time.Minute, P95: time.Minute},           // Steps: 1m recovered, 1m current.
	}
	if bucket.Attempts.Duration == nil || *bucket.Attempts.Duration != durations[0] {
		t.Fatalf("attempt durations lost the recovered measurement: %+v", bucket.Attempts.Duration)
	}
	attempts := bucket.Attempts
	attempts.Duration = nil
	if bucket.Runs != 2 || attempts != (RetainedTrendMetric{Observed: 2, Success: 2, Completed: 2}) {
		t.Fatalf("versions double-counted or window not refreshed: %+v", bucket.Attempts)
	}
	want := []RetainedTrendMetric{
		{Observed: 2, Success: 2, Completed: 2},
		{Observed: 2, Success: 2, Completed: 2},
	}
	for i, m := range []RetainedTrendMetric{bucket.Jobs, bucket.Steps} {
		if m.Duration == nil || *m.Duration != durations[i+1] {
			t.Fatalf("metric %d lost the recovered measurement: %+v", i, m.Duration)
		}
		m.Duration = nil
		if m != want[i] {
			t.Fatalf("metric %d counters: %+v", i, m)
		}
	}
	// The same observation version twice is still wholesale refusal.
	if got, err := SummarizeRetainedTrends(retainedTestScope, end, []AttemptFacts{sparse, sparse}); err != ErrTrendFacts || got != (RetainedTrends{}) {
		t.Fatalf("duplicate version accepted: %+v, %v", got, err)
	}
}
