package cihealth

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func sizingCompletionFixture(w JobResourceScope) JobSizingCompletion {
	return JobSizingCompletion{Job: w.Job, StartedAt: w.StartedAt, EndedAt: w.EndAt, ObservedAt: w.AsOf}
}

func jobSizingFixture(t *testing.T, primary int, span, covered time.Duration) JobSizingCohort {
	t.Helper()
	shape := JobSizingShape{1000, 100}
	in := JobSizingCohort{
		Scope: TrendScope{Source: "fixture", RepositoryID: 1, WorkflowID: 2}, Shape: shape,
		Comparability: JobSizingReviewedComparable, Coverage: TrendCoverageComplete,
	}
	for i := range primary {
		w := resourceScopeFixture(10*time.Second, 9*time.Second)
		w.Job.Attempt.RunID = int64(i + 1)
		w.Job.JobID = int64(i + 100)
		w.Job.Attempt.Attempt = 1
		w.RunnerIncarnation = fmt.Sprintf("fixture-incarnation-%d", i)
		offset := time.Duration(i) * span / time.Duration(max(1, primary-1))
		w.StartedAt, w.EndAt, w.AsOf = w.StartedAt.Add(offset), w.EndAt.Add(offset), w.AsOf.Add(offset)
		first, last := resourceSampleFixture(w, 0), resourceSampleFixture(w, covered)
		first.CPUTimeNanos, last.CPUTimeNanos = knownResource(0), knownResource(9*uint64(covered)/10)
		first.RAMBytes, last.RAMBytes = knownResource(90), knownResource(96)
		resources, err := SummarizeJobResources(w, []JobResourceSample{first, last})
		if err != nil {
			t.Fatal(err)
		}
		in.Samples = append(in.Samples, JobSizingSample{Resources: resources, Completion: sizingCompletionFixture(w), Shape: shape, Outcome: TrendOutcomeSuccess, RiskKnown: true})
	}
	return in
}

func TestJobSizingVerifiesExactPrimaryHistoryAndCoverageThresholds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		primary     int
		span, cover time.Duration
		want        JobSizingMetricState
	}{
		{"exact requirements", 20, 7 * 24 * time.Hour, 9 * time.Second, JobSizingIncreaseAdvised},
		{"too few independent primary attempts", 19, 7 * 24 * time.Hour, 9 * time.Second, JobSizingInsufficientHistory},
		{"too short", 20, 7*24*time.Hour - 1, 9 * time.Second, JobSizingInsufficientHistory},
		{"90 percent is not rounded", 20, 7 * 24 * time.Hour, 9*time.Second - 1, JobSizingCoverageUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := jobSizingFixture(t, tc.primary, tc.span, tc.cover)
			retry := in.Samples[0]
			retry.Resources.Scope.Job.Attempt.Attempt = 2
			retry.Resources.Scope.Job.JobID = 1000
			retry.Resources.Scope.RunnerIncarnation = "fixture-retry-incarnation"
			retry.Completion.Job = retry.Resources.Scope.Job
			in.Samples = append(in.Samples, retry)
			got, err := SummarizeJobSizing(in)
			if err != nil || got.Primary != tc.primary || got.Successful != tc.primary || got.Retried != 1 || got.CPU.State != tc.want || got.RAM.State != tc.want ||
				got.Disk.State != JobSizingCoverageUnavailable || got.Disk.Suggested != nil {
				t.Fatalf("threshold result: %+v, %v", got, err)
			}
			if tc.want == JobSizingIncreaseAdvised {
				if got.CPU.Suggested == nil || *got.CPU.Suggested != 1125 || got.RAM.Suggested == nil || *got.RAM.Suggested != 120 {
					t.Fatalf("bounded recommendations: %+v", got)
				}
			} else if got.CPU.Suggested != nil || got.RAM.Suggested != nil {
				t.Fatalf("insufficient evidence produced sizes: %+v", got)
			}
		})
	}
}

func TestJobSizingKeepsAtRiskUnknownAndMissingMetricsSeparate(t *testing.T) {
	in := jobSizingFixture(t, 21, MinJobSizingHistory, 9*time.Second)
	in.Samples[10].Resources.CPU = nil // 20 good CPU rows cannot hide this missing row.
	got, err := SummarizeJobSizing(in)
	if err != nil || got.CPU.State != JobSizingCoverageUnavailable || got.CPU.QualifyingSamples != 20 || got.CPU.Suggested != nil || got.RAM.State != JobSizingIncreaseAdvised {
		t.Fatalf("metrics did not preserve independent coverage: %+v, %v", got, err)
	}
	in.Samples[1].Risks = JobSizingThrottled | JobSizingOOM
	in.Samples[2].Risks = JobSizingDiskFull | JobSizingExternalBuilder
	in.Samples[3].RiskKnown = false
	in.Samples[4].Outcome = TrendOutcomeFailure
	got, err = SummarizeJobSizing(in)
	if err != nil || got.Successful != 17 || got.AtRisk != 2 || got.Unknown != 1 || got.Unsuccessful != 1 ||
		got.Throttled != 1 || got.OOM != 1 || got.DiskFull != 1 || got.ExternalBuilder != 1 ||
		got.CPU.State != JobSizingNeedsRiskOrOutcomeReview || got.RAM.State != JobSizingNeedsRiskOrOutcomeReview || got.CPU.Suggested != nil || got.RAM.Suggested != nil {
		t.Fatalf("risk or missing evidence became advice: %+v, %v", got, err)
	}
}

func TestJobSizingNoDownsizeTrimmedAdviceOrUnreviewedCohort(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	for _, coverage := range []TrendCoverage{TrendCoverageUnknown, TrendCoveragePartial} {
		in.Coverage = coverage
		got, err := SummarizeJobSizing(in)
		if err != nil || got.Coverage != coverage || got.CPU.State != JobSizingCohortIncomplete || got.CPU.Suggested != nil || got.RAM.Suggested != nil {
			t.Fatalf("incomplete cohort produced advice: %+v, %v", got, err)
		}
	}
	in.Coverage = TrendCoverageComplete
	in.Comparability = JobSizingComparabilityUnknown
	if got, err := SummarizeJobSizing(in); err != nil || got.Primary != 0 || got.CPU.State != JobSizingUnknown || got.RAM.State != JobSizingUnknown {
		t.Fatalf("unverified comparison consumed samples: %+v, %v", got, err)
	}
	in.Comparability = JobSizingReviewedComparable
	in.Shape = JobSizingShape{CPUMilli: 4000, RAMBytes: math.MaxUint64}
	for i := range in.Samples {
		in.Samples[i].Shape = in.Shape
	}
	in.Samples[0].Resources.RAM.SampleMax = math.MaxUint64
	// Above the configured step is not a permit to emit the clipped target;
	// arithmetic overflow is similarly withheld rather than wrapping to zero.
	m := JobSizingMetric{QualifyingSamples: 20}
	finalizeJobSizingMetric(&m, 20, 1001, 1000)
	got, err := SummarizeJobSizing(in)
	if err != nil || got.CPU.State != JobSizingNoIncrease || got.CPU.Suggested != nil || got.RAM.State != JobSizingNeedsManualReview || got.RAM.Suggested != nil || m.State != JobSizingNeedsManualReview || m.Suggested != nil {
		t.Fatalf("unsafe headroom advice: %+v, %v (%+v)", got, err, m)
	}
}

func TestJobSizingRefusesDuplicateRunAndForeignOrContradictoryEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*JobSizingCohort){
		"different job from same attempt": func(in *JobSizingCohort) {
			in.Samples[1].Resources.Scope.Job.Attempt.RunID = 1
			in.Samples[1].Completion.Job = in.Samples[1].Resources.Scope.Job
		},
		"different repository": func(in *JobSizingCohort) {
			in.Samples[1].Resources.Scope.Job.Attempt.RepositoryID++
			in.Samples[1].Completion.Job = in.Samples[1].Resources.Scope.Job
		},
		"different size":          func(in *JobSizingCohort) { in.Samples[1].Shape.RAMBytes++ },
		"unknown risk with flags": func(in *JobSizingCohort) { in.Samples[1].RiskKnown = false; in.Samples[1].Risks = JobSizingOOM },
		"contradictory coverage":  func(in *JobSizingCohort) { in.Samples[1].Resources.CPU.Coverage.Complete = true },
		"missing cadence":         func(in *JobSizingCohort) { in.Samples[1].Resources.Scope.MaxGap = 0 },
		"oversized cadence":       func(in *JobSizingCohort) { in.Samples[1].Resources.Scope.MaxGap = MaxJobResourceGap + time.Nanosecond },
		"CPU coverage exceeds cadence": func(in *JobSizingCohort) {
			in.Samples[1].Resources.Scope.MaxGap = 9*time.Second - time.Nanosecond
			in.Samples[1].Resources.RAMMean = nil
		},
		"RAM coverage exceeds cadence": func(in *JobSizingCohort) {
			in.Samples[1].Resources.Scope.MaxGap = 9*time.Second - time.Nanosecond
			in.Samples[1].Resources.CPU = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
			in.Samples[1].Completion.Job = in.Samples[1].Resources.Scope.Job
			mutate(&in)
			if got, err := SummarizeJobSizing(in); err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
				t.Fatalf("invalid cohort returned a partial result: %+v, %v", got, err)
			}
		})
	}
}

func TestJobSizingRefusesCoverageThatContradictsSamplingHoles(t *testing.T) {
	for name, change := range map[string]func(*JobResourceSummary){
		"reported CPU coverage crosses missing edge": func(r *JobResourceSummary) {
			r.Sampling.AfterLastSample = 2 * time.Second
		},
		"two samples cannot bridge an observed cadence gap": func(r *JobResourceSummary) {
			r.Sampling = ResourceSamplingCoverage{CadenceGaps: 1, CadenceGapDuration: 10 * time.Second}
		},
		"gap duration does not exceed maximum cadence": func(r *JobResourceSummary) {
			r.Sampling = ResourceSamplingCoverage{CadenceGaps: 1, CadenceGapDuration: 9 * time.Second}
		},
		"single recorded pair cannot leave unmatched time": func(r *JobResourceSummary) {
			r.CPU.Coverage.Duration--
		},
		"metric reported on empty series": func(r *JobResourceSummary) {
			r.Sampling = ResourceSamplingCoverage{NoSamples: r.Window}
		},
		"point gauge outside observed sample envelope": func(r *JobResourceSummary) {
			r.RAM.LatestAt = r.Scope.EndAt
		},
		"sampling gap duration overflows when added": func(r *JobResourceSummary) {
			r.Sampling.BeforeFirstSample = time.Duration(math.MaxInt64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
			change(&in.Samples[0].Resources)
			got, err := SummarizeJobSizing(in)
			if err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
				t.Fatalf("inconsistent sampling produced advice: %+v, %v", got, err)
			}
		})
	}
}

func TestJobSizingAcceptsRecordedSamplingGapsAndEmptyEvidenceWithoutAdvice(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	scope := in.Samples[0].Resources.Scope
	scope.MaxGap = 3 * time.Second
	for _, offsets := range [][]time.Duration{nil, {0, 2 * time.Second, 7 * time.Second, 9 * time.Second}} {
		var samples []JobResourceSample
		for _, offset := range offsets {
			s := resourceSampleFixture(scope, offset)
			s.CPUTimeNanos = knownResource(uint64(offset))
			s.RAMBytes = knownResource(96)
			samples = append(samples, s)
		}
		observed, err := SummarizeJobResources(scope, samples)
		if err != nil {
			t.Fatal(err)
		}
		in.Samples[0].Resources = observed
		got, err := SummarizeJobSizing(in)
		if err != nil || got.CPU.State != JobSizingCoverageUnavailable || got.RAM.State != JobSizingCoverageUnavailable ||
			got.CPU.Suggested != nil || got.RAM.Suggested != nil {
			t.Fatalf("honest missing coverage not reported: %+v, %v", got, err)
		}
	}
}

func TestJobSizingRejectsOverlappingWindowsOnOneRunnerIncarnation(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	first := in.Samples[0].Resources.Scope
	newSample := func(offset time.Duration) {
		in.Samples[1].Resources = jobSizingFixture(t, 1, 0, 9*time.Second).Samples[0].Resources
		r := &in.Samples[1].Resources
		r.Scope.Job.Attempt.RunID = 2
		r.Scope.Job.JobID = 101
		r.Scope.RunnerIncarnation = first.RunnerIncarnation
		r.Scope.StartedAt, r.Scope.EndAt, r.Scope.AsOf = r.Scope.StartedAt.Add(offset), r.Scope.EndAt.Add(offset), r.Scope.AsOf.Add(offset)
		r.RAM.LatestAt = r.RAM.LatestAt.Add(offset)
		in.Samples[1].Completion = sizingCompletionFixture(r.Scope)
	}
	newSample(5 * time.Second)
	for _, attempted := range []int64{1, 2} {
		in.Samples[1].Resources.Scope.Job.Attempt.Attempt = attempted
		in.Samples[1].Completion.Job.Attempt.Attempt = attempted
		in.Samples[0], in.Samples[1] = in.Samples[1], in.Samples[0] // later start comes first
		got, err := SummarizeJobSizing(in)
		if err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
			t.Errorf("overlapping window on attempt %d returned a result: %+v, %v", attempted, got, err)
		}
		in.Samples[0], in.Samples[1] = in.Samples[1], in.Samples[0]
	}

	newSample(first.EndAt.Sub(first.StartedAt))
	got, err := SummarizeJobSizing(in)
	if err != nil || got.Successful != 20 || got.CPU.State != JobSizingIncreaseAdvised {
		t.Fatalf("touching windows have no positive overlap: %+v, %v", got, err)
	}
	newSample(5 * time.Second)
	in.Samples[1].Resources.Scope.RunnerIncarnation = "different-runner-incarnation"
	got, err = SummarizeJobSizing(in)
	if err != nil || got.Successful != 20 || got.CPU.State != JobSizingIncreaseAdvised {
		t.Fatalf("different runner incarnations can have simultaneous jobs: %+v, %v", got, err)
	}
}

func TestJobSizingRefusesImpossibleRAMGaugeAndMean(t *testing.T) {
	for name, change := range map[string]func(*JobResourceSummary){
		"mean exceeds span ending at latest gauge": func(r *JobResourceSummary) {
			r.RAM.LatestAt = r.RAM.LatestAt.Add(-time.Nanosecond)
		},
		"multiple gauges cannot occur at first sample": func(r *JobResourceSummary) {
			r.RAMMean = nil
			r.RAM.LatestAt = r.Scope.StartedAt.Add(r.Sampling.BeforeFirstSample)
		},
		"one gauge must be its own maximum": func(r *JobResourceSummary) {
			r.RAMMean = nil
			r.RAM.Samples = 1
			r.RAM.SampleMax++
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
			change(&in.Samples[0].Resources)
			if got, err := SummarizeJobSizing(in); err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
				t.Fatalf("impossible RAM samples produced sizing summary: %+v, %v", got, err)
			}
		})
	}
}

func TestJobSizingKeepsLoneRAMGaugeAndCPUCoverageIndependent(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	r := &in.Samples[0].Resources
	r.RAM = &ResourceGaugeSummary{Latest: 90, SampleMax: 90, LatestAt: r.Scope.StartedAt, Samples: 1}
	r.RAMMean = nil
	got, err := SummarizeJobSizing(in)
	if err != nil || got.CPU.State != JobSizingIncreaseAdvised || got.RAM.State != JobSizingCoverageUnavailable || got.RAM.Suggested != nil {
		t.Fatalf("lone gauge should not fabricate coverage or suppress valid CPU advice: %+v, %v", got, err)
	}
}

func TestJobSizingWithholdsAdviceFromFullyCoveredJobPrefix(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	s := &in.Samples[len(in.Samples)-1]
	scope := s.Resources.Scope
	scope.MaxGap = scope.EndAt.Sub(scope.StartedAt)
	first, last := resourceSampleFixture(scope, 0), resourceSampleFixture(scope, scope.MaxGap)
	first.CPUTimeNanos, last.CPUTimeNanos = knownResource(0), knownResource(9*uint64(scope.MaxGap)/10)
	first.RAMBytes, last.RAMBytes = knownResource(90), knownResource(96)
	var err error
	s.Resources, err = SummarizeJobResources(scope, []JobResourceSample{first, last})
	if err != nil || !s.Resources.CPU.Coverage.Complete || !s.Resources.RAMMean.Coverage.Complete {
		t.Fatalf("expected complete prefix coverage: %+v, %v", s.Resources, err)
	}
	// Separate terminal evidence arrived later; the resource cutoff is not a job end.
	s.Completion.EndedAt = s.Resources.Scope.EndAt.Add(10 * time.Minute)
	s.Completion.ObservedAt = s.Completion.EndedAt.Add(time.Second)
	got, err := SummarizeJobSizing(in)
	if err != nil || got.Successful != 20 || got.CPU.QualifyingSamples != 19 ||
		got.RAM.QualifyingSamples != 19 || got.CPU.State != JobSizingCoverageUnavailable ||
		got.RAM.State != JobSizingCoverageUnavailable || got.CPU.Suggested != nil || got.RAM.Suggested != nil {
		t.Fatalf("resource prefix counted as completed-job evidence: %+v, %v", got, err)
	}
}

func TestJobSizingTreatsMissingCompletionAsUnknownAndConflictsAsInvalid(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	in.Samples[0].Completion = JobSizingCompletion{}
	got, err := SummarizeJobSizing(in)
	if err != nil || got.Unknown != 1 || got.Successful != 19 ||
		got.CPU.State != JobSizingNeedsRiskOrOutcomeReview || got.RAM.State != JobSizingNeedsRiskOrOutcomeReview {
		t.Fatalf("reported success lacks a confirmed completion: %+v, %v", got, err)
	}
	for name, change := range map[string]func(*JobSizingCompletion){
		"foreign job":            func(c *JobSizingCompletion) { c.Job.JobID++ },
		"different start":        func(c *JobSizingCompletion) { c.StartedAt = c.StartedAt.Add(time.Nanosecond) },
		"end before cutoff":      func(c *JobSizingCompletion) { c.EndedAt = c.EndedAt.Add(-time.Nanosecond) },
		"observation before end": func(c *JobSizingCompletion) { c.ObservedAt = c.EndedAt.Add(-time.Nanosecond) },
		"partial record":         func(c *JobSizingCompletion) { c.ObservedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
			change(&in.Samples[0].Completion)
			got, err := SummarizeJobSizing(in)
			if err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
				t.Fatalf("contradictory completion returned a result: %+v, %v", got, err)
			}
		})
	}
}

func TestJobSizingRejectsOverlapInUnsampledTailOfKnownJob(t *testing.T) {
	in := jobSizingFixture(t, 20, 8*24*time.Hour, 9*time.Second)
	first, later := &in.Samples[0], &in.Samples[1]
	later.Resources.Scope.RunnerIncarnation = first.Resources.Scope.RunnerIncarnation
	// The first job's resource cutoff precedes the second job, but its
	// independently observed end confirms it was still running.
	first.Completion.EndedAt = later.Resources.Scope.StartedAt.Add(time.Second)
	first.Completion.ObservedAt = first.Completion.EndedAt
	got, err := SummarizeJobSizing(in)
	if err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
		t.Fatalf("unsampled overlapping job tail accepted: %+v, %v", got, err)
	}
	first.Completion.EndedAt = later.Resources.Scope.StartedAt
	got, err = SummarizeJobSizing(in)
	if err != nil || got.CPU.State != JobSizingCoverageUnavailable || got.CPU.QualifyingSamples != 19 {
		t.Fatalf("exact boundary should accept both jobs but withhold prefix advice: %+v, %v", got, err)
	}
}

func TestJobSizingRejectsUnconfirmedRetryBeforeLaterJobOnSameRunner(t *testing.T) {
	in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	retry := in.Samples[0]
	retry.Resources.Scope.Job.Attempt.Attempt = 2
	retry.Resources.Scope.Job.JobID = 1000
	completion := sizingCompletionFixture(retry.Resources.Scope)
	retry.Completion = JobSizingCompletion{}
	retry.Outcome = TrendOutcomeUnknown
	in.Samples = append(in.Samples, retry)
	// There is no ambiguity on a different incarnation.
	in.Samples[0].Resources.Scope.RunnerIncarnation = "another-runner"
	got, err := SummarizeJobSizing(in)
	if err != nil || got.CPU.State != JobSizingIncreaseAdvised || got.Retried != 1 {
		t.Fatalf("an unrelated retry should not poison proven primary data: %+v, %v", got, err)
	}
	in.Samples[1].Resources.Scope.RunnerIncarnation = retry.Resources.Scope.RunnerIncarnation
	got, err = SummarizeJobSizing(in)
	if err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
		t.Fatalf("unconfirmed retry tail could overlap later primary: %+v, %v", got, err)
	}
	in.Samples[len(in.Samples)-1].Completion = completion
	got, err = SummarizeJobSizing(in)
	if err != nil || got.CPU.State != JobSizingIncreaseAdvised || got.Retried != 1 {
		t.Fatalf("confirmed non-overlap should restore advice: %+v, %v", got, err)
	}
}

func TestJobSizingRejectsJobIDReusedAcrossAttempts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		run, attempt int64
	}{
		{"distinct primary runs", 2, 1},
		{"same run, different attempt", 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := jobSizingFixture(t, MinJobSizingPrimaryAttempts, MinJobSizingHistory, 9*time.Second)
			a, b := in.Samples[0].Resources.Scope.Job, &in.Samples[1]
			b.Resources.Scope.Job.Attempt.RunID = tc.run
			b.Resources.Scope.Job.Attempt.Attempt = tc.attempt
			b.Resources.Scope.Job.JobID = a.JobID
			b.Completion.Job = b.Resources.Scope.Job
			got, err := SummarizeJobSizing(in)
			if err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
				t.Fatalf("reused numeric job ID returned a result: %+v, %v", got, err)
			}
		})
	}
}

func TestJobSizingChecksCompleteRAMSampleSeries(t *testing.T) {
	var trailingCadenceGap []int
	for i := 0; i <= 18; i++ {
		trailingCadenceGap = append(trailingCadenceGap, i)
	}
	trailingCadenceGap = append(trailingCadenceGap, 20)
	for _, tc := range []struct {
		name        string
		times       []int
		gap         int
		missingLast bool
		mutate      func(*JobResourceSummary)
		want        JobSizingMetricState
	}{
		{name: "full RAM series", times: []int{0, 9, 10}, gap: 9, want: JobSizingIncreaseAdvised},
		{name: "all RAM with a real cadence gap", times: trailingCadenceGap, gap: 1, want: JobSizingIncreaseAdvised},
		{name: "all RAM and no eligible pairs", times: []int{0, 10}, gap: 1, want: JobSizingCoverageUnavailable},
		{name: "partial RAM series may omit its final reading", times: []int{0, 9, 10}, gap: 9, missingLast: true, want: JobSizingIncreaseAdvised},
		{name: "all RAM mean cannot omit an eligible pair", times: []int{0, 9, 10}, gap: 9, mutate: func(r *JobResourceSummary) {
			r.RAMMean.Coverage = ResourceCoverage{Intervals: 1, Duration: 9 * time.Second}
		}},
		{name: "all RAM mean cannot be absent when pairs exist", times: []int{0, 9, 10}, gap: 9, mutate: func(r *JobResourceSummary) {
			r.RAMMean = nil
		}},
		{name: "all RAM latest cannot predate the final sample even with a trailing cadence gap", times: trailingCadenceGap, gap: 1, mutate: func(r *JobResourceSummary) {
			r.RAM.LatestAt = r.RAM.LatestAt.Add(-time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
			item := &in.Samples[1]
			scope := item.Resources.Scope
			scope.EndAt = scope.StartedAt.Add(time.Duration(tc.times[len(tc.times)-1]) * time.Second)
			scope.AsOf = scope.EndAt.Add(time.Second)
			scope.MaxGap = time.Duration(tc.gap) * time.Second
			var samples []JobResourceSample
			for i, at := range tc.times {
				s := resourceSampleFixture(scope, time.Duration(at)*time.Second)
				if !tc.missingLast || i != len(tc.times)-1 {
					s.RAMBytes = knownResource(96)
				}
				samples = append(samples, s)
			}
			var err error
			item.Resources, err = SummarizeJobResources(scope, samples)
			if err != nil {
				t.Fatal(err)
			}
			item.Completion = sizingCompletionFixture(scope)
			if tc.mutate != nil {
				tc.mutate(&item.Resources)
			}
			got, err := SummarizeJobSizing(in)
			if tc.mutate != nil {
				if err != ErrJobSizingFacts || got != (JobSizingSummary{}) {
					t.Fatalf("contradictory RAM evidence accepted: %+v, %v", got, err)
				}
				return
			}
			if err != nil || got.RAM.State != tc.want {
				t.Fatalf("genuine reducer output refused or misclassified: %+v, %v", got, err)
			}
		})
	}
}
