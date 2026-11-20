package cihealth

import "time"

const (
	MaxWorkflowTrendWindow       = 14 * 24 * time.Hour
	MaxWorkflowTrendCurrentLag   = 15 * time.Minute
	MinWorkflowTrendKnownResults = 5
	MinWorkflowTrendWallSamples  = 20
)

type WorkflowTrendComparability uint8

const (
	WorkflowTrendComparabilityUnknown WorkflowTrendComparability = iota
	// The owner independently established comparable definitions, inputs
	// and observation/selection rules for both windows. A stable workflow
	// ID or an unchanged name alone does not establish comparability.
	WorkflowTrendReviewedComparable
)

type WorkflowTrendWindow struct {
	// Half-open observation window [From, To) for each attempt's AsOf.
	// These are not terminal event dates or whole GitHub history.
	From, To time.Time
	Coverage TrendCoverage
	Attempts []TrendAttempt
}

type WorkflowTrendInput struct {
	Scope         TrendScope
	Now           time.Time
	Current       WorkflowTrendWindow
	Previous      WorkflowTrendWindow
	Comparability WorkflowTrendComparability
}

type WorkflowTrendDirection uint8

const (
	WorkflowTrendUnreported WorkflowTrendDirection = iota
	WorkflowTrendLower
	WorkflowTrendSame
	WorkflowTrendHigher
)

type WorkflowTrendChange struct {
	// Higher/lower success share among explicit success and failure only.
	// Cancellation, timeout, skipped and other results remain in the
	// underlying summaries; they do not count as success or failure here.
	SuccessShare WorkflowTrendDirection
	// Higher/lower successful attempt wall p95. Never an autoscale signal
	// or proof of slowdown in any individual job.
	SuccessWallP95 WorkflowTrendDirection
}

type WorkflowTrendComparison struct {
	Current, Previous TrendSummary
	Initial, Retried  WorkflowTrendChange
}

// CompareWorkflowTrend compares two bounded, directly adjacent windows of
// observed terminal attempt facts for one exact workflow. It uses the
// existing cohort reducer and never guesses cross-run job identity, terminal
// dates, workflow equivalence, or completeness. The counts are descriptive;
// direction is withheld if provenance, coverage, timing or samples are weak.
func CompareWorkflowTrend(in WorkflowTrendInput) (WorkflowTrendComparison, error) {
	normalize := func(w WorkflowTrendWindow) WorkflowTrendWindow {
		w.From, w.To = w.From.UTC(), w.To.UTC()
		return w
	}
	cur, prev, now := normalize(in.Current), normalize(in.Previous), in.Now.UTC()
	if in.Comparability > WorkflowTrendReviewedComparable || !validTime(now) ||
		!validWorkflowTrendWindow(cur) || !validWorkflowTrendWindow(prev) ||
		!prev.To.Equal(cur.From) || cur.To.Sub(cur.From) != prev.To.Sub(prev.From) ||
		cur.To.After(now) || now.Sub(cur.To) > MaxWorkflowTrendCurrentLag {
		return WorkflowTrendComparison{}, ErrTrendFacts
	}
	// Multiple attempts of one run may appear in one window (and stay in
	// separate initial/retry buckets), but the same run cannot contaminate
	// both samples even when the two attempt numbers differ.
	previousRuns := make(map[int64]bool, len(prev.Attempts))
	for _, a := range prev.Attempts {
		previousRuns[a.Key.RunID] = true
	}
	for _, a := range cur.Attempts {
		if previousRuns[a.Key.RunID] {
			return WorkflowTrendComparison{}, ErrTrendFacts
		}
	}
	previous, err := SummarizeTrend(TrendCohort{Scope: in.Scope, Coverage: prev.Coverage, Attempts: prev.Attempts})
	if err != nil {
		return WorkflowTrendComparison{}, ErrTrendFacts
	}
	current, err := SummarizeTrend(TrendCohort{Scope: in.Scope, Coverage: cur.Coverage, Attempts: cur.Attempts})
	if err != nil {
		return WorkflowTrendComparison{}, ErrTrendFacts
	}
	out := WorkflowTrendComparison{Current: current, Previous: previous}
	if in.Comparability == WorkflowTrendReviewedComparable &&
		previous.Coverage == TrendCoverageComplete && current.Coverage == TrendCoverageComplete {
		c := workflowTrendEvidenceFor(cur.Attempts)
		p := workflowTrendEvidenceFor(prev.Attempts)
		out.Initial = workflowTrendChange(current.Initial, previous.Initial, c[0], p[0])
		out.Retried = workflowTrendChange(current.Retried, previous.Retried, c[1], p[1])
	}
	return out, nil
}

func validWorkflowTrendWindow(w WorkflowTrendWindow) bool {
	if !validTime(w.From) || !validTime(w.To) || !w.From.Before(w.To) || w.To.Sub(w.From) > MaxWorkflowTrendWindow ||
		len(w.Attempts) > MaxTrendAttempts {
		return false
	}
	for _, a := range w.Attempts {
		at := a.AsOf.UTC()
		if (a.Clock.Phase != Completed && a.Clock.Phase != Skipped) || at.Before(w.From) || !at.Before(w.To) {
			return false
		}
	}
	return true
}

type workflowTrendEvidence struct {
	resultRuns, exactSuccessWallRuns int
	gap, repeatedRetries             bool
}

// Build independent headline sample counts only from validated attempt-clock
// conclusions. The legacy standalone outcome is still included in descriptive
// SummarizeTrend counts, but cannot establish authenticated headline evidence.
func workflowTrendEvidenceFor(rows []TrendAttempt) [2]workflowTrendEvidence {
	var out [2]workflowTrendEvidence
	seenRetries := map[int64]bool{}
	resultIDs := [2]map[int64]bool{{}, {}}
	wallIDs := [2]map[int64]bool{{}, {}}
	for _, row := range rows {
		run := row.Key.RunID
		bucket := 0
		if row.Key.Attempt > 1 {
			bucket = 1
			if seenRetries[run] {
				out[bucket].repeatedRetries = true
			}
			seenRetries[run] = true
		}
		if row.Clock.Phase != Completed {
			continue
		}
		switch row.Clock.Conclusion {
		case ConclusionUnknown:
			out[bucket].gap = true
		case ConclusionSuccess, ConclusionFailure:
			resultIDs[bucket][run] = true
			wall, _, valid := measureClock(row.Clock, row.AsOf)
			if valid && row.Clock.Conclusion == ConclusionSuccess && wall.Basis == ObservedEndpoints {
				wallIDs[bucket][run] = true
			}
		}
	}
	for i := range out {
		out[i].resultRuns = len(resultIDs[i])
		out[i].exactSuccessWallRuns = len(wallIDs[i])
	}
	return out
}

func workflowTrendChange(current, previous TrendBucket, ce, pe workflowTrendEvidence) (out WorkflowTrendChange) {
	if ce.gap || pe.gap || ce.repeatedRetries || pe.repeatedRetries {
		return out
	}
	nowResults := ce.resultRuns
	prevResults := pe.resultRuns
	if nowResults >= MinWorkflowTrendKnownResults && prevResults >= MinWorkflowTrendKnownResults {
		// Both factors are bounded by MaxTrendAttempts, avoiding rounding
		// or float conversion when comparing rates with unlike denominators.
		out.SuccessShare = workflowTrendDirection(current.Results.Success*prevResults, previous.Results.Success*nowResults)
	}
	if current.SuccessWallMissing == 0 && previous.SuccessWallMissing == 0 &&
		current.SuccessWall != nil && previous.SuccessWall != nil &&
		ce.exactSuccessWallRuns >= MinWorkflowTrendWallSamples && pe.exactSuccessWallRuns >= MinWorkflowTrendWallSamples {
		out.SuccessWallP95 = workflowTrendDirection(current.SuccessWall.P95, previous.SuccessWall.P95)
	}
	return
}

func workflowTrendDirection[T ~int | ~int64](current, previous T) WorkflowTrendDirection {
	switch {
	case current < previous:
		return WorkflowTrendLower
	case current > previous:
		return WorkflowTrendHigher
	default:
		return WorkflowTrendSame
	}
}
