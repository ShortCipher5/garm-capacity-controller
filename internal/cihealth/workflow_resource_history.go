package cihealth

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"time"
)

// WorkflowResourceKey is independently bound to each retained job by the producer.
// DefinitionDigest identifies the relevant workflow definition, not a changing
// source commit, and JobDefinition is the YAML job key, not its display name.
type WorkflowResourceKey struct {
	Scope                           TrendScope
	DefinitionDigest, JobDefinition string
	Runner                          SizingCohortIdentity
}

type WorkflowResourceRow struct {
	Key        WorkflowResourceKey
	Usage      VerifiedJobUsageRecord
	Shape      JobSizingShape
	Completion JobSizingCompletion
	Outcome    TrendOutcome
	RiskKnown  bool
	Risks      JobSizingRisk
}

// Rows must enumerate the selected cohort, including retries and missing or failed
// observations. Complete coverage is a producer assertion, never derived from len.
type WorkflowResourceInput struct {
	Key          WorkflowResourceKey
	Shape        JobSizingShape
	Window       JobSizingReviewWindow
	Coverage     TrendCoverage
	Reviewed     bool
	Capabilities AttributionCapabilityMatrix
	Rows         []WorkflowResourceRow
}

type ResourceComparison struct {
	Current   string `json:"current"`
	Median    string `json:"baseline_median"`
	Direction string `json:"direction"`
}

type WorkflowResourceHistory struct {
	DefinitionDigest string `json:"definition_sha256"`
	JobDefinition    string `json:"job_definition"`
	Primary          int    `json:"primary"`
	Retries          int    `json:"retries"`
	Baseline         int    `json:"baseline"`
	// Fields are omitted, never replaced by zero, when a metric lacks proof.
	CurrentRun string              `json:"current_run,omitempty"`
	CurrentJob string              `json:"current_job,omitempty"`
	CPUAverage *ResourceComparison `json:"cpu_average,omitempty"`
	CPUPeak    *ResourceComparison `json:"cpu_peak,omitempty"`
	RAMAverage *ResourceComparison `json:"ram_average,omitempty"`
	RAMPeak    *ResourceComparison `json:"ram_peak,omitempty"`
	DiskPeak   *ResourceComparison `json:"disk_peak,omitempty"`
}

// ReviewWorkflowResourceHistory shares the exact attribution/eligibility and
// upward-only sizing rules with the existing advisory. It compares the latest
// successful primary execution with prior independent executions; never sums
// per-job peaks into a "workflow peak" or feeds these hints to admission.
func ReviewWorkflowResourceHistory(in WorkflowResourceInput) (JobSizingAdvisory, WorkflowResourceHistory, error) {
	fail := func() (JobSizingAdvisory, WorkflowResourceHistory, error) {
		return JobSizingAdvisory{}, WorkflowResourceHistory{}, ErrJobSizingFacts
	}
	key := in.Key
	digest, err := hex.DecodeString(key.DefinitionDigest)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != key.DefinitionDigest || !validSizingHostLabel(key.JobDefinition) ||
		key.Runner.Profile == "" || key.Runner.Revision == "" || len(in.Rows) > MaxTrendAttempts {
		return fail()
	}
	h := WorkflowResourceHistory{DefinitionDigest: key.DefinitionDigest, JobDefinition: key.JobDefinition}
	// Freshness policy is an upper bound even for a permissive caller.
	if in.Window.LatestCompletionMaxAge > 24*time.Hour {
		in.Window.LatestCompletionMaxAge = 24 * time.Hour
	}
	coverage := in.Coverage
	if coverage > TrendCoverageComplete {
		return fail()
	}
	candidates := make([]SizingCandidate, 0, len(in.Rows))
	seenJob := map[int64]bool{}
	seenAttempt := map[AttemptKey]bool{}
	for _, row := range in.Rows {
		r := row.Usage
		job := r.Job
		if row.Key != key || row.Shape != in.Shape || !validKey(job.Attempt) || job.JobID <= 0 ||
			job.Attempt.Source != key.Scope.Source || job.Attempt.RepositoryID != key.Scope.RepositoryID || job.Attempt.WorkflowID != key.Scope.WorkflowID ||
			seenJob[job.JobID] || seenAttempt[job.Attempt] {
			return fail()
		}
		seenJob[job.JobID], seenAttempt[job.Attempt] = true, true
		if job.Attempt.Attempt == 1 {
			h.Primary++
		} else {
			h.Retries++
		}
		if !in.Capabilities.supportsExactJobAttribution() || !in.Capabilities.Supports(FactExclusiveJobWindow) || (r.State != VerifiedUsageVerified && r.State != VerifiedUsagePartial) || r.Measured == nil {
			coverage = TrendCoveragePartial
			continue
		}
		m := *r.Measured // strip unsupported dimensions without mutating producer memory
		if r.Reason != UsageOmissionNone || m.Scope.Job != job || !validTime(r.AsOf) || !r.AsOf.Equal(m.Scope.AsOf) || !row.Completion.ObservedAt.IsZero() && row.Completion.Job != job {
			return fail()
		}
		restrictUsageMetrics(&m, in.Capabilities)
		if r.diskUnsupported || !in.Capabilities.Supports(FactDiskUsedBytes) {
			m.DiskUsed = nil
		}
		candidates = append(candidates, SizingCandidate{Identity: key.Runner, Sample: JobSizingSample{Resources: m, Completion: row.Completion, Shape: in.Shape, Outcome: row.Outcome, RiskKnown: row.RiskKnown, Risks: row.Risks}})
	}
	p, err := ProjectJobSizingCohort(key.Scope, key.Runner, in.Shape, coverage, in.Reviewed, in.Window, candidates)
	if err != nil || len(p.Rejected) > 0 || p.Unverifiable > 0 || p.Foreign > 0 {
		return fail()
	}
	ad, err := ReviewJobSizingAdvisory(p, in.Window, JobSizingHostContext{})
	if err != nil {
		return fail()
	}
	if !in.Reviewed || coverage != TrendCoverageComplete || !ad.Freshness.Fresh || ad.Freshness.Age > 24*time.Hour || ad.Samples.Successful != h.Primary || h.Primary < MinJobSizingPrimaryAttempts+1 {
		return ad, h, nil
	}
	primary := make([]JobSizingSample, 0, h.Primary)
	for _, s := range p.Cohort.Samples {
		if s.Resources.Scope.Job.Attempt.Attempt == 1 {
			primary = append(primary, s)
		}
	}
	slices.SortFunc(primary, func(a, b JobSizingSample) int { return a.Completion.EndedAt.Compare(b.Completion.EndedAt) })
	last := primary[len(primary)-1]
	prev := primary[:len(primary)-1]
	// An ambiguous latest primary, or a baseline not wholly prior to the
	// newest job, cannot support a statement about a change from the past.
	if prev[len(prev)-1].Completion.EndedAt.After(last.Completion.StartedAt) || prev[len(prev)-1].Completion.EndedAt.Sub(prev[0].Completion.EndedAt) < MinJobSizingHistory {
		return ad, h, nil
	}
	h.Baseline = len(prev)
	h.CurrentRun = strconv.FormatInt(last.Resources.Scope.Job.Attempt.RunID, 10)
	h.CurrentJob = strconv.FormatInt(last.Resources.Scope.Job.JobID, 10)
	measure := func(get func(JobResourceSummary) (uint64, bool), floor uint64) *ResourceComparison {
		values := make([]uint64, 0, len(primary))
		for _, s := range primary {
			if !s.Resources.Scope.EndAt.Equal(s.Completion.EndedAt) {
				return nil
			}
			v, ok := get(s.Resources)
			if !ok {
				return nil
			}
			values = append(values, v)
		}
		return compareResourceValues(values[:len(values)-1], values[len(values)-1], floor)
	}
	cpu := func(m JobResourceSummary) bool {
		return m.CPU != nil && sizingCoverageSufficient(m.CPU.Coverage.Duration, m.Window)
	}
	ram := func(m JobResourceSummary) bool {
		return m.RAMMean != nil && m.RAM != nil && sizingCoverageSufficient(m.RAMMean.Coverage.Duration, m.Window)
	}
	h.CPUAverage = measure(func(m JobResourceSummary) (uint64, bool) {
		if !cpu(m) {
			return 0, false
		}
		return m.CPU.AverageMilliCPU, true
	}, 50)
	h.CPUPeak = measure(func(m JobResourceSummary) (uint64, bool) {
		if !cpu(m) {
			return 0, false
		}
		return m.CPU.PeakIntervalMilliCPU, true
	}, 50)
	h.RAMAverage = measure(func(m JobResourceSummary) (uint64, bool) {
		if !ram(m) {
			return 0, false
		}
		return m.RAMMean.Bytes, true
	}, 64<<20)
	h.RAMPeak = measure(func(m JobResourceSummary) (uint64, bool) {
		if !ram(m) {
			return 0, false
		}
		return m.RAM.SampleMax, true
	}, 64<<20)
	h.DiskPeak = measure(func(m JobResourceSummary) (uint64, bool) {
		span, ok := jobSizingGaugeSpan(m, m.DiskUsed, m.Scope.StartedAt, m.Scope.EndAt)
		if !ok || m.DiskUsed.PeakAt.Before(m.Scope.StartedAt) || m.DiskUsed.PeakAt.After(m.DiskUsed.LatestAt) || m.DiskUsed.Samples != m.Samples || !sizingCoverageSufficient(span, m.Window) || m.Sampling.CadenceGaps != 0 {
			return 0, false
		}
		return m.DiskUsed.SampleMax, true
	}, 64<<20)
	return ad, h, nil
}

// A dashboard signal needs a material absolute change, at least a 25%
// change versus the median and a value outside the empirical 10–90% band.
// Integer distance comparisons avoid overflow, floating-point precision loss
// and division by zero. A resource decrease is never downsizing guidance.
func compareResourceValues(previous []uint64, current, floor uint64) *ResourceComparison {
	slices.Sort(previous)
	n := len(previous)
	lo, hi := previous[(n-1)/2], previous[n/2]
	median := lo + (hi-lo)/2
	p10, p90 := previous[(n+9)/10-1], previous[(9*n+9)/10-1]
	minChange := max(floor, (median/4)+(median%4+3)/4)
	direction := "within_baseline"
	if current > p90 && current > median && current-median >= minChange {
		direction = "increase"
	}
	if current < p10 && current < median && median-current >= minChange {
		direction = "decrease"
	}
	return &ResourceComparison{Current: strconv.FormatUint(current, 10), Median: strconv.FormatUint(median, 10), Direction: direction}
}
