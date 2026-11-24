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
	if len(proj.Cohort.Samples) != 20 || proj.Incomparable != 2 || proj.Foreign != 1 || proj.Unverifiable != 0 {
		t.Fatalf("incomparable identities were combined or hidden: %+v", proj)
	}
	// Out-of-scope identifiers are never retained: the cross-tenant candidate
	// is only counted, and every keyed rejection stays inside the authorized
	// repository scope.
	want := map[SizingRejectionReason]int{
		SizingRejectionIncomparableIdentity: 2,
		SizingRejectionProfileChanged:       1,
	}
	got := map[SizingRejectionReason]int{}
	for _, r := range proj.Rejected {
		got[r.Reason]++
		if r.Job.JobID <= 0 || r.Job.Attempt.RepositoryID != base.Scope.RepositoryID || r.Job.Attempt.Source != base.Scope.Source {
			t.Fatalf("keyed rejection left the authorized scope: %+v", r)
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
	if len(proj.Cohort.Samples) != 20 || proj.Unverifiable != 2 {
		t.Fatalf("ineligible candidates entered the cohort or unverifiable identities were retained: %+v", proj)
	}
	want := map[SizingRejectionReason]int{
		SizingRejectionStale:     1,
		SizingRejectionDuplicate: 1,
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
	review, err := ReviewJobSizingWindow(proj.Cohort, window)
	if err != nil || review.Summary.Successful != 20 || review.Summary.CPU.State != JobSizingIncreaseAdvised {
		t.Fatalf("review did not see only the eligible selection: %+v, %v", review, err)
	}
}

func TestProjectJobSizingCohortResolvesConflictingDuplicatesInEitherInputOrder(t *testing.T) {
	base := jobSizingFixture(t, 20, MinJobSizingHistory, 9*time.Second)
	window := sizingReviewWindowFixture(base)
	// A conflicting re-submission of an eligible execution: identical exact
	// job key, different observation time and measured content.
	conflicting := SizingCandidate{Identity: sizingTestIdentity, Sample: base.Samples[0]}
	conflicting.Sample.Resources.Scope.AsOf = conflicting.Sample.Resources.Scope.AsOf.Add(time.Minute)
	conflicting.Sample.Resources.RAM = &ResourceGaugeSummary{Samples: 2, SampleMax: 99, Latest: 99, LatestAt: base.Samples[0].Resources.RAM.LatestAt}
	project := func(candidates ...SizingCandidate) SizingCohortProjection {
		t.Helper()
		proj, err := ProjectJobSizingCohort(base.Scope, sizingTestIdentity, base.Shape, base.Coverage, true, window, candidates)
		if err != nil {
			t.Fatalf("projection: %v", err)
		}
		return proj
	}
	eligible := sizingCandidatesFor(sizingTestIdentity, base)
	tailFirst := project(append(append([]SizingCandidate{}, eligible...), conflicting)...)
	tailLast := project(append([]SizingCandidate{conflicting}, eligible...)...)
	if !reflect.DeepEqual(tailFirst, tailLast) {
		t.Fatalf("input order changed the conflicting-duplicate resolution:\n%+v\n%+v", tailFirst, tailLast)
	}
	if len(tailFirst.Cohort.Samples) != 20 || len(tailFirst.Rejected) != 1 || tailFirst.Rejected[0].Reason != SizingRejectionDuplicate {
		t.Fatalf("conflicting duplicate was not reported exactly once: %+v", tailFirst.Rejected)
	}
	kept := tailFirst.Cohort.Samples[0]
	if !kept.Resources.Scope.AsOf.Equal(base.Samples[0].Resources.Scope.AsOf.Add(time.Minute)) || kept.Resources.RAM.SampleMax != 99 {
		t.Fatalf("the most recently observed candidate did not win: %+v", kept.Resources.Scope)
	}
	// A conflicting job ID across distinct runs resolves the same way: the
	// later observation wins regardless of input order.
	conflictingID := SizingCandidate{Identity: sizingTestIdentity, Sample: base.Samples[1]}
	conflictingID.Sample.Resources.Scope.Job.Attempt.RunID = 500
	conflictingID.Sample.Resources.Scope.AsOf = conflictingID.Sample.Resources.Scope.AsOf.Add(2 * time.Minute)
	idTailFirst := project(append(append([]SizingCandidate{}, eligible...), conflictingID)...)
	idTailLast := project(append([]SizingCandidate{conflictingID}, eligible...)...)
	if !reflect.DeepEqual(idTailFirst, idTailLast) {
		t.Fatalf("input order changed the conflicting job-id resolution:\n%+v\n%+v", idTailFirst, idTailLast)
	}
	if len(idTailFirst.Cohort.Samples) != 20 || len(idTailFirst.Rejected) != 1 {
		t.Fatalf("conflicting job-id duplicate was not reported exactly once: %+v", idTailFirst.Rejected)
	}
	loser := idTailFirst.Rejected[0]
	if loser.Reason != SizingRejectionDuplicate || loser.Job.Attempt.RunID != 2 {
		t.Fatalf("conflicting job-id duplicate was not reported against the losing execution: %+v", loser)
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
