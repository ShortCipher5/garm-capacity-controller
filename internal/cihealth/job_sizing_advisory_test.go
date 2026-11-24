package cihealth

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func advisoryCohortFixture(t *testing.T, primary int, shape JobSizingShape, cpuPeakMilli, ramPeakBytes uint64, disk bool, span, covered time.Duration) JobSizingCohort {
	t.Helper()
	in := JobSizingCohort{
		Scope:         TrendScope{Source: "fixture", RepositoryID: 1, WorkflowID: 2},
		Shape:         shape,
		Comparability: JobSizingReviewedComparable,
		Coverage:      TrendCoverageComplete,
	}
	for i := range primary {
		w := resourceScopeFixture(10*time.Second, 9*time.Second)
		w.Job.Attempt.RunID = int64(i + 1)
		w.Job.JobID = int64(i + 100)
		w.Job.Attempt.Attempt = 1
		w.RunnerIncarnation = fmt.Sprintf("advisory-incarnation-%d", i)
		offset := time.Duration(i) * span / time.Duration(max(1, primary-1))
		w.StartedAt, w.EndAt, w.AsOf = w.StartedAt.Add(offset), w.EndAt.Add(offset), w.AsOf.Add(offset)
		first, last := resourceSampleFixture(w, 0), resourceSampleFixture(w, covered)
		first.CPUTimeNanos = knownResource(0)
		last.CPUTimeNanos = knownResource(cpuPeakMilli * uint64(covered) / 1000)
		first.RAMBytes, last.RAMBytes = knownResource(ramPeakBytes), knownResource(ramPeakBytes)
		if disk {
			first.DiskUsedBytes = knownResource(uint64(1000 + i))
			last.DiskUsedBytes = knownResource(uint64(1100 + i))
		}
		resources, err := SummarizeJobResources(w, []JobResourceSample{first, last})
		if err != nil {
			t.Fatal(err)
		}
		in.Samples = append(in.Samples, JobSizingSample{Resources: resources, Completion: sizingCompletionFixture(w), Shape: shape, Outcome: TrendOutcomeSuccess, RiskKnown: true})
	}
	return in
}

func advisoryFixture(t *testing.T, host JobSizingHostContext, in JobSizingCohort, window JobSizingReviewWindow) JobSizingAdvisory {
	t.Helper()
	proj, err := ProjectJobSizingCohort(in.Scope, sizingTestIdentity, in.Shape, in.Coverage, true, window, sizingCandidatesFor(sizingTestIdentity, in))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	out, err := ReviewJobSizingAdvisory(proj, window, host)
	if err != nil {
		t.Fatalf("advisory: %v", err)
	}
	return out
}

func TestReviewJobSizingAdvisoryReproducibleQualifiedRecommendation(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	advisory := advisoryFixture(t, JobSizingHostContext{}, base, window)
	if advisory.Version != JobSizingAdvisoryVersion || advisory.PolicyVersion != JobSizingCohortPolicyVersion ||
		advisory.Outcome != JobSizingAdvisoryIncreaseAdvised || advisory.Coverage != TrendCoverageComplete ||
		advisory.Scope != base.Scope || advisory.Identity != sizingTestIdentity {
		t.Fatalf("advisory lost its reproducible identity: %+v", advisory)
	}
	if advisory.CPU.State != JobSizingAdvisoryIncreaseAdvised || advisory.CPU.Suggested == nil || *advisory.CPU.Suggested != 1125 ||
		advisory.RAM.State != JobSizingAdvisoryIncreaseAdvised || advisory.RAM.Suggested == nil || *advisory.RAM.Suggested != 120 {
		t.Fatalf("qualified recommendation changed: %+v", advisory)
	}
	if advisory.CPU.Reason != "" || advisory.RAM.Reason != "" || advisory.Uncertainty != JobSizingUncertaintySampledPeakBound {
		t.Fatalf("recommendation missing uncertainty or carried a blocker reason: %+v", advisory)
	}
	if advisory.Samples.Eligible != 20 || advisory.Samples.Primary != 20 || advisory.Samples.Successful != 20 ||
		advisory.Samples.QualifyingCPU != 20 || advisory.Samples.QualifyingRAM != 20 {
		t.Fatalf("sample coverage not carried: %+v", advisory.Samples)
	}
	if !advisory.Observed.CPUKnown || advisory.Observed.CPUPeakMilli != 900 || !advisory.Observed.RAMKnown ||
		advisory.Observed.RAMPeakBytes != 96 || advisory.Observed.CPUSaturated != 20 || advisory.Observed.RAMSaturated != 20 {
		t.Fatalf("observed usage not carried: %+v", advisory.Observed)
	}
	if advisory.Installed != base.Shape || !advisory.Freshness.Fresh ||
		!advisory.Freshness.LatestPrimaryCompletion.Equal(base.Samples[19].Completion.EndedAt) ||
		advisory.Freshness.Age != time.Hour {
		t.Fatalf("source age or installed profile not carried: %+v", advisory.Freshness)
	}
	if !reflect.DeepEqual(advisory.Warnings, []string{JobSizingWarningSustainedCPUPressure, JobSizingWarningSustainedRAMPressure}) {
		t.Fatalf("sustained pressure warnings missing: %+v", advisory.Warnings)
	}
	if advisory.Host.Known || advisory.Host.Fit != JobSizingHostFitUnknown || advisory.Disk.State != JobSizingDiskUnavailable || advisory.Disk.Qualified != 20 {
		t.Fatalf("neutral host/disk defaults changed: %+v %+v", advisory.Host, advisory.Disk)
	}
	// Identical source facts in any input order reproduce the identical
	// advisory, so the recommendation is reproducible from them.
	candidates := sizingCandidatesFor(sizingTestIdentity, base)
	shuffled := append([]SizingCandidate(nil), candidates...)
	rand.New(rand.NewSource(11)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	proj, err := ProjectJobSizingCohort(base.Scope, sizingTestIdentity, base.Shape, base.Coverage, true, window, shuffled)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	again, err := ReviewJobSizingAdvisory(proj, window, JobSizingHostContext{})
	if err != nil || !reflect.DeepEqual(advisory, again) {
		t.Fatalf("input order changed the advisory:\n%+v\n%+v\n%v", advisory, again, err)
	}
}

func TestReviewJobSizingAdvisoryHostFitAndSpareCapacityWarnings(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	host := func(allocated uint64) JobSizingHostContext {
		return JobSizingHostContext{Known: true, Host: "host-a", CPUBudgetMilli: 64000, RAMBudgetBytes: 64 << 30, CPUAllocatedMilli: allocated, RAMAllocatedBytes: 4 << 30}
	}
	comfortable := advisoryFixture(t, host(4000), base, window)
	if comfortable.Host.Fit != JobSizingHostFitFits || comfortable.Host.CPUHeadroomMilli != 60000 ||
		reflect.DeepEqual(comfortable.Warnings, []string{JobSizingWarningInsufficientSpareCapacity}) {
		t.Fatalf("comfortable host misfit or hidden pressure warnings: %+v", comfortable)
	}
	if !reflect.DeepEqual(comfortable.Warnings, []string{JobSizingWarningSustainedCPUPressure, JobSizingWarningSustainedRAMPressure}) {
		t.Fatalf("unexpected warnings: %+v", comfortable.Warnings)
	}
	tight := advisoryFixture(t, host(63800), base, window)
	if tight.Host.Fit != JobSizingHostFitFits || !containsString(tight.Warnings, JobSizingWarningInsufficientSpareCapacity) {
		t.Fatalf("spare capacity warning not raised on a fitting host: %+v", tight)
	}
	over := advisoryFixture(t, host(63900), base, window)
	// A hard fit failure is reported as a failure; the warning accompanies it
	// but can never replace or hide it.
	if over.Host.Fit != JobSizingHostFitDoesNotFit || !containsString(over.Warnings, JobSizingWarningInsufficientSpareCapacity) {
		t.Fatalf("hard fit failure hidden behind warnings: %+v", over.Host)
	}
	oversized := advisoryFixture(t, JobSizingHostContext{Known: true, Host: "host-a", CPUBudgetMilli: 500, RAMBudgetBytes: 64 << 30}, base, window)
	if oversized.Host.Fit != JobSizingHostFitDoesNotFit {
		t.Fatalf("installed profile exceeding the budget was not a hard failure: %+v", oversized.Host)
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestReviewJobSizingAdvisoryNeverAdvisesReductionsAndSuppressesOscillation(t *testing.T) {
	low := advisoryCohortFixture(t, 20, JobSizingShape{CPUMilli: 1000, RAMBytes: 100000}, 100, 10, false, MinJobSizingHistory, 9*time.Second)
	got := advisoryFixture(t, JobSizingHostContext{}, low, sizingReviewWindowFixture(low))
	if got.Outcome != JobSizingAdvisoryUnchanged || got.CPU.Suggested != nil || got.RAM.Suggested != nil ||
		got.CPU.Reason != "" || got.RAM.Reason != "" {
		t.Fatalf("genuine low usage produced a change advice: %+v", got)
	}
	if !got.Observed.CPUKnown || got.Observed.CPUPeakMilli != 100 || !got.Observed.RAMKnown || got.Observed.RAMPeakBytes != 10 ||
		got.Uncertainty != JobSizingUncertaintySampledPeakBound {
		t.Fatalf("genuine low usage not reported as measured usage: %+v", got.Observed)
	}
	// At the suppression band the increase is withheld; one millicpu above it
	// is advised. The band boundary is exact and deterministic.
	boundary := advisoryCohortFixture(t, 20, JobSizingShape{CPUMilli: 1000, RAMBytes: 100000}, 849, 10, false, MinJobSizingHistory, 9*time.Second)
	got = advisoryFixture(t, JobSizingHostContext{}, boundary, sizingReviewWindowFixture(boundary))
	if got.Outcome != JobSizingAdvisoryUnchanged || got.CPU.Suggested != nil || got.CPU.Reason != JobSizingNoReasonOscillationSuppressed {
		t.Fatalf("in-band increase was not suppressed: %+v", got.CPU)
	}
	advised := advisoryCohortFixture(t, 20, JobSizingShape{CPUMilli: 1000, RAMBytes: 100000}, 850, 10, false, MinJobSizingHistory, 9*time.Second)
	got = advisoryFixture(t, JobSizingHostContext{}, advised, sizingReviewWindowFixture(advised))
	if got.Outcome != JobSizingAdvisoryIncreaseAdvised || got.CPU.Suggested == nil || *got.CPU.Suggested != 1063 {
		t.Fatalf("above-band increase was not advised: %+v", got.CPU)
	}
}

func TestReviewJobSizingAdvisoryWithholdsStaleRiskAndUnreviewedCohorts(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	staleWindow := sizingReviewWindowFixture(base)
	staleWindow.AsOf = staleWindow.AsOf.Add(24 * time.Hour)
	got := advisoryFixture(t, JobSizingHostContext{}, base, staleWindow)
	if got.Outcome != JobSizingAdvisoryStaleHistory || got.CPU.Suggested != nil || got.RAM.Suggested != nil ||
		got.CPU.Reason != JobSizingNoReasonStale || got.RAM.Reason != JobSizingNoReasonStale || got.Freshness.Fresh {
		t.Fatalf("stale history was not withheld: %+v", got)
	}
	if got.Freshness.Age != 25*time.Hour {
		t.Fatalf("stale source age not carried: %+v", got.Freshness)
	}
	risky := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	risky.Samples[3].Risks = JobSizingExternalBuilder
	got = advisoryFixture(t, JobSizingHostContext{}, risky, sizingReviewWindowFixture(risky))
	if got.Outcome != JobSizingAdvisoryNeedsRiskOrOutcomeReview || got.CPU.Suggested != nil || got.RAM.Suggested != nil ||
		got.CPU.Reason != JobSizingNoReasonRiskEvidence || got.Samples.AtRisk != 1 {
		t.Fatalf("unreliable external work was not held for review: %+v", got)
	}
	failed := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	failed.Samples[3].Outcome = TrendOutcomeFailure
	got = advisoryFixture(t, JobSizingHostContext{}, failed, sizingReviewWindowFixture(failed))
	if got.Outcome != JobSizingAdvisoryNeedsRiskOrOutcomeReview || got.CPU.Reason != JobSizingNoReasonUnsuccessful {
		t.Fatalf("unsuccessful jobs were not held for review: %+v", got)
	}
	window := sizingReviewWindowFixture(base)
	proj, err := ProjectJobSizingCohort(base.Scope, sizingTestIdentity, base.Shape, base.Coverage, false, window, sizingCandidatesFor(sizingTestIdentity, base))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	got, err = ReviewJobSizingAdvisory(proj, window, JobSizingHostContext{})
	if err != nil || got.Outcome != JobSizingAdvisoryUnreviewed || got.CPU.Reason != JobSizingNoReasonNotReviewed ||
		got.CPU.Suggested != nil || got.RAM.Suggested != nil || got.Installed != (JobSizingShape{}) {
		t.Fatalf("unreviewed comparability must stay neutral: %+v, %v", got, err)
	}
}

func TestReviewJobSizingAdvisoryDistinguishesLowUsageFromMissingSamples(t *testing.T) {
	missing := advisoryCohortFixture(t, 20, JobSizingShape{CPUMilli: 1000, RAMBytes: 100}, 100, 96, false, MinJobSizingHistory, 9*time.Second)
	for i := range missing.Samples {
		missing.Samples[i].Resources.CPU = nil
	}
	got := advisoryFixture(t, JobSizingHostContext{}, missing, sizingReviewWindowFixture(missing))
	if got.Outcome != JobSizingAdvisoryIncreaseAdvised || got.Observed.CPUKnown || got.CPU.State != JobSizingAdvisoryCoverageUnavailable ||
		got.CPU.Reason != JobSizingNoReasonMissingCoverage {
		t.Fatalf("missing samples must not read as low usage: %+v", got)
	}
	if !got.Observed.RAMKnown || got.RAM.State != JobSizingAdvisoryIncreaseAdvised {
		t.Fatalf("independent RAM coverage not preserved: %+v", got)
	}
	sparse := advisoryCohortFixture(t, 5, JobSizingShape{CPUMilli: 1000, RAMBytes: 100000}, 100, 10, false, MinJobSizingHistory, 9*time.Second)
	got = advisoryFixture(t, JobSizingHostContext{}, sparse, sizingReviewWindowFixture(sparse))
	if got.Outcome != JobSizingAdvisoryInsufficientHistory || got.CPU.Reason != JobSizingNoReasonTooFewAttempts || got.CPU.Suggested != nil {
		t.Fatalf("sparse history produced a confident advice: %+v", got)
	}
	incomplete := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	incomplete.Coverage = TrendCoveragePartial
	got = advisoryFixture(t, JobSizingHostContext{}, incomplete, sizingReviewWindowFixture(incomplete))
	if got.Outcome != JobSizingAdvisoryCohortIncomplete || got.CPU.Reason != JobSizingNoReasonIncompleteCoverage || got.CPU.Suggested != nil {
		t.Fatalf("incomplete cohort coverage produced a confident advice: %+v", got)
	}
}

func TestReviewJobSizingAdvisoryCountsOmittedCandidatesWithoutRetainingIdentifiers(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	foreign := SizingCandidate{Identity: sizingTestIdentity, Sample: base.Samples[0]}
	foreign.Sample.Resources.Scope.Job.Attempt.RepositoryID = 4242
	foreign.Sample.Resources.Scope.Job.JobID = 9999
	unverifiable := SizingCandidate{Identity: SizingCohortIdentity{}, Sample: base.Samples[1]}
	proj, err := ProjectJobSizingCohort(base.Scope, sizingTestIdentity, base.Shape, base.Coverage, true, window,
		append(sizingCandidatesFor(sizingTestIdentity, base), foreign, unverifiable))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	got, err := ReviewJobSizingAdvisory(proj, window, JobSizingHostContext{})
	if err != nil || got.Foreign != 1 || got.Unverifiable != 1 || len(got.Rejected) != 0 {
		t.Fatalf("omitted candidates were not counted without retained identifiers: %+v, %v", got, err)
	}
	if got.Samples.Eligible != 20 || got.Outcome != JobSizingAdvisoryIncreaseAdvised {
		t.Fatalf("omitted candidates changed the reviewed cohort: %+v", got.Samples)
	}
}

func TestReviewJobSizingAdvisoryDiskAdviceOnlyWithAttributableData(t *testing.T) {
	withDisk := advisoryCohortFixture(t, 20, JobSizingShape{CPUMilli: 1000, RAMBytes: 100000}, 100, 10, true, MinJobSizingHistory, 9*time.Second)
	got := advisoryFixture(t, JobSizingHostContext{}, withDisk, sizingReviewWindowFixture(withDisk))
	if got.Disk.State != JobSizingDiskObserved || got.Disk.Samples != 20 || got.Disk.Qualified != 20 || got.Disk.Peak != 1119 {
		t.Fatalf("attributable disk gauges were not reported: %+v", got.Disk)
	}
	without := advisoryCohortFixture(t, 20, JobSizingShape{CPUMilli: 1000, RAMBytes: 100000}, 100, 10, false, MinJobSizingHistory, 9*time.Second)
	got = advisoryFixture(t, JobSizingHostContext{}, without, sizingReviewWindowFixture(without))
	if got.Disk.State != JobSizingDiskUnavailable || got.Disk.Samples != 0 || got.Disk.Qualified != 20 || got.Disk.Peak != 0 {
		t.Fatalf("disk advice appeared without attributable data: %+v", got.Disk)
	}
}

func TestReviewJobSizingAdvisoryRejectsInconsistentInputs(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	proj, err := ProjectJobSizingCohort(base.Scope, sizingTestIdentity, base.Shape, base.Coverage, true, window, sizingCandidatesFor(sizingTestIdentity, base))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	proj.Version = "job-sizing-cohort-policy/v0"
	if got, err := ReviewJobSizingAdvisory(proj, window, JobSizingHostContext{}); !errors.Is(err, ErrJobSizingFacts) || !reflect.DeepEqual(got, JobSizingAdvisory{}) {
		t.Fatalf("stale policy version was accepted: %+v, %v", got, err)
	}
	proj.Version = JobSizingCohortPolicyVersion
	for name, host := range map[string]JobSizingHostContext{
		"allocated exceeds cpu budget": {Known: true, Host: "host-a", CPUBudgetMilli: 100, CPUAllocatedMilli: 101},
		"allocated exceeds ram budget": {Known: true, Host: "host-a", RAMBudgetBytes: 100, RAMAllocatedBytes: 101},
		"invalid host label":           {Known: true, Host: "-leading-dash"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ReviewJobSizingAdvisory(proj, window, host); !errors.Is(err, ErrJobSizingFacts) || !reflect.DeepEqual(got, JobSizingAdvisory{}) {
				t.Fatalf("inconsistent host context accepted: %+v, %v", got, err)
			}
		})
	}
}
