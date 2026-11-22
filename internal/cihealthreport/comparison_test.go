package cihealthreport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

var compareCohort = ReviewedCaseCohort{
	Source: "fixture_source", RepositoryID: 10, WorkflowID: 11, CaseID: 12,
	ConfigurationID: 13, RevisionID: 14, RunnerClassID: 15, ShardID: 16, ShardKey: "fixture_shard",
}

func reviewedTestCase(t *testing.T, run, attempt int64, retry uint16, artifact int64, outcome Outcome, seconds string) ReviewedCaseObservation {
	t.Helper()
	job := cihealth.JobKey{Attempt: cihealth.AttemptKey{
		Source: compareCohort.Source, RepositoryID: compareCohort.RepositoryID, WorkflowID: compareCohort.WorkflowID, RunID: run, Attempt: attempt,
	}, JobID: artifact + 100}
	binding := ArtifactBinding{
		Attempt: job.Attempt, ArtifactID: artifact, Job: &job, Shard: compareCohort.ShardKey, Retry: &retry,
	}
	marker := ""
	switch outcome {
	case Failure:
		marker = "<failure/>"
	case Error:
		marker = "<error/>"
	case Skipped:
		marker = "<skipped/>"
	}
	duration := ""
	if seconds != "" {
		duration = fmt.Sprintf(" time=%q", seconds)
	}
	// Two anonymous occurrences per artifact; exact out-of-band review chooses
	// the second. The same XML names are neither proof of identity nor retained.
	raw := fmt.Sprintf(`<testsuite><testcase name="synthetic-private"/><testcase name="synthetic-private"%s>%s</testcase></testsuite>`, duration, marker)
	report, err := ParseJUnit([]byte(raw), binding)
	if err != nil {
		t.Fatal(err)
	}
	return ReviewedCaseObservation{Report: report, Review: &ReviewedOccurrence{
		Cohort: compareCohort, ArtifactID: artifact, Job: job, Occurrence: 2, Retry: retry,
		ExpectedReportSHA256: sha256.Sum256([]byte(raw)),
	}}
}

func TestReviewedCaseExactTimingPassFailAndRetryDenominators(t *testing.T) {
	inputs := []ReviewedCaseObservation{
		reviewedTestCase(t, 1, 2, 0, 203, Pass, "1"),
		reviewedTestCase(t, 3, 1, 0, 205, Error, ""),
		reviewedTestCase(t, 5, 1, 1, 208, Skipped, ""),
		reviewedTestCase(t, 2, 1, 0, 204, Pass, "2"),
		reviewedTestCase(t, 1, 1, 0, 201, Failure, "3"),
		reviewedTestCase(t, 6, 4, 2, 209, Pass, "0"),
		reviewedTestCase(t, 5, 1, 0, 207, Failure, "5"),
		reviewedTestCase(t, 1, 1, 1, 202, Failure, "4"),
		reviewedTestCase(t, 4, 1, 0, 206, Skipped, "9"),
	}
	c, err := CompareReviewedCase(compareCohort, inputs, 3*time.Second)
	want := CaseComparison{
		All: CaseComparisonStats{
			Summary:              Summary{Totals: Totals{Cases: 9, Passed: 3, Failed: 3, Errors: 1, Skipped: 2}, Timed: 6, Mean: 2500 * time.Millisecond, P95: 5 * time.Second},
			TotalTimedDuration:   15 * time.Second,
			FailureAmongPassFail: Frequency{3, 6}, ErrorAmongPassFailError: Frequency{1, 7}, SlowAmongTimed: Frequency{3, 6},
		},
		First: CaseComparisonStats{
			Summary:              Summary{Totals: Totals{Cases: 5, Passed: 1, Failed: 2, Errors: 1, Skipped: 1}, Timed: 3, Mean: 3_333_333_333, P95: 5 * time.Second},
			TotalTimedDuration:   10 * time.Second,
			FailureAmongPassFail: Frequency{2, 3}, ErrorAmongPassFailError: Frequency{1, 4}, SlowAmongTimed: Frequency{2, 3},
		},
		Repeat: CaseComparisonStats{
			Summary:              Summary{Totals: Totals{Cases: 4, Passed: 2, Failed: 1, Skipped: 1}, Timed: 3, Mean: 1_666_666_666, P95: 4 * time.Second},
			TotalTimedDuration:   5 * time.Second,
			FailureAmongPassFail: Frequency{1, 3}, ErrorAmongPassFailError: Frequency{0, 3}, SlowAmongTimed: Frequency{1, 3},
		},
		DistinctRuns: 6, DistinctRunsWithRepeat: 3, IndependentContrast: true, RecoveryAmongRetestedFailures: Frequency{1, 1},
	}
	if err != nil || !reflect.DeepEqual(c, want) {
		t.Fatalf("unqualified comparison: %+v %v", c, err)
	}
	for _, drop := range []string{"synthetic-private", compareCohort.ShardKey, compareCohort.Source, "ArtifactID", "JobID", "Occurrence"} {
		if strings.Contains(fmt.Sprintf("%+v", c), drop) {
			t.Fatal("comparison retained uncontrolled input or identity")
		}
	}
	for i, j := 0, len(inputs)-1; i < j; i, j = i+1, j-1 {
		inputs[i], inputs[j] = inputs[j], inputs[i]
	}
	again, err := CompareReviewedCase(compareCohort, inputs, 3*time.Second)
	if err != nil || again != c {
		t.Fatalf("review input order changed comparison: %+v %v", again, err)
	}
}

func TestReviewedCaseRetryRecoveryDoesNotBecomeIndependentContrast(t *testing.T) {
	failure := reviewedTestCase(t, 1, 1, 0, 201, Failure, "")
	pass := reviewedTestCase(t, 1, 2, 0, 202, Pass, "")
	otherRetry := reviewedTestCase(t, 2, 2, 1, 203, Pass, "")
	got, err := CompareReviewedCase(compareCohort, []ReviewedCaseObservation{otherRetry, pass, failure}, 0)
	if err != nil || got.IndependentContrast || got.DistinctRuns != 2 || got.DistinctRunsWithRepeat != 2 ||
		got.First.FailureAmongPassFail != (Frequency{1, 1}) || got.RecoveryAmongRetestedFailures != (Frequency{1, 1}) ||
		got.All.SlowAmongTimed.Denominator != 0 || got.All.Timed != 0 || got.All.TotalTimedDuration != 0 {
		t.Fatalf("retry promoted to independent contrast or invented timing: %+v %v", got, err)
	}
}

func TestReviewedCaseRejectsDifferentExactEntryDespiteSameArtifactAndCase(t *testing.T) {
	in := reviewedTestCase(t, 1, 1, 0, 201, Pass, "1")
	if _, err := CompareReviewedCase(compareCohort, []ReviewedCaseObservation{in}, 0); err != nil {
		t.Fatal(err)
	}
	// This exact XML entry yields the same anonymous projection with the
	// same artifact, based on the same binding and the same ordinal.
	raw := []byte(`<testsuite><testcase name="different-private"/><testcase name="synthetic-private" time="1"></testcase></testsuite>`)
	other, err := ParseJUnit(raw, in.Report.Binding())
	if err != nil || !reflect.DeepEqual(other.Cases(), in.Report.Cases()) {
		t.Fatalf("fixture differs in more than the source bytes: %v", err)
	}
	in.Report = other
	assertNoComparison(t, compareCohort, []ReviewedCaseObservation{in})
	in.Review.ExpectedReportSHA256 = sha256.Sum256([]byte("unrelated enclosing archive bytes"))
	assertNoComparison(t, compareCohort, []ReviewedCaseObservation{in})
	in.Review.ExpectedReportSHA256 = sha256.Sum256(raw)
	raw[0] = 'X' // Parsed identity is based on the retained proof, not caller bytes.
	if _, err := CompareReviewedCase(compareCohort, []ReviewedCaseObservation{in}, 0); err != nil {
		t.Fatalf("exact independently reviewed replacement denied: %v", err)
	}
	if serialized, err := json.Marshal(in.Report); err != nil || string(serialized) != "{}" {
		t.Fatal("private report proof escaped serialization")
	}
	expected := in.Review.ExpectedReportSHA256
	for _, v := range []string{fmt.Sprintf("%#v", in.Report), fmt.Sprintf("%+v", in.Report), fmt.Sprintf("%x", in.Report)} {
		if strings.Contains(v, hex.EncodeToString(expected[:])) || v != "JUnit report: 2 cases" {
			t.Fatal("private content identity escaped formatting")
		}
	}
}

func TestReviewedCaseRejectsForeignAmbiguousOrUnprovenInput(t *testing.T) {
	for name, mutate := range map[string]func(*ReviewedCaseObservation){
		"absent external review":            func(x *ReviewedCaseObservation) { x.Review = nil },
		"absent separately reviewed digest": func(x *ReviewedCaseObservation) { x.Review.ExpectedReportSHA256 = [sha256.Size]byte{} },
		"case ID absent":                    func(x *ReviewedCaseObservation) { x.Review.Cohort.CaseID = 0 },
		"different canonical case":          func(x *ReviewedCaseObservation) { x.Review.Cohort.CaseID++ },
		"different config":                  func(x *ReviewedCaseObservation) { x.Review.Cohort.ConfigurationID++ },
		"different revision":                func(x *ReviewedCaseObservation) { x.Review.Cohort.RevisionID++ },
		"different class":                   func(x *ReviewedCaseObservation) { x.Review.Cohort.RunnerClassID++ },
		"different shard ID":                func(x *ReviewedCaseObservation) { x.Review.Cohort.ShardID++ },
		"different shard key":               func(x *ReviewedCaseObservation) { x.Review.Cohort.ShardKey += "_other" },
		"report shard unknown":              func(x *ReviewedCaseObservation) { x.Report.binding.Shard = "" },
		"different retry":                   func(x *ReviewedCaseObservation) { x.Review.Retry++ },
		"report retry unknown":              func(x *ReviewedCaseObservation) { x.Report.binding.Retry = nil },
		"different artifact":                func(x *ReviewedCaseObservation) { x.Review.ArtifactID++ },
		"run-only report":                   func(x *ReviewedCaseObservation) { x.Report.binding.Job = nil },
		"different job":                     func(x *ReviewedCaseObservation) { x.Review.Job.JobID++ },
		"different attempt":                 func(x *ReviewedCaseObservation) { x.Review.Job.Attempt.Attempt++ },
		"different source":                  func(x *ReviewedCaseObservation) { x.Review.Job.Attempt.Source = "fixture_elsewhere" },
		"occurrence absent":                 func(x *ReviewedCaseObservation) { x.Review.Occurrence = 0 },
		"occurrence unknown":                func(x *ReviewedCaseObservation) { x.Review.Occurrence++ },
	} {
		t.Run(name, func(t *testing.T) {
			known := reviewedTestCase(t, 1, 1, 0, 201, Pass, "1")
			unqualified := reviewedTestCase(t, 2, 1, 0, 202, Failure, "2")
			mutate(&unqualified)
			assertNoComparison(t, compareCohort, []ReviewedCaseObservation{known, unqualified})
		})
	}
	known := reviewedTestCase(t, 1, 1, 0, 201, Pass, "1")
	duplicateSlot := reviewedTestCase(t, 1, 1, 0, 202, Failure, "1")
	duplicateArtifact := reviewedTestCase(t, 1, 2, 0, 201, Pass, "1")
	assertNoComparison(t, compareCohort, []ReviewedCaseObservation{known, known})
	assertNoComparison(t, compareCohort, []ReviewedCaseObservation{known, duplicateSlot})
	assertNoComparison(t, compareCohort, []ReviewedCaseObservation{known, duplicateArtifact})
	assertNoComparison(t, compareCohort, nil)
	assertNoComparison(t, compareCohort, make([]ReviewedCaseObservation, MaxComparisonObservations+1))
	if _, err := CompareReviewedCase(compareCohort, []ReviewedCaseObservation{known}, MaxDuration+1); err != ErrCaseComparison {
		t.Fatal("unauthorized comparison threshold accepted")
	}
}

func assertNoComparison(t *testing.T, cohort ReviewedCaseCohort, got []ReviewedCaseObservation) {
	t.Helper()
	if view, err := CompareReviewedCase(cohort, got, 0); view != (CaseComparison{}) || err != ErrCaseComparison {
		t.Fatalf("invalid input yielded partial comparison: %+v %v", view, err)
	}
}
