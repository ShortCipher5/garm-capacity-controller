package cihealth

import (
	"errors"
	"math"
	"sort"
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

// JobSizingCompletion is separately observed terminal evidence for the exact job,
// not inferred from metric coverage or a resource-window cutoff. The caller
// must independently verify this observation; the reducer checks consistency.
type JobSizingCompletion struct {
	Job                            JobKey
	StartedAt, EndedAt, ObservedAt time.Time
}

// JobSizingSample carries already verified, attributable telemetry from the
// existing reducer plus the independently observed exact job outcome, risks
// and installed limits. Missing risk evidence is not an absence of risk.
type JobSizingSample struct {
	Resources  JobResourceSummary
	Completion JobSizingCompletion // zero means no confirmed completion
	Shape      JobSizingShape
	Outcome    TrendOutcome
	RiskKnown  bool
	Risks      JobSizingRisk
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
	windows := make([]jobSizingRunnerWindow, 0, len(in.Samples))
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
		completion, ok := sizingCompletionEnd(r.Scope, sample.Completion)
		if !ok {
			return fail()
		}
		cadence, ok := jobSizingSamplingCeiling(r)
		if !ok {
			return fail()
		}
		cpu, ram := r.CPU, r.RAMMean
		if cpu != nil && (cpu.AverageMilliCPU > cpu.PeakIntervalMilliCPU || !sizingCoverageValid(cpu.Coverage, r.Window, r.Scope.MaxGap, r.Samples, cadence)) ||
			!jobSizingRAMValid(r, start, end, cadence) {
			return fail()
		}
		seen[key] = true // One slot per source/run/attempt, even if job IDs differ.
		// A positively confirmed job is present through its true end, even
		// if its own resource samples stop early. It can overlap another job.
		windowEnd := end
		if completion.After(end) {
			windowEnd = completion
		}
		windows = append(windows, jobSizingRunnerWindow{r.Scope.RunnerIncarnation, start, windowEnd, !completion.IsZero()})
		if key.Attempt != 1 {
			out.Retried++
			continue
		}
		out.Primary++
		switch {
		case sample.Risks != 0:
			out.AtRisk++
			countJobSizingRisks(&out, sample.Risks)
		case completion.IsZero() || !sample.RiskKnown || sample.Outcome == TrendOutcomeUnknown:
			out.Unknown++
		case sample.Outcome != TrendOutcomeSuccess:
			out.Unsuccessful++
		default:
			out.Successful++
			if first.IsZero() || completion.Before(first) {
				first = completion
			}
			if completion.After(last) {
				last = completion
			}
			// Scope.EndAt may be a report cutoff rather than the actual job
			// end. Preserve the summary but never use a prefix for sizing.
			if !end.Equal(completion) {
				continue
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
	if ambiguousJobSizingWindows(windows) {
		return fail()
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

// sizingCompletionEnd distinguishes missing evidence from a contradictory
// supplied observation. A later terminal observation can report a later end
// than Scope.EndAt, but resources cannot extend past the observed job end.
func sizingCompletionEnd(s JobResourceScope, c JobSizingCompletion) (time.Time, bool) {
	if c.Job == (JobKey{}) && c.StartedAt.IsZero() && c.EndedAt.IsZero() && c.ObservedAt.IsZero() {
		return time.Time{}, true
	}
	start, end, observed := c.StartedAt.UTC(), c.EndedAt.UTC(), c.ObservedAt.UTC()
	if c.Job != s.Job || !validTime(start) || !start.Equal(s.StartedAt) ||
		!validTime(end) || !end.After(start) || end.Before(s.EndAt) ||
		!validTime(observed) || observed.Before(end) {
		return time.Time{}, false
	}
	return end, true
}

type jobSizingRunnerWindow struct {
	incarnation string
	start, end  time.Time
	endKnown    bool
}

// A runner-wide resource observation cannot independently describe two jobs
// executing on the same incarnation at the same time. Reject ambiguity even
// for retries: their samples are not counted, but their execution may affect
// a primary job's counters. Touching boundaries share no elapsed time
// only when the earlier end is confirmed; a sample cutoff alone cannot
// exclude overlap with a later job on the same incarnation.
func ambiguousJobSizingWindows(windows []jobSizingRunnerWindow) bool {
	sort.Slice(windows, func(i, j int) bool {
		a, b := windows[i], windows[j]
		if a.incarnation != b.incarnation {
			return a.incarnation < b.incarnation
		}
		return a.start.Before(b.start)
	})
	for i := 1; i < len(windows); i++ {
		a, b := windows[i-1], windows[i]
		if a.incarnation == b.incarnation && (!a.endKnown || b.start.Before(a.end)) {
			return true
		}
	}
	return false
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

// A RAM interval requires a gauge at both endpoints, so its summed
// non-overlapping duration cannot extend beyond the latest RAM reading.
// Individual gauges also occupy distinct timestamps in the overall series.
func jobSizingRAMValid(r JobResourceSummary, start, end time.Time, cadence ResourceCoverage) bool {
	gauge, mean := r.RAM, r.RAMMean
	if gauge == nil {
		return mean == nil
	}
	earliest := start.Add(r.Sampling.BeforeFirstSample)
	latest := end.Add(-r.Sampling.AfterLastSample)
	span := gauge.LatestAt.Sub(earliest)
	if gauge.Samples < 1 || gauge.Samples > r.Samples || gauge.Latest > gauge.SampleMax ||
		gauge.Samples == 1 && gauge.Latest != gauge.SampleMax ||
		gauge.LatestAt.After(latest) || span < time.Duration(gauge.Samples-1)*time.Nanosecond {
		return false
	}
	return mean == nil || mean.Bytes <= gauge.SampleMax && mean.Coverage.Duration <= span &&
		sizingCoverageValid(mean.Coverage, r.Window, r.Scope.MaxGap, gauge.Samples, cadence)
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
