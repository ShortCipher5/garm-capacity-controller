package cihealth

import (
	"errors"
	"math"
	"time"
)

const (
	MinJobSizingPrimaryAttempts = 20
	MinJobSizingHistory         = 7 * 24 * time.Hour
)

var ErrJobSizingFacts = errors.New("invalid or oversized CI sizing facts")

type JobSizingComparability uint8

const (
	JobSizingComparabilityUnknown JobSizingComparability = iota
	// JobSizingReviewedComparable is only set after independent workload and
	// resource attribution review. It is not evidence that review occurred.
	JobSizingReviewedComparable
)

type JobSizingRisk uint8

const (
	JobSizingThrottled JobSizingRisk = 1 << iota
	JobSizingOOM
	JobSizingDiskFull
	JobSizingExternalBuilder
)

const knownJobSizingRisks = JobSizingThrottled | JobSizingOOM | JobSizingDiskFull | JobSizingExternalBuilder

type JobSizingShape struct {
	CPUMilli, RAMBytes uint64
}

// JobSizingSample carries already verified, attributable telemetry from the
// existing reducer plus the independently observed exact job outcome, risks
// and installed limits. Missing risk evidence is not an absence of risk.
type JobSizingSample struct {
	Resources JobResourceSummary
	Shape     JobSizingShape
	Outcome   TrendOutcome
	RiskKnown bool
	Risks     JobSizingRisk
}

type JobSizingCohort struct {
	// No job-name matching, automatic grouping or provider inference is done.
	// Scope is one exact source/repo/workflow; the caller additionally owns
	// establishing that the selected jobs, environment and inputs are comparable.
	Scope         TrendScope
	Comparability JobSizingComparability
	Coverage      TrendCoverage  // of this reviewed cohort, not global workflow history
	Shape         JobSizingShape // same reviewed installed limit across samples
	Samples       []JobSizingSample
}

type JobSizingMetricState uint8

const (
	JobSizingUnknown JobSizingMetricState = iota
	JobSizingCohortIncomplete
	JobSizingInsufficientHistory
	JobSizingNeedsRiskOrOutcomeReview
	JobSizingCoverageUnavailable
	JobSizingNoIncrease
	JobSizingIncreaseAdvised
	JobSizingNeedsManualReview
	// JobSizingStaleHistory is reported by the windowed review consumer.
	JobSizingStaleHistory
)

type JobSizingMetric struct {
	State             JobSizingMetricState
	QualifyingSamples int
	// Suggested is non-nil only for an upward-only advisory target within
	// 25% of the installed shape. It does not establish sufficient capacity.
	Suggested *uint64
}

type JobSizingSummary struct {
	Scope                                     TrendScope
	Coverage                                  TrendCoverage
	Primary, Retried, Successful              int // Successful means primary, known risk-free success.
	AtRisk, Unknown, Unsuccessful             int
	Throttled, OOM, DiskFull, ExternalBuilder int
	CPU, RAM, Disk                            JobSizingMetric
}

// SummarizeJobSizing is a display-only comparison, never a scaler input.
// Recentness, physical headroom, settings writes and authority are outside
// this reducer. Only explicitly reviewed comparable primary jobs count.
// It never recommends reducing resources, even with complete clean evidence.
func SummarizeJobSizing(in JobSizingCohort) (JobSizingSummary, error) {
	fail := func() (JobSizingSummary, error) { return JobSizingSummary{}, ErrJobSizingFacts }
	if !validSource(in.Scope.Source) || in.Scope.RepositoryID <= 0 || in.Scope.WorkflowID <= 0 ||
		in.Comparability > JobSizingReviewedComparable || in.Coverage > TrendCoverageComplete || len(in.Samples) > MaxTrendAttempts {
		return fail()
	}
	out := JobSizingSummary{Scope: in.Scope, Coverage: in.Coverage, Disk: JobSizingMetric{State: JobSizingCoverageUnavailable}}
	if in.Comparability == JobSizingComparabilityUnknown || in.Shape.CPUMilli == 0 || in.Shape.RAMBytes == 0 {
		return out, nil
	}
	seen := make(map[AttemptKey]bool, len(in.Samples))
	var first, last time.Time
	var highestCPU, highestRAM uint64
	for _, sample := range in.Samples {
		r := sample.Resources
		job := r.Scope.Job
		key := job.Attempt
		start, end := r.Scope.StartedAt.UTC(), r.Scope.EndAt.UTC()
		if !validKey(key) || key.Source != in.Scope.Source || key.RepositoryID != in.Scope.RepositoryID || key.WorkflowID != in.Scope.WorkflowID ||
			job.JobID <= 0 || seen[key] || !validSource(r.Scope.RunnerIncarnation) ||
			!validTime(start) || !validTime(end) || !validTime(r.Scope.AsOf.UTC()) || end.After(r.Scope.AsOf.UTC()) ||
			r.Window != end.Sub(start) || r.Window <= 0 || r.Window > MaxJobResourceWindow || r.Samples < 0 || r.Samples > MaxJobResourceSamples ||
			r.Scope.MaxGap <= 0 || r.Scope.MaxGap > MaxJobResourceGap ||
			sample.Shape != in.Shape || sample.Outcome > TrendOutcomeOtherTerminal || sample.Risks&^knownJobSizingRisks != 0 || !sample.RiskKnown && sample.Risks != 0 {
			return fail()
		}
		cadence, ok := jobSizingSamplingCeiling(r)
		if !ok {
			return fail()
		}
		cpu, ram := r.CPU, r.RAMMean
		if cpu != nil && (cpu.AverageMilliCPU > cpu.PeakIntervalMilliCPU || !sizingCoverageValid(cpu.Coverage, r.Window, r.Scope.MaxGap, r.Samples, cadence)) ||
			r.RAM != nil && (r.RAM.Samples < 1 || r.RAM.Samples > r.Samples || r.RAM.Latest > r.RAM.SampleMax ||
				r.RAM.LatestAt.Before(start.Add(r.Sampling.BeforeFirstSample)) || r.RAM.LatestAt.After(end.Add(-r.Sampling.AfterLastSample))) ||
			ram != nil && (r.RAM == nil || ram.Bytes > r.RAM.SampleMax || !sizingCoverageValid(ram.Coverage, r.Window, r.Scope.MaxGap, r.RAM.Samples, cadence)) {
			return fail()
		}
		seen[key] = true // One slot per source/run/attempt, even if job IDs differ.
		if key.Attempt != 1 {
			out.Retried++
			continue
		}
		out.Primary++
		switch {
		case sample.Risks != 0:
			out.AtRisk++
			countJobSizingRisks(&out, sample.Risks)
		case !sample.RiskKnown || sample.Outcome == TrendOutcomeUnknown:
			out.Unknown++
		case sample.Outcome != TrendOutcomeSuccess:
			out.Unsuccessful++
		default:
			out.Successful++
			if first.IsZero() || end.Before(first) {
				first = end
			}
			if end.After(last) {
				last = end
			}
			if cpu != nil && sizingCoverageSufficient(cpu.Coverage.Duration, r.Window) {
				out.CPU.QualifyingSamples++
				highestCPU = max(highestCPU, cpu.PeakIntervalMilliCPU)
			}
			if ram != nil && sizingCoverageSufficient(ram.Coverage.Duration, r.Window) {
				out.RAM.QualifyingSamples++
				highestRAM = max(highestRAM, r.RAM.SampleMax)
			}
		}
	}
	if out.AtRisk != 0 || out.Unknown != 0 || out.Unsuccessful != 0 {
		out.CPU.State, out.RAM.State = JobSizingNeedsRiskOrOutcomeReview, JobSizingNeedsRiskOrOutcomeReview
		return out, nil
	}
	if in.Coverage != TrendCoverageComplete {
		out.CPU.State, out.RAM.State = JobSizingCohortIncomplete, JobSizingCohortIncomplete
		return out, nil
	}
	if out.Successful < MinJobSizingPrimaryAttempts || last.Sub(first) < MinJobSizingHistory {
		out.CPU.State, out.RAM.State = JobSizingInsufficientHistory, JobSizingInsufficientHistory
		return out, nil
	}
	finalizeJobSizingMetric(&out.CPU, out.Successful, highestCPU, in.Shape.CPUMilli)
	finalizeJobSizingMetric(&out.RAM, out.Successful, highestRAM, in.Shape.RAMBytes)
	return out, nil
}

// jobSizingSamplingCeiling checks that the diagnostic is possible for the
// bounded sample sequence, then computes the most any metric could cover.
// It cannot establish that a metric was measured or authenticate a summary.
func jobSizingSamplingCeiling(r JobResourceSummary) (ResourceCoverage, bool) {
	s := r.Sampling
	if r.Samples == 0 {
		return ResourceCoverage{}, s == (ResourceSamplingCoverage{NoSamples: r.Window})
	}
	if s.NoSamples != 0 || s.BeforeFirstSample < 0 || s.BeforeFirstSample > r.Window ||
		s.AfterLastSample < 0 || s.AfterLastSample > r.Window-s.BeforeFirstSample ||
		s.CadenceGaps < 0 || s.CadenceGaps >= r.Samples {
		return ResourceCoverage{}, false
	}
	sampleSpan := r.Window - s.BeforeFirstSample - s.AfterLastSample
	if s.CadenceGapDuration < 0 || s.CadenceGapDuration > sampleSpan ||
		s.CadenceGaps == 0 && s.CadenceGapDuration != 0 ||
		s.CadenceGapDuration < time.Duration(s.CadenceGaps)*(r.Scope.MaxGap+time.Nanosecond) {
		return ResourceCoverage{}, false
	}
	pairs := r.Samples - 1 - s.CadenceGaps
	duration := sampleSpan - s.CadenceGapDuration
	// Strictly ordered sample times require at least 1 ns per pair; pairs
	// within cadence are each at most MaxGap. Inputs have already been bounded.
	if duration < time.Duration(pairs)*time.Nanosecond || duration > time.Duration(pairs)*r.Scope.MaxGap {
		return ResourceCoverage{}, false
	}
	return ResourceCoverage{Intervals: pairs, Duration: duration}, true
}

func sizingCoverageValid(c ResourceCoverage, window, maxGap time.Duration, samples int, ceiling ResourceCoverage) bool {
	// The caller bounded samples and maxGap, so this product cannot overflow.
	if c.Intervals <= 0 || c.Intervals >= samples || c.Intervals > ceiling.Intervals ||
		c.Duration < time.Duration(c.Intervals)*time.Nanosecond || c.Duration > ceiling.Duration ||
		c.Duration > time.Duration(c.Intervals)*maxGap || c.Complete != (c.Duration == window) {
		return false
	}
	// Pairs missing this metric still account for a positive share of the
	// aggregate cadence span. Do not let credited pairs consume their time.
	uncounted := time.Duration(ceiling.Intervals - c.Intervals)
	remaining := ceiling.Duration - c.Duration
	return remaining >= uncounted*time.Nanosecond && remaining <= uncounted*maxGap
}

func sizingCoverageSufficient(covered, window time.Duration) bool {
	// Both durations are at most a day. Compare exactly without rounding or
	// interpreting ten percent missing duration as measured zero.
	return 10*covered >= 9*window
}

func finalizeJobSizingMetric(m *JobSizingMetric, required int, peak, current uint64) {
	if m.QualifyingSamples != required {
		m.State = JobSizingCoverageUnavailable
		return
	}
	margin := quarterCeil(peak)
	if margin > math.MaxUint64-peak {
		m.State = JobSizingNeedsManualReview
		return
	}
	target := peak + margin
	if target <= current {
		m.State = JobSizingNoIncrease
		return
	}
	if target-current > current/4 {
		m.State = JobSizingNeedsManualReview // Never clip and claim the bound was sufficient.
		return
	}
	m.State, m.Suggested = JobSizingIncreaseAdvised, &target
}

func quarterCeil(v uint64) uint64 {
	if v%4 != 0 {
		return v/4 + 1
	}
	return v / 4
}

func countJobSizingRisks(s *JobSizingSummary, risks JobSizingRisk) {
	if risks&JobSizingThrottled != 0 {
		s.Throttled++
	}
	if risks&JobSizingOOM != 0 {
		s.OOM++
	}
	if risks&JobSizingDiskFull != 0 {
		s.DiskFull++
	}
	if risks&JobSizingExternalBuilder != 0 {
		s.ExternalBuilder++
	}
}
