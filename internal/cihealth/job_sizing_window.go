package cihealth

import "time"

// MaxJobSizingReviewWindow matches CI Health's intended 30-day detail horizon.
const MaxJobSizingReviewWindow = 30 * 24 * time.Hour

// JobSizingReviewWindow describes a bounded, complete cohort that was selected
// before calling the reducer. AsOf is the trusted caller's evaluation time,
// not the time reported by any resource sample.
type JobSizingReviewWindow struct {
	From, AsOf             time.Time
	LatestCompletionMaxAge time.Duration
}

// JobSizingReview is a display-only result for an explicitly reviewed window.
// Neither validity nor freshness establishes provider capacity, authority, or
// that the caller independently verified its inputs.
type JobSizingReview struct {
	Summary                 JobSizingSummary
	Installed               JobSizingShape
	Window                  JobSizingReviewWindow
	LatestPrimaryCompletion time.Time
	FreshPrimaryCompletion  bool
}

// ReviewJobSizingWindow reuses the sizing reducer without silently dropping
// observations outside the requested window. Filtering after a cohort has
// been marked complete would let a caller unintentionally omit failures, risks
// or missing coverage. A retry cannot make old primary evidence fresh.
func ReviewJobSizingWindow(in JobSizingCohort, w JobSizingReviewWindow) (JobSizingReview, error) {
	w.From, w.AsOf = w.From.UTC(), w.AsOf.UTC()
	span := w.AsOf.Sub(w.From)
	if !validTime(w.From) || !validTime(w.AsOf) || span <= 0 || span > MaxJobSizingReviewWindow ||
		w.LatestCompletionMaxAge <= 0 || w.LatestCompletionMaxAge > span {
		return JobSizingReview{}, ErrJobSizingFacts
	}
	summary, err := SummarizeJobSizing(in)
	if err != nil {
		return JobSizingReview{}, err
	}
	out := JobSizingReview{Summary: summary, Window: w}
	if in.Comparability != JobSizingReviewedComparable || in.Shape.CPUMilli == 0 || in.Shape.RAMBytes == 0 {
		return out, nil
	}
	out.Installed = in.Shape
	for _, sample := range in.Samples {
		s := sample.Resources.Scope
		if s.StartedAt.Before(w.From) || s.EndAt.After(w.AsOf) || s.AsOf.After(w.AsOf) {
			return JobSizingReview{}, ErrJobSizingFacts
		}
		if s.Job.Attempt.Attempt == 1 && s.EndAt.After(out.LatestPrimaryCompletion) {
			out.LatestPrimaryCompletion = s.EndAt.UTC()
		}
	}
	out.FreshPrimaryCompletion = !out.LatestPrimaryCompletion.IsZero() &&
		w.AsOf.Sub(out.LatestPrimaryCompletion) <= w.LatestCompletionMaxAge
	if !out.FreshPrimaryCompletion {
		withholdStaleSizing(&out.Summary.CPU)
		withholdStaleSizing(&out.Summary.RAM)
	}
	return out, nil
}

func withholdStaleSizing(m *JobSizingMetric) {
	if m.State == JobSizingIncreaseAdvised || m.State == JobSizingNoIncrease {
		m.State, m.Suggested = JobSizingStaleHistory, nil
	}
}
