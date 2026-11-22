package cihealthreport

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func impactInput(caseID int64, observations ...ReviewedCaseObservation) ReviewedCaseImpactInput {
	cohort := compareCohort
	cohort.CaseID = caseID
	for i := range observations {
		r := *observations[i].Review
		r.Cohort = cohort
		observations[i].Review = &r
	}
	return ReviewedCaseImpactInput{cohort, observations}
}

func TestRankReviewedCaseImpactCoverageAndStableTies(t *testing.T) {
	missing := impactInput(1, reviewedTestCase(t, 2, 1, 0, 301, Skipped, ""))
	zero := impactInput(2, reviewedTestCase(t, 2, 1, 0, 302, Pass, "0"))
	tieLater := impactInput(6, reviewedTestCase(t, 2, 1, 0, 303, Pass, "4"))
	tieEarlier := impactInput(4, reviewedTestCase(t, 2, 1, 0, 304, Pass, "4"))
	heavy := impactInput(7, reviewedTestCase(t, 2, 1, 0, 305, Failure, "3"),
		reviewedTestCase(t, 2, 2, 0, 306, Pass, "3"),
		reviewedTestCase(t, 3, 1, 0, 307, Error, ""))
	inputs := []ReviewedCaseImpactInput{missing, zero, tieLater, tieEarlier, heavy}
	got, err := RankReviewedCaseImpact(inputs)
	if err != nil || len(got) != 5 {
		t.Fatalf("rank failed: %+v %v", got, err)
	}
	ids := make([]int64, 0, len(got))
	for _, row := range got {
		ids = append(ids, row.CaseID)
	}
	if !reflect.DeepEqual(ids, []int64{7, 4, 6, 2, 1}) ||
		got[0].Comparison.All.TotalTimedDuration != 6*time.Second ||
		got[0].Comparison.All.Timed != 2 || got[0].Comparison.All.Cases != 3 ||
		got[0].Comparison.First.TotalTimedDuration != 3*time.Second ||
		got[0].Comparison.Repeat.TotalTimedDuration != 3*time.Second ||
		got[3].Comparison.All.Timed != 1 || got[4].Comparison.All.Timed != 0 {
		t.Fatalf("wrong rank or coverage: %+v", got)
	}
	reversed := []ReviewedCaseImpactInput{heavy, tieEarlier, tieLater, zero, missing}
	for i, j := 0, len(heavy.Observations)-1; i < j; i, j = i+1, j-1 {
		heavy.Observations[i], heavy.Observations[j] = heavy.Observations[j], heavy.Observations[i]
	}
	again, err := RankReviewedCaseImpact(reversed)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("input order changed ranking: %+v %v", again, err)
	}
	if s := fmt.Sprintf("%+v", got); strings.Contains(s, compareCohort.ShardKey) ||
		strings.Contains(s, compareCohort.Source) || strings.Contains(s, "ArtifactID") {
		t.Fatal("ranking leaked caller proof or free-form labels")
	}
}

func TestRankReviewedCaseImpactRejectsConflictsAndCrossCategory(t *testing.T) {
	first := reviewedTestCase(t, 2, 1, 0, 301, Pass, "1")
	a, duplicate := impactInput(101, first), impactInput(102, first)
	distinctOccurrence := impactInput(102, first)
	distinctOccurrence.Observations[0].Review.Occurrence = 1
	if rows, err := RankReviewedCaseImpact([]ReviewedCaseImpactInput{a, distinctOccurrence}); err != nil ||
		len(rows) != 2 || rows[1].Comparison.All.Timed != 0 {
		t.Fatalf("distinct reviewed occurrences in one report could not be ranked: %+v %v", rows, err)
	}
	contradiction := impactInput(102, reviewedTestCase(t, 2, 1, 0, 301, Pass, "2"))
	outside := impactInput(102, reviewedTestCase(t, 2, 1, 0, 302, Pass, "1"))
	outside.Cohort.ConfigurationID++
	outside.Observations[0].Review.Cohort = outside.Cohort
	if _, err := CompareReviewedCase(outside.Cohort, outside.Observations, 0); err != nil {
		t.Fatalf("foreign cohort should be individually valid: %v", err)
	}
	conflictShard := impactInput(102, reviewedTestCase(t, 2, 1, 0, 303, Pass, "1"))
	conflictShard.Cohort.ShardID++
	conflictShard.Observations[0].Review.Cohort = conflictShard.Cohort
	oversize := impactInput(102, first)
	oversize.Observations = make([]ReviewedCaseObservation, MaxComparisonObservations)
	for name, bad := range map[string]ReviewedCaseImpactInput{
		"duplicate cohort": a, "duplicate report occurrence": duplicate,
		"conflicting report bytes for one artifact": contradiction, "outside category": outside,
		"two numeric shards for one reviewed shard label": conflictShard, "total bound": oversize,
	} {
		t.Run(name, func(t *testing.T) {
			// Exercise the same artifact ID with different reviewed bytes at
			// different positions, rather than relying on the duplicate check.
			base := a
			if name == "conflicting report bytes for one artifact" {
				base = impactInput(101, first)
				base.Observations[0].Review.Occurrence = 1
			}
			rows, err := RankReviewedCaseImpact([]ReviewedCaseImpactInput{base, bad})
			if rows != nil || !errors.Is(err, ErrCaseComparison) {
				t.Fatalf("invalid reviews produced a rank: %+v %v", rows, err)
			}
		})
	}
}

func TestRankReviewedCaseImpactKeepsDistinctShardsOfSameCase(t *testing.T) {
	low := impactInput(101, reviewedTestCase(t, 2, 1, 0, 301, Pass, "1"))
	high := impactInput(101, reviewedTestCase(t, 2, 1, 0, 302, Pass, "1"))
	high.Cohort.ShardID++
	high.Cohort.ShardKey = "second_fixture_shard"
	r := high.Observations[0].Review
	r.Cohort = high.Cohort
	raw := []byte(`<testsuite><testcase name="synthetic-private" time="1"/></testsuite>`)
	r.Occurrence, r.ExpectedReportSHA256 = 1, sha256.Sum256(raw)
	binding := high.Observations[0].Report.Binding()
	binding.Shard = high.Cohort.ShardKey
	report, err := ParseJUnit(raw, binding)
	if err != nil {
		t.Fatal(err)
	}
	high.Observations[0].Report = report
	got, err := RankReviewedCaseImpact([]ReviewedCaseImpactInput{high, low})
	if err != nil || len(got) != 2 || got[0].CaseID != 101 || got[1].CaseID != 101 ||
		got[0].ShardID != low.Cohort.ShardID || got[1].ShardID != high.Cohort.ShardID ||
		got[0].Comparison.All.TotalTimedDuration != got[1].Comparison.All.TotalTimedDuration {
		t.Fatalf("distinct reviewed shards pooled or ordered incorrectly: %+v %v", got, err)
	}
}

func TestRankReviewedCaseImpactRequiresConsistentJobAcrossCohorts(t *testing.T) {
	first := impactInput(101, reviewedTestCaseOnJob(t, 1, 1, 0, 201, 20, Failure, "1"))
	sameJob := impactInput(102, reviewedTestCaseOnJob(t, 1, 1, 0, 202, 20, Pass, "1"))
	if got, err := RankReviewedCaseImpact([]ReviewedCaseImpactInput{first, sameJob}); err != nil || len(got) != 2 {
		t.Fatalf("valid distinct cases in one job refused: %+v %v", got, err)
	}
	for _, second := range []ReviewedCaseImpactInput{
		impactInput(102, reviewedTestCaseOnJob(t, 1, 2, 0, 203, 20, Pass, "1")),
		impactInput(102, reviewedTestCaseOnJob(t, 2, 1, 0, 204, 20, Pass, "1")),
	} {
		if got, err := RankReviewedCaseImpact([]ReviewedCaseImpactInput{first, second}); got != nil ||
			!errors.Is(err, ErrCaseComparison) {
			t.Fatalf("conflicting job claimed by different cohorts: %+v %v", got, err)
		}
	}
}
