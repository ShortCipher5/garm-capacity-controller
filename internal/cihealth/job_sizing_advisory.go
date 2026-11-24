package cihealth

import "time"

// JobSizingAdvisoryVersion identifies the deterministic advisory derivation
// applied on top of the reviewed sizing window. A recommendation is only
// reproducible under the advisory and cohort policy versions that produced
// it together with the exact source facts carried here.
const JobSizingAdvisoryVersion = "job-sizing-advisory/v1"

// JobSizingMinChangeDivisor suppresses oscillation: an advised increase must
// exceed the installed shape by more than installed/Divisor before it is
// displayed, so measurement noise cannot flip a profile back and forth.
const JobSizingMinChangeDivisor = 16

// JobSizingUncertaintySampledPeakBound marks recommendations derived from
// observed peaks: sampling did not cover every interval, so a sampled peak
// is not a complete upper bound for unmeasured load.
const JobSizingUncertaintySampledPeakBound = "sampled_peak_bound"

// Advisory outcomes. Blocked and neutral outcomes carry a reason; a
// recommendation never appears without its uncertainty.
const (
	JobSizingAdvisoryUnreviewed               = "unreviewed"
	JobSizingAdvisoryIncreaseAdvised          = "increase_advised"
	JobSizingAdvisoryUnchanged                = "unchanged"
	JobSizingAdvisoryNeedsManualReview        = "needs_manual_review"
	JobSizingAdvisoryNeedsRiskOrOutcomeReview = "needs_risk_or_outcome_review"
	JobSizingAdvisoryCohortIncomplete         = "cohort_incomplete"
	JobSizingAdvisoryCoverageUnavailable      = "coverage_unavailable"
	JobSizingAdvisoryInsufficientHistory      = "insufficient_history"
	JobSizingAdvisoryStaleHistory             = "stale_history"
)

// Neutral no-recommendation reasons.
const (
	JobSizingNoReasonNotReviewed           = "not_reviewed"
	JobSizingNoReasonIncompleteCoverage    = "incomplete_cohort_coverage"
	JobSizingNoReasonRiskEvidence          = "risk_evidence_present"
	JobSizingNoReasonUnsuccessful          = "unsuccessful_jobs_present"
	JobSizingNoReasonOutcomeUnknown        = "outcome_or_risk_unknown"
	JobSizingNoReasonTooFewAttempts        = "insufficient_primary_attempts"
	JobSizingNoReasonHistorySpan           = "insufficient_history_span"
	JobSizingNoReasonMissingCoverage       = "missing_metric_coverage"
	JobSizingNoReasonStale                 = "no_fresh_primary_completion"
	JobSizingNoReasonExceedsBound          = "change_exceeds_reviewed_bound"
	JobSizingNoReasonOscillationSuppressed = "within_oscillation_suppression_band"
)

// Advisory warnings. A warning never replaces a hard fit failure; the host
// fit is reported separately and takes precedence in the view.
const (
	JobSizingWarningSustainedCPUPressure      = "sustained_cpu_pressure"
	JobSizingWarningSustainedRAMPressure      = "sustained_ram_pressure"
	JobSizingWarningInsufficientSpareCapacity = "insufficient_spare_capacity"
)

const (
	JobSizingHostFitUnknown    = "unknown"
	JobSizingHostFitFits       = "fits"
	JobSizingHostFitDoesNotFit = "does_not_fit"
)

const (
	JobSizingDiskUnavailable = "unavailable"
	JobSizingDiskObserved    = "observed"
)

// JobSizingDimensionAdvice is the display decision for one dimension.
// State is the deterministic advisory outcome; Suggested is non-nil only
// for an advised increase within the reviewed bounds. Reason is empty for
// an advice or a plain unchanged result and names the neutral blocker
// otherwise. Reductions are never advised.
type JobSizingDimensionAdvice struct {
	State     string
	Suggested *uint64
	Reason    string
}

type JobSizingAdvisorySamples struct {
	Eligible                                  int // samples that passed cohort eligibility
	Primary, Retried, Successful              int
	AtRisk, Unknown, Unsuccessful             int
	Throttled, OOM, DiskFull, ExternalBuilder int
	QualifyingCPU, QualifyingRAM              int
}

// JobSizingAdvisoryObserved distinguishes genuine measured usage from
// missing samples: a known false peak means no qualifying measurement, not
// zero usage.
type JobSizingAdvisoryObserved struct {
	CPUKnown, RAMKnown         bool
	CPUPeakMilli, RAMPeakBytes uint64
	CPUSaturated, RAMSaturated int
}

type JobSizingAdvisoryFreshness struct {
	LatestPrimaryCompletion time.Time // zero when no confirmed primary completion exists
	Age                     time.Duration
	Fresh                   bool
}

// JobSizingHostContext is caller-supplied display context for one host. It
// never authorizes placement, never proves spare capacity and is not
// evidence that the current runner profile is placed there.
type JobSizingHostContext struct {
	Known                                bool
	Host                                 string
	CPUBudgetMilli, RAMBudgetBytes       uint64
	CPUAllocatedMilli, RAMAllocatedBytes uint64
}

type JobSizingAdvisoryHost struct {
	Known                                bool
	Host                                 string
	CPUBudgetMilli, RAMBudgetBytes       uint64
	CPUAllocatedMilli, RAMAllocatedBytes uint64
	CPUHeadroomMilli, RAMHeadroomBytes   uint64
	Fit                                  string
}

// JobSizingDiskAdvice appears only where attributable data supports it:
// disk gauges bound to fully evidenced samples. Otherwise it stays
// unavailable and the view must not invent disk guidance.
type JobSizingDiskAdvice struct {
	State     string
	Peak      uint64
	Samples   int // qualifying samples with an attributable disk gauge
	Qualified int // qualifying samples
}

// JobSizingAdvisory is a display-only, deterministic projection of one
// reviewed cohort. It carries its exact source facts (scope, identity,
// window, coverage, counts, observed peaks, installed shape, freshness and
// the eligibility rejections) so any recommendation can be reproduced from
// them under the recorded versions. It has no apply action and never
// changes scaling settings.
//
// Smaller-profile (downgrade) advice is deliberately deferred: this advisory
// is upward-only and never recommends reducing CPU or memory, even with
// complete clean evidence, because a sampled peak is not a complete upper
// bound and no verified current-workload source is wired. Reductions require
// a future policy revision on top of the verified history producers
// (Plans 04/05) with their own floors and suppression; until then the only
// conservative smaller-profile outcome is the neutral "unchanged" state.
type JobSizingAdvisory struct {
	Version, PolicyVersion string
	Outcome                string
	Scope                  TrendScope
	Identity               SizingCohortIdentity
	Coverage               TrendCoverage
	Window                 JobSizingReviewWindow
	Freshness              JobSizingAdvisoryFreshness
	Samples                JobSizingAdvisorySamples
	Observed               JobSizingAdvisoryObserved
	Installed              JobSizingShape
	CPU, RAM               JobSizingDimensionAdvice
	Uncertainty            string
	Host                   JobSizingAdvisoryHost
	Warnings               []string
	Disk                   JobSizingDiskAdvice
	Rejected               []SizingRejection
	Incomparable           int
	Unverifiable           int
	Foreign                int
}

// ReviewJobSizingAdvisory runs the reviewed window over the projected
// cohort and derives the deterministic advisory view. Invalid facts,
// inconsistent host context or an unknown policy version fail closed.
func ReviewJobSizingAdvisory(proj SizingCohortProjection, window JobSizingReviewWindow, host JobSizingHostContext) (JobSizingAdvisory, error) {
	fail := func() (JobSizingAdvisory, error) { return JobSizingAdvisory{}, ErrJobSizingFacts }
	if proj.Version != JobSizingCohortPolicyVersion || proj.Cohort.Shape.CPUMilli == 0 || proj.Cohort.Shape.RAMBytes == 0 {
		return fail()
	}
	if host.Known && (!validSizingHostLabel(host.Host) || host.CPUAllocatedMilli > host.CPUBudgetMilli || host.RAMAllocatedBytes > host.RAMBudgetBytes) {
		return fail()
	}
	review, err := ReviewJobSizingWindow(proj.Cohort, window)
	if err != nil {
		return JobSizingAdvisory{}, err
	}
	out := JobSizingAdvisory{
		Version:       JobSizingAdvisoryVersion,
		PolicyVersion: proj.Version,
		Scope:         proj.Scope,
		Identity:      proj.Identity,
		Coverage:      proj.Coverage,
		Window:        review.Window,
		Installed:     review.Installed,
		Rejected:      proj.Rejected,
		Incomparable:  proj.Incomparable,
		Unverifiable:  proj.Unverifiable,
		Foreign:       proj.Foreign,
		Warnings:      []string{},
	}
	summary := review.Summary
	out.Samples = JobSizingAdvisorySamples{
		Eligible: len(proj.Cohort.Samples), Primary: summary.Primary, Retried: summary.Retried, Successful: summary.Successful,
		AtRisk: summary.AtRisk, Unknown: summary.Unknown, Unsuccessful: summary.Unsuccessful,
		Throttled: summary.Throttled, OOM: summary.OOM, DiskFull: summary.DiskFull, ExternalBuilder: summary.ExternalBuilder,
		QualifyingCPU: summary.CPU.QualifyingSamples, QualifyingRAM: summary.RAM.QualifyingSamples,
	}
	out.Observed = JobSizingAdvisoryObserved{
		CPUKnown: summary.CPU.QualifyingSamples > 0, RAMKnown: summary.RAM.QualifyingSamples > 0,
		CPUPeakMilli: summary.CPUPeakMilliCPU, RAMPeakBytes: summary.RAMPeakBytes,
		CPUSaturated: summary.CPUSaturated, RAMSaturated: summary.RAMSaturated,
	}
	if out.Observed.CPUKnown || out.Observed.RAMKnown {
		out.Uncertainty = JobSizingUncertaintySampledPeakBound
	}
	if !review.LatestPrimaryCompletion.IsZero() {
		out.Freshness = JobSizingAdvisoryFreshness{
			LatestPrimaryCompletion: review.LatestPrimaryCompletion,
			Age:                     review.Window.AsOf.Sub(review.LatestPrimaryCompletion),
			Fresh:                   review.FreshPrimaryCompletion,
		}
	}
	out.CPU = jobSizingDimensionAdvice(summary, true, proj.Cohort.Shape.CPUMilli)
	out.RAM = jobSizingDimensionAdvice(summary, false, proj.Cohort.Shape.RAMBytes)
	out.Outcome = jobSizingHeadlineOutcome(out.CPU.State, out.RAM.State)
	out.Host = jobSizingHostView(host, proj.Cohort.Shape, out.CPU.Suggested, out.RAM.Suggested)
	out.Warnings = jobSizingWarnings(summary, proj.Cohort.Shape, out.CPU, out.RAM, out.Host)
	out.Disk = jobSizingDiskAdvice(proj.Cohort.Samples)
	return out, nil
}

// jobSizingDiskAdvice reports observed disk usage only where attributable
// data supports it: a disk gauge bound to a fully evidenced qualifying
// sample. Any other sample leaves disk advice unavailable rather than
// inventing guidance from timing or unattributed counters.
func jobSizingDiskAdvice(samples []JobSizingSample) JobSizingDiskAdvice {
	out := JobSizingDiskAdvice{State: JobSizingDiskUnavailable}
	for _, sample := range samples {
		completion, ok := sizingCompletionEnd(sample.Resources.Scope, sample.Completion)
		if !ok || !sizingCleanSample(sample, completion) {
			continue
		}
		out.Qualified++
		r := sample.Resources
		if _, valid := jobSizingGaugeSpan(r, r.DiskUsed, r.Scope.StartedAt.UTC(), r.Scope.EndAt.UTC()); valid {
			out.Samples++
			out.Peak = max(out.Peak, r.DiskUsed.SampleMax)
		}
	}
	if out.Samples > 0 {
		out.State = JobSizingDiskObserved
	}
	return out
}

func jobSizingDimensionAdvice(s JobSizingSummary, cpu bool, installed uint64) JobSizingDimensionAdvice {
	var state JobSizingMetricState
	var suggested *uint64
	if cpu {
		state, suggested = s.CPU.State, s.CPU.Suggested
	} else {
		state, suggested = s.RAM.State, s.RAM.Suggested
	}
	advice := JobSizingDimensionAdvice{State: JobSizingAdvisoryUnreviewed, Reason: JobSizingNoReasonNotReviewed}
	switch state {
	case JobSizingCohortIncomplete:
		advice.State, advice.Reason = JobSizingAdvisoryCohortIncomplete, JobSizingNoReasonIncompleteCoverage
	case JobSizingInsufficientHistory:
		advice.State = JobSizingAdvisoryInsufficientHistory
		if s.Successful < MinJobSizingPrimaryAttempts {
			advice.Reason = JobSizingNoReasonTooFewAttempts
		} else {
			advice.Reason = JobSizingNoReasonHistorySpan
		}
	case JobSizingNeedsRiskOrOutcomeReview:
		advice.State = JobSizingAdvisoryNeedsRiskOrOutcomeReview
		switch {
		case s.AtRisk != 0:
			advice.Reason = JobSizingNoReasonRiskEvidence
		case s.Unsuccessful != 0:
			advice.Reason = JobSizingNoReasonUnsuccessful
		default:
			advice.Reason = JobSizingNoReasonOutcomeUnknown
		}
	case JobSizingCoverageUnavailable:
		advice.State, advice.Reason = JobSizingAdvisoryCoverageUnavailable, JobSizingNoReasonMissingCoverage
	case JobSizingStaleHistory:
		advice.State, advice.Reason = JobSizingAdvisoryStaleHistory, JobSizingNoReasonStale
	case JobSizingNeedsManualReview:
		advice.State, advice.Reason = JobSizingAdvisoryNeedsManualReview, JobSizingNoReasonExceedsBound
	case JobSizingNoIncrease:
		advice.State, advice.Reason = JobSizingAdvisoryUnchanged, ""
	case JobSizingIncreaseAdvised:
		if suggested != nil && *suggested > installed && *suggested-installed <= installed/JobSizingMinChangeDivisor {
			advice.State, advice.Reason = JobSizingAdvisoryUnchanged, JobSizingNoReasonOscillationSuppressed
			break
		}
		advice.State, advice.Suggested, advice.Reason = JobSizingAdvisoryIncreaseAdvised, suggested, ""
	}
	return advice
}

// jobSizingHeadlineOutcome picks the decisive display outcome
// deterministically from the dimension outcomes.
func jobSizingHeadlineOutcome(cpu, ram string) string {
	for _, outcome := range []string{
		JobSizingAdvisoryUnreviewed,
		JobSizingAdvisoryIncreaseAdvised,
		JobSizingAdvisoryNeedsManualReview,
		JobSizingAdvisoryStaleHistory,
		JobSizingAdvisoryNeedsRiskOrOutcomeReview,
		JobSizingAdvisoryCohortIncomplete,
		JobSizingAdvisoryCoverageUnavailable,
		JobSizingAdvisoryInsufficientHistory,
		JobSizingAdvisoryUnchanged,
	} {
		if cpu == outcome || ram == outcome {
			return outcome
		}
	}
	return JobSizingAdvisoryUnchanged
}

func jobSizingHostView(host JobSizingHostContext, installed JobSizingShape, cpuSuggested, ramSuggested *uint64) JobSizingAdvisoryHost {
	out := JobSizingAdvisoryHost{Known: host.Known, Fit: JobSizingHostFitUnknown}
	if !host.Known {
		return out
	}
	out.Host, out.CPUBudgetMilli, out.RAMBudgetBytes = host.Host, host.CPUBudgetMilli, host.RAMBudgetBytes
	out.CPUAllocatedMilli, out.RAMAllocatedBytes = host.CPUAllocatedMilli, host.RAMAllocatedBytes
	out.CPUHeadroomMilli = host.CPUBudgetMilli - host.CPUAllocatedMilli
	out.RAMHeadroomBytes = host.RAMBudgetBytes - host.RAMAllocatedBytes
	out.Fit = JobSizingHostFitFits
	if installed.CPUMilli > host.CPUBudgetMilli || installed.RAMBytes > host.RAMBudgetBytes {
		out.Fit = JobSizingHostFitDoesNotFit
	}
	cpuTarget, ramTarget := installed.CPUMilli, installed.RAMBytes
	if cpuSuggested != nil {
		cpuTarget = *cpuSuggested
	}
	if ramSuggested != nil {
		ramTarget = *ramSuggested
	}
	if cpuTarget > installed.CPUMilli+out.CPUHeadroomMilli || ramTarget > installed.RAMBytes+out.RAMHeadroomBytes {
		out.Fit = JobSizingHostFitDoesNotFit
	}
	return out
}

// jobSizingWarnings derives pressure and headroom warnings from attributable
// facts only. They accompany, and never replace, a hard fit failure.
func jobSizingWarnings(s JobSizingSummary, installed JobSizingShape, cpu, ram JobSizingDimensionAdvice, host JobSizingAdvisoryHost) []string {
	warnings := []string{}
	if s.CPU.QualifyingSamples > 0 && s.CPUSaturated*2 >= s.CPU.QualifyingSamples {
		warnings = append(warnings, JobSizingWarningSustainedCPUPressure)
	}
	if s.RAM.QualifyingSamples > 0 && s.RAMSaturated*2 >= s.RAM.QualifyingSamples {
		warnings = append(warnings, JobSizingWarningSustainedRAMPressure)
	}
	if host.Known && (cpu.Suggested != nil || ram.Suggested != nil) {
		cpuDelta, ramDelta := uint64(0), uint64(0)
		if cpu.Suggested != nil {
			cpuDelta = *cpu.Suggested - installed.CPUMilli
		}
		if ram.Suggested != nil {
			ramDelta = *ram.Suggested - installed.RAMBytes
		}
		if cpuDelta > host.CPUHeadroomMilli/2 || ramDelta > host.RAMHeadroomBytes/2 {
			warnings = append(warnings, JobSizingWarningInsufficientSpareCapacity)
		}
	}
	return warnings
}
