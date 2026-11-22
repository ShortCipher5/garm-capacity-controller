package cihealth

import (
	"fmt"
	"math"
	"testing"
	"time"
)

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
		in.Samples = append(in.Samples, JobSizingSample{Resources: resources, Shape: shape, Outcome: TrendOutcomeSuccess, RiskKnown: true})
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
			retry.Resources.Scope.RunnerIncarnation = "fixture-retry-incarnation"
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
		"different job from same attempt": func(in *JobSizingCohort) { in.Samples[1].Resources.Scope.Job.Attempt.RunID = 1 },
		"different repository":            func(in *JobSizingCohort) { in.Samples[1].Resources.Scope.Job.Attempt.RepositoryID++ },
		"different size":                  func(in *JobSizingCohort) { in.Samples[1].Shape.RAMBytes++ },
		"unknown risk with flags":         func(in *JobSizingCohort) { in.Samples[1].RiskKnown = false; in.Samples[1].Risks = JobSizingOOM },
		"contradictory coverage":          func(in *JobSizingCohort) { in.Samples[1].Resources.CPU.Coverage.Complete = true },
		"missing cadence":                 func(in *JobSizingCohort) { in.Samples[1].Resources.Scope.MaxGap = 0 },
		"oversized cadence":               func(in *JobSizingCohort) { in.Samples[1].Resources.Scope.MaxGap = MaxJobResourceGap + time.Nanosecond },
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
			in.Samples[1].Resources.Scope.Job.JobID++
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
		r.Scope.RunnerIncarnation = first.RunnerIncarnation
		r.Scope.StartedAt, r.Scope.EndAt, r.Scope.AsOf = r.Scope.StartedAt.Add(offset), r.Scope.EndAt.Add(offset), r.Scope.AsOf.Add(offset)
		r.RAM.LatestAt = r.RAM.LatestAt.Add(offset)
	}
	newSample(5 * time.Second)
	for _, attempted := range []int64{1, 2} {
		in.Samples[1].Resources.Scope.Job.Attempt.Attempt = attempted
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
