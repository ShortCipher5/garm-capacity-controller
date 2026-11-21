package cihealth

import "time"

const MaxRetainedTrendAttempts = 64

const retainedTrendWindow = 7 * 24 * time.Hour

// RetainedTrends describes selected retained observations, never complete
// coverage or a comparison of equivalent jobs, steps or workflow versions.
type RetainedTrends struct {
	Previous, Current RetainedTrendWindow
}

// RetainedTrendWindow groups each attempt and its children by the attempt's
// last observed timestamp, not the time a job ended. Previous excludes its
// upper boundary; Current includes it.
type RetainedTrendWindow struct {
	From, To         time.Time
	Initial, Retries RetainedTrendBucket
}

type RetainedTrendBucket struct {
	Runs                  int // exact distinct run IDs within this window/attempt partition
	Attempts, Jobs, Steps RetainedTrendMetric
}

// RetainedTrendMetric counts individual observations. A reported failure
// fraction uses only Success + Failure as its denominator. Duration pools
// exact completed endpoint timings independently of result; nil denotes no
// sample and differs from a measured zero.
type RetainedTrendMetric struct {
	Observed, Success, Failure, Other, Active, Unknown int
	Duration                                           *TrendDurations
}

type retainedTrendSamples struct {
	attempts, jobs, steps []time.Duration
}

// SummarizeRetainedTrends reduces one exact workflow's bounded local history.
// A missing row never creates a count or establishes completeness. Invalid,
// duplicate, future, or foreign evidence invalidates the whole result,
// including rows older than the selected windows.
func SummarizeRetainedTrends(scope TrendScope, observedAt time.Time, rows []AttemptFacts) (RetainedTrends, error) {
	observedAt = observedAt.UTC()
	cutover := observedAt.Add(-retainedTrendWindow)
	begin := cutover.Add(-retainedTrendWindow)
	if !validSource(scope.Source) || scope.RepositoryID <= 0 || scope.WorkflowID <= 0 ||
		!validTime(observedAt) || !validTime(begin) || len(rows) > MaxRetainedTrendAttempts {
		return RetainedTrends{}, ErrTrendFacts
	}
	out := RetainedTrends{
		Previous: RetainedTrendWindow{From: begin, To: cutover},
		Current:  RetainedTrendWindow{From: cutover, To: observedAt},
	}
	buckets := [...]*RetainedTrendBucket{&out.Previous.Initial, &out.Previous.Retries, &out.Current.Initial, &out.Current.Retries}
	var samples [len(buckets)]retainedTrendSamples
	var runs [len(buckets)]map[int64]bool
	seen := make(map[AttemptKey]bool, len(rows))
	for _, row := range rows {
		key := row.Key
		if key.Source != scope.Source || key.RepositoryID != scope.RepositoryID || key.WorkflowID != scope.WorkflowID ||
			seen[key] || row.AsOf.After(observedAt) {
			return RetainedTrends{}, ErrTrendFacts
		}
		seen[key] = true
		measured, err := Measure(row)
		if err != nil {
			return RetainedTrends{}, ErrTrendFacts
		}
		if row.AsOf.Before(begin) {
			continue
		}
		i := 0
		if !row.AsOf.Before(cutover) {
			i += 2
		}
		if key.Attempt > 1 {
			i++
		}
		b, d := buckets[i], &samples[i]
		if runs[i] == nil {
			runs[i] = make(map[int64]bool)
		}
		runs[i][key.RunID] = true
		recordRetainedMetric(&b.Attempts, &d.attempts, measured.Phase, measured.Conclusion, measured.Wall)
		for _, job := range measured.Jobs {
			recordRetainedMetric(&b.Jobs, &d.jobs, job.Phase, job.Conclusion, job.Execution)
			for _, step := range job.Steps {
				recordRetainedMetric(&b.Steps, &d.steps, step.Phase, step.Conclusion, step.Duration)
			}
		}
	}
	for i, b := range buckets {
		b.Runs = len(runs[i])
		d := &samples[i]
		b.Attempts.Duration = summarizeTrendDurations(d.attempts)
		b.Jobs.Duration = summarizeTrendDurations(d.jobs)
		b.Steps.Duration = summarizeTrendDurations(d.steps)
	}
	return out, nil
}

func recordRetainedMetric(m *RetainedTrendMetric, samples *[]time.Duration, phase Phase, conclusion Conclusion, duration Span) {
	m.Observed++
	switch {
	case conclusion == ConclusionSuccess:
		m.Success++
	case conclusion == ConclusionFailure:
		m.Failure++
	case conclusion != ConclusionUnknown || phase == Skipped:
		m.Other++
	case phase == Queued || phase == Running:
		m.Active++
	default:
		m.Unknown++
	}
	if phase == Completed && duration.Basis == ObservedEndpoints {
		*samples = append(*samples, duration.Duration)
	}
}
