package cihealth

import (
	"errors"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

var sizingTestIdentity = SizingCohortIdentity{Profile: "small", Revision: "rev-1"}

func sizingCandidatesFor(identity SizingCohortIdentity, in JobSizingCohort) []SizingCandidate {
	out := make([]SizingCandidate, 0, len(in.Samples))
	for _, sample := range in.Samples {
		out = append(out, SizingCandidate{Identity: identity, Sample: sample})
	}
	return out
}

func sizingProjectionFixture(t *testing.T, reviewed bool, window JobSizingReviewWindow, candidates ...SizingCandidate) SizingCohortProjection {
	t.Helper()
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	proj, err := ProjectJobSizingCohort(base.Scope, sizingTestIdentity, base.Shape, base.Coverage, reviewed, window,
		append(sizingCandidatesFor(sizingTestIdentity, base), candidates...))
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	return proj
}

func sizingReviewWindowFixture(in JobSizingCohort) JobSizingReviewWindow {
	first := in.Samples[0].Resources.Scope
	last := in.Samples[len(in.Samples)-1].Resources.Scope
	return JobSizingReviewWindow{From: first.StartedAt, AsOf: last.EndAt.Add(time.Hour), LatestCompletionMaxAge: time.Hour}
}

func TestProjectJobSizingCohortSelectsComparableExactHistoryDeterministically(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	candidates := sizingCandidatesFor(sizingTestIdentity, base)
	proj := sizingProjectionFixture(t, true, window)
	if proj.Version != JobSizingCohortPolicyVersion || proj.Cohort.Comparability != JobSizingReviewedComparable ||
		len(proj.Cohort.Samples) != 20 || len(proj.Rejected) != 0 || proj.Incomparable != 0 ||
		proj.Coverage != TrendCoverageComplete || proj.Cohort.Shape != base.Shape {
		t.Fatalf("projection did not select the exact cohort: %+v", proj)
	}
	review, err := ReviewJobSizingWindow(proj.Cohort, window)
	if err != nil || review.Summary.CPU.State != JobSizingIncreaseAdvised || !review.FreshPrimaryCompletion {
		t.Fatalf("projected cohort is not reviewable: %+v, %v", review, err)
	}
	// Any input order produces the identical projection, so a recommendation
	// derived from it is reproducible from the exact same source facts.
	shuffled := append([]SizingCandidate(nil), candidates...)
	rand.New(rand.NewSource(7)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	again, err := ProjectJobSizingCohort(base.Scope, sizingTestIdentity, base.Shape, base.Coverage, true, window, shuffled)
	if err != nil || !reflect.DeepEqual(proj, again) {
		t.Fatalf("input order changed the projection:\n%+v\n%+v\n%v", proj, again, err)
	}
	for i := 1; i < len(proj.Cohort.Samples); i++ {
		if compareJobKey(proj.Cohort.Samples[i-1].Resources.Scope.Job, proj.Cohort.Samples[i].Resources.Scope.Job) >= 0 {
			t.Fatalf("eligible samples not sorted by exact job key at %d", i)
		}
	}
}

func TestProjectJobSizingCohortNeverCombinesDistinctIdentitiesOrProfiles(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	otherMatrix := SizingCandidate{
		Identity: SizingCohortIdentity{Matrix: "ubuntu-24.04", Profile: "small", Revision: "rev-1"},
		Sample:   base.Samples[0],
	}
	otherProfile := SizingCandidate{
		Identity: SizingCohortIdentity{Profile: "large", Revision: "rev-2"},
		Sample:   base.Samples[1],
	}
	changedShape := SizingCandidate{
		Identity: sizingTestIdentity,
		Sample:   JobSizingSample{Resources: base.Samples[2].Resources, Completion: base.Samples[2].Completion, Shape: JobSizingShape{2000, 200}, Outcome: TrendOutcomeSuccess, RiskKnown: true},
	}
	foreign := SizingCandidate{Identity: sizingTestIdentity, Sample: base.Samples[3]}
	foreign.Sample.Resources.Scope.Job.Attempt.RepositoryID = 99
	proj := sizingProjectionFixture(t, true, window, otherMatrix, otherProfile, changedShape, foreign)
	if len(proj.Cohort.Samples) != 20 || proj.Incomparable != 2 {
		t.Fatalf("incomparable identities were combined or hidden: %+v", proj)
	}
	want := map[SizingRejectionReason]int{
		SizingRejectionIncomparableIdentity: 2,
		SizingRejectionProfileChanged:       1,
		SizingRejectionForeignScope:         1,
	}
	got := map[SizingRejectionReason]int{}
	for _, r := range proj.Rejected {
		got[r.Reason]++
		if r.Job.JobID <= 0 {
			t.Fatalf("rejection lost its exact job identity: %+v", r)
		}
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("rejection reasons = %+v, want %+v", got, want)
	}
	for i := 1; i < len(proj.Rejected); i++ {
		if compareJobKey(proj.Rejected[i-1].Job, proj.Rejected[i].Job) > 0 {
			t.Fatalf("rejections not sorted by exact job key at %d", i)
		}
	}
}

func TestProjectJobSizingCohortReportsStaleDuplicateAndUnverifiableCandidates(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	stale := SizingCandidate{Identity: sizingTestIdentity, Sample: base.Samples[0]}
	stale.Sample.Resources.Scope.StartedAt = stale.Sample.Resources.Scope.StartedAt.Add(-time.Hour)
	stale.Sample.Resources.Scope.EndAt = stale.Sample.Resources.Scope.EndAt.Add(-time.Hour)
	stale.Sample.Resources.Scope.AsOf = stale.Sample.Resources.Scope.AsOf.Add(-time.Hour)
	stale.Sample.Completion.StartedAt = stale.Sample.Completion.StartedAt.Add(-time.Hour)
	stale.Sample.Completion.EndedAt = stale.Sample.Completion.EndedAt.Add(-time.Hour)
	stale.Sample.Completion.ObservedAt = stale.Sample.Completion.ObservedAt.Add(-time.Hour)
	duplicate := SizingCandidate{Identity: sizingTestIdentity, Sample: base.Samples[1]}
	unverifiable := SizingCandidate{Identity: SizingCohortIdentity{}, Sample: base.Samples[2]}
	badKey := SizingCandidate{Identity: sizingTestIdentity, Sample: base.Samples[3]}
	badKey.Sample.Resources.Scope.Job.JobID = 0
	proj := sizingProjectionFixture(t, true, window, stale, duplicate, unverifiable, badKey)
	if len(proj.Cohort.Samples) != 20 {
		t.Fatalf("ineligible candidates entered the cohort: %+v", proj)
	}
	want := map[SizingRejectionReason]int{
		SizingRejectionStale:                1,
		SizingRejectionDuplicate:            1,
		SizingRejectionUnverifiableIdentity: 2,
	}
	got := map[SizingRejectionReason]int{}
	for _, r := range proj.Rejected {
		got[r.Reason]++
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("rejection reasons = %+v, want %+v", got, want)
	}
	review, err := ReviewJobSizingWindow(proj.Cohort, window)
	if err != nil || review.Summary.Successful != 20 || review.Summary.CPU.State != JobSizingIncreaseAdvised {
		t.Fatalf("review did not see only the eligible selection: %+v, %v", review, err)
	}
}

func TestProjectJobSizingCohortRejectsInvalidRequests(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	candidates := sizingCandidatesFor(sizingTestIdentity, base)
	for name, mutate := range map[string]func(*TrendScope, *SizingCohortIdentity, *JobSizingShape, *TrendCoverage, *bool, *JobSizingReviewWindow, *[]SizingCandidate){
		"unknown profile and revision": func(_ *TrendScope, id *SizingCohortIdentity, _ *JobSizingShape, _ *TrendCoverage, _ *bool, _ *JobSizingReviewWindow, _ *[]SizingCandidate) {
			id.Profile, id.Revision = "", ""
		},
		"oversized identity label": func(_ *TrendScope, id *SizingCohortIdentity, _ *JobSizingShape, _ *TrendCoverage, _ *bool, _ *JobSizingReviewWindow, _ *[]SizingCandidate) {
			id.Profile = string(make([]byte, 129))
		},
		"zero installed shape": func(_ *TrendScope, _ *SizingCohortIdentity, sh *JobSizingShape, _ *TrendCoverage, _ *bool, _ *JobSizingReviewWindow, _ *[]SizingCandidate) {
			sh.CPUMilli = 0
		},
		"coverage beyond complete": func(_ *TrendScope, _ *SizingCohortIdentity, _ *JobSizingShape, c *TrendCoverage, _ *bool, _ *JobSizingReviewWindow, _ *[]SizingCandidate) {
			*c = TrendCoverageComplete + 1
		},
		"oversized candidate set": func(_ *TrendScope, _ *SizingCohortIdentity, _ *JobSizingShape, _ *TrendCoverage, _ *bool, _ *JobSizingReviewWindow, c *[]SizingCandidate) {
			*c = make([]SizingCandidate, MaxTrendAttempts+1)
		},
		"window too wide": func(_ *TrendScope, _ *SizingCohortIdentity, _ *JobSizingShape, _ *TrendCoverage, _ *bool, w *JobSizingReviewWindow, _ *[]SizingCandidate) {
			w.From = w.AsOf.Add(-MaxJobSizingReviewWindow - time.Nanosecond)
		},
		"stale window max age": func(_ *TrendScope, _ *SizingCohortIdentity, _ *JobSizingShape, _ *TrendCoverage, _ *bool, w *JobSizingReviewWindow, _ *[]SizingCandidate) {
			w.LatestCompletionMaxAge = w.AsOf.Sub(w.From) + time.Nanosecond
		},
	} {
		t.Run(name, func(t *testing.T) {
			scope, identity, shape, coverage, reviewed, w, offered := base.Scope, sizingTestIdentity, base.Shape, base.Coverage, true, window, candidates
			mutate(&scope, &identity, &shape, &coverage, &reviewed, &w, &offered)
			if got, err := ProjectJobSizingCohort(scope, identity, shape, coverage, reviewed, w, offered); !errors.Is(err, ErrJobSizingFacts) || !reflect.DeepEqual(got, SizingCohortProjection{}) {
				t.Fatalf("invalid request projected a cohort: %+v, %v", got, err)
			}
		})
	}
}
