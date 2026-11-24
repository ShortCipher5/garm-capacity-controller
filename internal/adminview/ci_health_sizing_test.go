package adminview

import (
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func ciSizingAdvisoryFixture(at time.Time) cihealth.JobSizingAdvisory {
	suggestedCPU, suggestedRAM := uint64(1125), uint64(120)
	return cihealth.JobSizingAdvisory{
		Version:       cihealth.JobSizingAdvisoryVersion,
		PolicyVersion: cihealth.JobSizingCohortPolicyVersion,
		Outcome:       cihealth.JobSizingAdvisoryIncreaseAdvised,
		Scope:         cihealth.TrendScope{Source: "github", RepositoryID: 12, WorkflowID: 44},
		Identity:      cihealth.SizingCohortIdentity{Matrix: "ubuntu-24.04", Profile: "small", Revision: "rev-3"},
		Coverage:      cihealth.TrendCoverageComplete,
		Window:        cihealth.JobSizingReviewWindow{From: at.Add(-7 * 24 * time.Hour), AsOf: at, LatestCompletionMaxAge: time.Hour},
		Freshness: cihealth.JobSizingAdvisoryFreshness{
			LatestPrimaryCompletion: at.Add(-30 * time.Minute), Age: 30 * time.Minute, Fresh: true,
		},
		Samples:     cihealth.JobSizingAdvisorySamples{Eligible: 20, Primary: 20, Successful: 20, QualifyingCPU: 20, QualifyingRAM: 20},
		Observed:    cihealth.JobSizingAdvisoryObserved{CPUKnown: true, CPUPeakMilli: 900, RAMKnown: true, RAMPeakBytes: 96, CPUSaturated: 3},
		Installed:   cihealth.JobSizingShape{CPUMilli: 1000, RAMBytes: 100},
		CPU:         cihealth.JobSizingDimensionAdvice{State: cihealth.JobSizingAdvisoryIncreaseAdvised, Suggested: &suggestedCPU},
		RAM:         cihealth.JobSizingDimensionAdvice{State: cihealth.JobSizingAdvisoryIncreaseAdvised, Suggested: &suggestedRAM},
		Uncertainty: cihealth.JobSizingUncertaintySampledPeakBound,
		Host: cihealth.JobSizingAdvisoryHost{
			Known: true, Host: "host-a", CPUBudgetMilli: 64000, CPUAllocatedMilli: 4000, CPUHeadroomMilli: 60000,
			RAMBudgetBytes: 64 << 30, RAMAllocatedBytes: 4 << 30, RAMHeadroomBytes: 60 << 30, Fit: cihealth.JobSizingHostFitFits,
		},
		Warnings: []string{cihealth.JobSizingWarningSustainedCPUPressure},
		Disk:     cihealth.JobSizingDiskAdvice{State: cihealth.JobSizingDiskUnavailable, Qualified: 20},
		Rejected: []cihealth.SizingRejection{{
			Job:    cihealth.JobKey{Attempt: cihealth.AttemptKey{Source: "github", RepositoryID: 12, WorkflowID: 44, RunID: 7, Attempt: 1}, JobID: 100},
			Reason: cihealth.SizingRejectionStale,
		}},
		Incomparable: 2,
		Unverifiable: 1,
		Foreign:      1,
	}
}

func TestCISizingAPIOptionalReadOnlyAndUnavailableByDefault(t *testing.T) {
	h, _, token := fixture(t)
	const route = "/admin/api/v1/ci-health/sizing"
	if request(t, h, "GET", route, token).Code != 404 {
		t.Fatal("sizing route enabled by default")
	}
	s, err := NewCISizingSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil || h.SetCISizingSnapshot(s) != nil || h.SetCISizingSnapshot(s) == nil {
		t.Fatalf("invalid startup attachment: %v", err)
	}
	if request(t, h, "GET", route, "").Code != 401 ||
		request(t, h, "POST", route, token).Code != 405 ||
		request(t, h, "GET", route+"?workflow=44", token).Code != 400 {
		t.Fatal("sizing owner read boundary changed")
	}
	if !strings.Contains(request(t, h, "GET", route, token).Body.String(), `"state":"unavailable"`) ||
		!strings.Contains(request(t, h, "GET", route, token).Body.String(), `"sizing":null`) {
		t.Fatal("unpublished source must serve the unavailable state, not an empty advice")
	}
}

func TestCISizingPublishServesExactReviewedAdviceOnly(t *testing.T) {
	h, _, token := fixture(t)
	s, err := NewCISizingSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil || h.SetCISizingSnapshot(s) != nil {
		t.Fatalf("attach: %v", err)
	}
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	advisory := ciSizingAdvisoryFixture(at)
	foreign := advisory
	foreign.Scope.RepositoryID = 14
	if s.Publish(at, foreign) == nil {
		t.Fatal("mutating startup input expanded bound repo scope")
	}
	misaligned := advisory
	misaligned.Window.AsOf = at.Add(time.Second)
	if s.Publish(at, misaligned) == nil {
		t.Fatal("publication time did not equal the review time")
	}
	staleVersion := advisory
	staleVersion.PolicyVersion = "job-sizing-cohort-policy/v0"
	if s.Publish(at, staleVersion) == nil {
		t.Fatal("stale policy version published")
	}
	if err := s.Publish(at, advisory); err != nil {
		t.Fatal(err)
	}
	res := request(t, h, "GET", "/admin/api/v1/ci-health/sizing", token)
	if res.Code != 200 || res.Header().Get("Cache-Control") == "" {
		t.Fatalf("not the exact owner read: %d %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		`"state":"observed"`, `"policy_version":"job-sizing-cohort-policy/v1"`, `"advisory_version":"job-sizing-advisory/v1"`,
		`"outcome":"increase_advised"`, `"workflow_id":"44"`, `"matrix":"ubuntu-24.04"`, `"age_ns":"1800000000000"`, `"fresh":true`,
		`"eligible":20`, `"qualifying_cpu":20`, `"cpu_peak_milli":"900"`, `"ram_peak_bytes":"96"`, `"cpu_saturated":3`,
		`"cpu_milli":"1000"`, `"suggested":"1125"`, `"uncertainty":"sampled_peak_bound"`, `"cpu_headroom_milli":"60000"`,
		`"fit":"fits"`, `"sustained_cpu_pressure"`, `"disk"`, `"peak_bytes":null`, `"reason":"stale"`, `"incomparable":2`, `"unverifiable":1`, `"foreign":1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sizing view missing %s: %s", want, body)
		}
	}
	s.Withdraw()
	if s.Publish(at, advisory) == nil {
		t.Fatal("an older publication resurrected the result")
	}
	if strings.Contains(request(t, h, "GET", "/admin/api/v1/ci-health/sizing", token).Body.String(), `"outcome"`) {
		t.Fatal("withdrawn advice still displayed")
	}
}

func TestCISizingViewKeepsMissingFactsNullNotZero(t *testing.T) {
	s, err := NewCISizingSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	advisory := cihealth.JobSizingAdvisory{
		Version:       cihealth.JobSizingAdvisoryVersion,
		PolicyVersion: cihealth.JobSizingCohortPolicyVersion,
		Outcome:       cihealth.JobSizingAdvisoryUnreviewed,
		Scope:         cihealth.TrendScope{Source: "github", RepositoryID: 12, WorkflowID: 44},
		Identity:      cihealth.SizingCohortIdentity{Profile: "small"},
		Coverage:      cihealth.TrendCoverageUnknown,
		Window:        cihealth.JobSizingReviewWindow{From: at.Add(-time.Hour), AsOf: at, LatestCompletionMaxAge: time.Minute},
		CPU:           cihealth.JobSizingDimensionAdvice{State: cihealth.JobSizingAdvisoryUnreviewed, Reason: cihealth.JobSizingNoReasonNotReviewed},
		RAM:           cihealth.JobSizingDimensionAdvice{State: cihealth.JobSizingAdvisoryUnreviewed, Reason: cihealth.JobSizingNoReasonNotReviewed},
		Host:          cihealth.JobSizingAdvisoryHost{Fit: cihealth.JobSizingHostFitUnknown},
		Disk:          cihealth.JobSizingDiskAdvice{State: cihealth.JobSizingDiskUnavailable},
	}
	if err := s.Publish(at, advisory); err != nil {
		t.Fatal(err)
	}
	h, _, token := fixture(t)
	if err := h.SetCISizingSnapshot(s); err != nil {
		t.Fatal(err)
	}
	body := request(t, h, "GET", "/admin/api/v1/ci-health/sizing", token).Body.String()
	for _, want := range []string{
		`"outcome":"unreviewed"`, `"latest_primary_completion":null`, `"cpu_peak_milli":null`, `"ram_peak_bytes":null`,
		`"known":false`, `"cpu_budget_milli":null`, `"fit":"unknown"`, `"warnings":[]`, `"reason":"not_reviewed"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("neutral advisory misencoded %s: %s", want, body)
		}
	}
}

func TestCISizingPublisherExpiryAndRollbackFencing(t *testing.T) {
	s, err := NewCISizingSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := at
	s.clock = func() time.Time { return now }
	advisory := ciSizingAdvisoryFixture(at)
	advisory.Window = cihealth.JobSizingReviewWindow{From: at.Add(-time.Hour), AsOf: at, LatestCompletionMaxAge: time.Minute}
	advisory.Freshness = cihealth.JobSizingAdvisoryFreshness{}
	if err := s.Publish(at, advisory); err != nil {
		t.Fatal(err)
	}
	now = at.Add(ciDisplayLifetime - time.Nanosecond)
	if s.read() == nil {
		t.Fatal("unexpired advisory unavailable")
	}
	now = at.Add(ciDisplayLifetime)
	if s.read() != nil {
		t.Fatal("expired advisory remains visible")
	}
	now = at.Add(10 * time.Second)
	s.Withdraw()
	now = at.Add(40 * time.Second)
	if s.Publish(at.Add(20*time.Second), advisory) == nil || s.read() != nil {
		t.Fatal("observation at or below the rollback withdrawal watermark was published")
	}
	now = at.Add(ciDisplayLifetime + time.Second)
	if err := s.Publish(now, ciSizingAdvisoryFixture(now)); err != nil || s.read() == nil {
		t.Fatalf("fresh observation after watermark did not publish: %v", err)
	}
	s.Stop()
	s.Withdraw()
	if s.read() != nil {
		t.Fatal("stopped source still served a snapshot")
	}
}

func TestCISizingOverviewAdvertisesOnlyAnAttachedSource(t *testing.T) {
	h, _, token := fixture(t)
	const path = "/admin/api/v1/overview"
	assertOverview := func(wantMarker bool) {
		t.Helper()
		auth := request(t, h, "GET", path, token)
		found := strings.Contains(auth.Body.String(), `"ci_health_sizing_available":true`)
		if auth.Code != 200 || found != wantMarker ||
			(!wantMarker && strings.Contains(auth.Body.String(), `"ci_health_sizing_available"`)) {
			t.Fatal("incorrect sizing overview availability")
		}
		for _, rejected := range []string{"", strings.Repeat("0", 64)} {
			unauth := request(t, h, "GET", path, rejected)
			if unauth.Code != 401 || strings.Contains(unauth.Body.String(), "ci_health_sizing_available") {
				t.Fatal("unauthorized overview leaked sizing availability")
			}
		}
	}
	assertOverview(false)
	if h.SetCISizingSnapshot(nil) == nil || h.SetCISizingSnapshot(&CISizingSnapshot{}) == nil {
		t.Fatal("invalid source attachment")
	}
	source, err := NewCISizingSnapshot([]CIHealthRepository{{"github", 17}})
	if err != nil || h.SetCISizingSnapshot(source) != nil {
		t.Fatalf("attach configured source: %v", err)
	}
	assertOverview(true)
	source.Withdraw()
	assertOverview(true)
}
