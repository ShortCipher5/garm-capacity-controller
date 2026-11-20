package cihealth

import (
	"errors"
	"testing"
	"time"
)

func workflowTrendInputFixture() WorkflowTrendInput {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	scope := TrendScope{"fixture", 9007199254740993, 9007199254740997}
	end := now.Add(-5 * time.Minute)
	current := WorkflowTrendWindow{From: end.Add(-24 * time.Hour), To: end, Coverage: TrendCoverageComplete}
	previous := WorkflowTrendWindow{From: end.Add(-48 * time.Hour), To: current.From, Coverage: TrendCoverageComplete}
	return WorkflowTrendInput{Scope: scope, Now: now, Current: current, Previous: previous, Comparability: WorkflowTrendReviewedComparable}
}

func appendWorkflowTrendFixture(w *WorkflowTrendWindow, scope TrendScope, fromID, attempt int64, c Conclusion, count int, duration time.Duration) {
	for i := range count {
		at := w.From.Add(time.Hour + time.Duration(i)*time.Second)
		start := at.Add(-duration - time.Minute)
		w.Attempts = append(w.Attempts, TrendAttempt{
			Key:   AttemptKey{scope.Source, scope.RepositoryID, scope.WorkflowID, fromID + int64(i), attempt},
			AsOf:  at,
			Clock: Clock{Phase: Completed, Conclusion: c, StartedAt: start, CompletedAt: start.Add(duration), ObservedAt: at},
		})
	}
}

func TestWorkflowTrendComparesExplicitClosedResultsAndExactWall(t *testing.T) {
	in := workflowTrendInputFixture()
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 1, 1, ConclusionSuccess, 20, 2*time.Minute)
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 21, 1, ConclusionFailure, 5, time.Second)
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 1, 2, ConclusionSuccess, 4, time.Minute)
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 5, 2, ConclusionFailure, 1, time.Minute)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 101, 1, ConclusionSuccess, 21, 3*time.Minute)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 122, 1, ConclusionFailure, 2, time.Second)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 124, 1, ConclusionCancelled, 3, time.Second)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 101, 2, ConclusionSuccess, 1, time.Minute)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 102, 2, ConclusionFailure, 4, time.Minute)
	got, err := CompareWorkflowTrend(in)
	if err != nil || got.Current.Scope != in.Scope || got.Previous.Scope != in.Scope ||
		got.Initial != (WorkflowTrendChange{WorkflowTrendHigher, WorkflowTrendHigher}) ||
		got.Retried != (WorkflowTrendChange{WorkflowTrendLower, WorkflowTrendUnreported}) {
		t.Fatalf("expected independent success-share/p95 direction across first attempts and retries: %+v %v", got, err)
	}
	if got.Current.Initial.Results.Success != 21 || got.Current.Initial.Results.Failure != 2 || got.Current.Initial.Results.Canceled != 3 ||
		got.Previous.Initial.Results.Success != 20 || got.Previous.Initial.Results.Failure != 5 || got.Current.Initial.SuccessWall == nil ||
		got.Current.Initial.SuccessWall.Samples != 21 || got.Current.Initial.SuccessWall.P95 != 3*time.Minute {
		t.Fatalf("descriptive counts or timing evidence changed: %+v", got)
	}
}

func TestWorkflowTrendKeepsPartialMissingAndMinimumSeparate(t *testing.T) {
	in := workflowTrendInputFixture()
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 1, 1, ConclusionSuccess, 20, time.Minute)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 101, 1, ConclusionSuccess, 20, time.Minute)
	base, err := CompareWorkflowTrend(in)
	if err != nil || base.Initial != (WorkflowTrendChange{WorkflowTrendSame, WorkflowTrendSame}) {
		t.Fatalf("exact equal measured workflow samples became unknown: %+v %v", base, err)
	}
	for label, edit := range map[string]func(*WorkflowTrendInput){
		"partial":                      func(v *WorkflowTrendInput) { v.Current.Coverage = TrendCoveragePartial },
		"unknown workflow equivalence": func(v *WorkflowTrendInput) { v.Comparability = WorkflowTrendComparabilityUnknown },
		"small sample":                 func(v *WorkflowTrendInput) { v.Current.Attempts = v.Current.Attempts[:4] },
		"unknown result":               func(v *WorkflowTrendInput) { v.Current.Attempts[0].Clock.Conclusion = ConclusionUnknown },
	} {
		t.Run(label, func(t *testing.T) {
			v := in
			v.Current.Attempts = append([]TrendAttempt(nil), in.Current.Attempts...)
			edit(&v)
			got, err := CompareWorkflowTrend(v)
			if err != nil || got.Initial != (WorkflowTrendChange{}) || got.Current.Initial.Observed == 0 {
				t.Fatalf("weak comparison should retain counts but withhold direction: %+v %v", got, err)
			}
		})
	}
	in.Current.Attempts[0].Clock.CompletedAt = time.Time{}
	got, err := CompareWorkflowTrend(in)
	if err != nil || got.Initial != (WorkflowTrendChange{WorkflowTrendSame, WorkflowTrendUnreported}) || got.Current.Initial.SuccessWallMissing != 1 {
		t.Fatalf("missing run endpoint affected known result or invented duration: %+v %v", got, err)
	}
}

func TestWorkflowTrendRefusesUnrelatedOrAmbiguousCohortsAtomically(t *testing.T) {
	in := workflowTrendInputFixture()
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 1, 1, ConclusionSuccess, 1, time.Minute)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 101, 1, ConclusionSuccess, 1, time.Minute)
	for label, edit := range map[string]func(*WorkflowTrendInput){
		"stale ending window":         func(v *WorkflowTrendInput) { v.Now = v.Current.To.Add(MaxWorkflowTrendCurrentLag + time.Nanosecond) },
		"current window in future":    func(v *WorkflowTrendInput) { v.Now = v.Current.To.Add(-time.Nanosecond) },
		"gap":                         func(v *WorkflowTrendInput) { v.Previous.To = v.Previous.To.Add(-time.Second) },
		"unequal window":              func(v *WorkflowTrendInput) { v.Previous.From = v.Previous.From.Add(-time.Second) },
		"right endpoint not included": func(v *WorkflowTrendInput) { v.Current.Attempts[0].AsOf = v.Current.To },
		"open attempt":                func(v *WorkflowTrendInput) { v.Current.Attempts[0].Clock.Phase = Running },
		"foreign workflow":            func(v *WorkflowTrendInput) { v.Current.Attempts[0].Key.WorkflowID++ },
		"same run across windows different attempt": func(v *WorkflowTrendInput) {
			v.Current.Attempts[0].Key.RunID = 1
			v.Current.Attempts[0].Key.Attempt = 2
		},
		"duplicate attempt": func(v *WorkflowTrendInput) { v.Current.Attempts = append(v.Current.Attempts, v.Current.Attempts[0]) },
	} {
		t.Run(label, func(t *testing.T) {
			v := in
			v.Current.Attempts = append([]TrendAttempt(nil), in.Current.Attempts...)
			edit(&v)
			if got, err := CompareWorkflowTrend(v); got != (WorkflowTrendComparison{}) || !errors.Is(err, ErrTrendFacts) {
				t.Fatalf("invalid cohorts produced a partial result: %+v %v", got, err)
			}
		})
	}
}

func TestWorkflowTrendRepeatedRetryRunCannotSupplyEitherHeadlineMinimum(t *testing.T) {
	in := workflowTrendInputFixture()
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 1, 1, ConclusionSuccess, 5, time.Minute)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 101, 1, ConclusionSuccess, 5, time.Minute)
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 20, 2, ConclusionSuccess, 20, time.Minute)
	for i := range int64(20) {
		appendWorkflowTrendFixture(&in.Current, in.Scope, 120, 2+i, ConclusionSuccess, 1, 2*time.Minute)
	}
	got, err := CompareWorkflowTrend(in)
	if err != nil || got.Initial.SuccessShare != WorkflowTrendSame ||
		got.Retried != (WorkflowTrendChange{}) || got.Current.Retried.Results.Success != 20 ||
		got.Current.Retried.SuccessWall == nil || got.Current.Retried.SuccessWall.Samples != 20 {
		t.Fatalf("one repeatedly retried run met a headline denominator or lost descriptive facts: %+v %v", got, err)
	}
}

func TestWorkflowTrendLegacyOutcomeNotAcceptedForHeadlines(t *testing.T) {
	in := workflowTrendInputFixture()
	appendWorkflowTrendFixture(&in.Previous, in.Scope, 1, 1, ConclusionSuccess, 20, time.Minute)
	appendWorkflowTrendFixture(&in.Current, in.Scope, 101, 1, ConclusionSuccess, 20, 2*time.Minute)
	in.Current.Attempts[0].Clock.Conclusion = ConclusionUnknown
	in.Current.Attempts[0].Outcome = TrendOutcomeSuccess
	got, err := CompareWorkflowTrend(in)
	if err != nil || got.Initial != (WorkflowTrendChange{}) || got.Current.Initial.Results.Success != 20 ||
		got.Current.Initial.SuccessWall == nil || got.Current.Initial.SuccessWall.Samples != 20 {
		t.Fatalf("legacy outcome supplied or erased authenticated headline evidence: %+v %v", got, err)
	}
}
