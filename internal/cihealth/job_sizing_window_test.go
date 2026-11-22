package cihealth

import (
	"errors"
	"testing"
	"time"
)

func TestReviewJobSizingWindowWithholdsStaleAdviceDespiteFreshRetry(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	summary, err := SummarizeJobSizing(in)
	if err != nil || summary.CPU.State != JobSizingIncreaseAdvised || summary.RAM.State != JobSizingIncreaseAdvised {
		t.Fatalf("reducer baseline = %+v, %v", summary, err)
	}
	end := in.Samples[len(in.Samples)-1].Resources.Scope.EndAt
	start := in.Samples[0].Resources.Scope.StartedAt
	window := JobSizingReviewWindow{From: start, AsOf: end.Add(time.Hour), LatestCompletionMaxAge: time.Hour}
	review, err := ReviewJobSizingWindow(in, window)
	if err != nil || !review.FreshPrimaryCompletion || !review.LatestPrimaryCompletion.Equal(end) ||
		review.Installed != in.Shape || review.Summary.CPU.State != JobSizingIncreaseAdvised || review.Summary.CPU.Suggested == nil {
		t.Fatalf("exact freshness boundary = %+v, %v", review, err)
	}
	window.AsOf = window.AsOf.Add(time.Nanosecond)
	retry := jobSizingFixture(t, 1, 0, 9*time.Second).Samples[0]
	r := &retry.Resources.Scope
	r.Job.Attempt.Attempt = 2
	offset := window.AsOf.Sub(r.EndAt)
	r.StartedAt, r.EndAt, r.AsOf = r.StartedAt.Add(offset), r.EndAt.Add(offset), window.AsOf
	retry.Resources.RAM.LatestAt = retry.Resources.RAM.LatestAt.Add(offset)
	retry.Completion = sizingCompletionFixture(*r)
	in.Samples = append(in.Samples, retry)
	review, err = ReviewJobSizingWindow(in, window)
	if err != nil || review.FreshPrimaryCompletion || !review.LatestPrimaryCompletion.Equal(end) ||
		review.Summary.Retried != 1 || review.Summary.CPU.State != JobSizingStaleHistory ||
		review.Summary.RAM.State != JobSizingStaleHistory || review.Summary.CPU.Suggested != nil || review.Summary.RAM.Suggested != nil {
		t.Fatalf("fresh retry refreshed old primary advice: %+v, %v", review, err)
	}
}

func TestReviewJobSizingWindowRefusesOutOfWindowAndFutureEvidence(t *testing.T) {
	for _, name := range []string{"window cuts into job", "observation after review", "completion observation after review", "too wide", "missing max age"} {
		t.Run(name, func(t *testing.T) {
			in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
			w := JobSizingReviewWindow{
				From: in.Samples[0].Resources.Scope.StartedAt, AsOf: in.Samples[len(in.Samples)-1].Resources.Scope.AsOf,
				LatestCompletionMaxAge: time.Hour,
			}
			switch name {
			case "window cuts into job":
				w.From = w.From.Add(time.Nanosecond)
			case "observation after review":
				in.Samples[0].Resources.Scope.AsOf = w.AsOf.Add(time.Nanosecond)
			case "completion observation after review":
				in.Samples[0].Completion.ObservedAt = w.AsOf.Add(time.Nanosecond)
			case "too wide":
				w.From = w.AsOf.Add(-MaxJobSizingReviewWindow - time.Nanosecond)
			case "missing max age":
				w.LatestCompletionMaxAge = 0
			}
			got, err := ReviewJobSizingWindow(in, w)
			if !errors.Is(err, ErrJobSizingFacts) || got != (JobSizingReview{}) {
				t.Fatalf("out-of-window evidence returned a partial result: %+v, %v", got, err)
			}
		})
	}
}

func TestReviewJobSizingWindowPreservesExistingCoverageAndRiskBlockers(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	in.Samples[0].Risks = JobSizingOOM
	w := JobSizingReviewWindow{
		From:                   in.Samples[0].Resources.Scope.StartedAt,
		AsOf:                   in.Samples[len(in.Samples)-1].Resources.Scope.EndAt.Add(2 * time.Hour),
		LatestCompletionMaxAge: time.Hour,
	}
	got, err := ReviewJobSizingWindow(in, w)
	if err != nil || got.FreshPrimaryCompletion || got.Summary.OOM != 1 || got.Summary.CPU.State != JobSizingNeedsRiskOrOutcomeReview ||
		got.Summary.CPU.Suggested != nil || got.Summary.Disk.State != JobSizingCoverageUnavailable {
		t.Fatalf("recency masked another blocker: %+v, %v", got, err)
	}
}

func TestReviewJobSizingWindowDoesNotUseUnconfirmedCutoffAsCompletion(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	last := &in.Samples[len(in.Samples)-1]
	last.Outcome = TrendOutcomeUnknown
	last.Completion = JobSizingCompletion{}
	window := JobSizingReviewWindow{From: in.Samples[0].Resources.Scope.StartedAt, AsOf: last.Resources.Scope.AsOf,
		LatestCompletionMaxAge: time.Hour}
	review, err := ReviewJobSizingWindow(in, window)
	want := in.Samples[len(in.Samples)-2].Completion.EndedAt
	if err != nil || review.FreshPrimaryCompletion || !review.LatestPrimaryCompletion.Equal(want) ||
		review.Summary.CPU.State != JobSizingNeedsRiskOrOutcomeReview {
		t.Fatalf("unconfirmed cutoff reported as fresh primary completion: %+v, %v", review, err)
	}
}

func TestReviewJobSizingWindowUsesActualCompletionButWithholdsPrefixAdvice(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	last := &in.Samples[len(in.Samples)-1]
	last.Completion.EndedAt = last.Resources.Scope.EndAt.Add(2 * time.Hour)
	last.Completion.ObservedAt = last.Completion.EndedAt.Add(time.Second)
	w := JobSizingReviewWindow{From: in.Samples[0].Resources.Scope.StartedAt, AsOf: last.Completion.ObservedAt,
		LatestCompletionMaxAge: time.Hour}
	got, err := ReviewJobSizingWindow(in, w)
	if err != nil || !got.FreshPrimaryCompletion || !got.LatestPrimaryCompletion.Equal(last.Completion.EndedAt) ||
		got.Summary.CPU.State != JobSizingCoverageUnavailable || got.Summary.RAM.State != JobSizingCoverageUnavailable {
		t.Fatalf("valid late completion should be fresh without qualifying prefix metrics: %+v, %v", got, err)
	}
}
