package cihealthreport

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

var setAttempt = cihealth.AttemptKey{Source: "fixture_source", RepositoryID: 1, WorkflowID: 2, RunID: 3, Attempt: 2}

func reviewedSetFixture(t *testing.T, attempt cihealth.AttemptKey, artifact, job int64, shard string, retry uint16, raw string) (ReviewedAttemptEntry, Report) {
	t.Helper()
	key := cihealth.JobKey{Attempt: attempt, JobID: job}
	entry := ReviewedAttemptEntry{ArtifactID: artifact, JobID: job, Shard: shard, Retry: retry, XMLSHA256: sha256.Sum256([]byte(raw))}
	binding := ArtifactBinding{Attempt: attempt, ArtifactID: artifact, Job: &key, Shard: shard, Retry: &retry}
	report, err := ParseJUnit([]byte(raw), binding)
	if err != nil {
		t.Fatal(err)
	}
	return entry, report
}

func assertNoReviewedAttemptSet(t *testing.T, attempt cihealth.AttemptKey, expected []ReviewedAttemptEntry, reports []Report) {
	t.Helper()
	if got, err := MatchReviewedAttemptReports(attempt, expected, reports); err != ErrReviewedAttemptReportSet || got != (ReviewedAttemptReportTotals{}) {
		t.Fatalf("mismatched set escaped: %+v %v", got, err)
	}
}

func TestReviewedAttemptSetCountsOccurrencesAndSeparatesRetries(t *testing.T) {
	same := `<testsuite><testcase name="common"/><testcase name="failed"><failure/></testcase></testsuite>`
	retry := `<testsuite><testcase name="failed"/><testcase name="skipped"><skipped/></testcase></testsuite>`
	a, x := reviewedSetFixture(t, setAttempt, 10, 20, "shard_a", 0, same)
	b, y := reviewedSetFixture(t, setAttempt, 11, 20, "shard_b", 0, same)
	c, z := reviewedSetFixture(t, setAttempt, 12, 20, "shard_a", 1, retry)
	got, err := MatchReviewedAttemptReports(setAttempt, []ReviewedAttemptEntry{c, b, a}, []Report{y, z, x})
	want := ReviewedAttemptReportTotals{
		Initial: Totals{Cases: 4, Passed: 2, Failed: 2}, Retry: Totals{Cases: 2, Passed: 1, Skipped: 1},
		InitialArtifacts: 2, RetryArtifacts: 1,
	}
	if err != nil || got != want {
		t.Fatalf("got %+v %v; want %+v", got, err, want)
	}
}

func TestReviewedAttemptSetRejectsMissingUnexpectedDuplicateAndForeignEvidence(t *testing.T) {
	raw := `<testsuite><testcase name="fixture"/></testsuite>`
	a, x := reviewedSetFixture(t, setAttempt, 10, 20, "shard_a", 0, raw)
	b, y := reviewedSetFixture(t, setAttempt, 11, 21, "shard_b", 0, raw)
	good := []ReviewedAttemptEntry{a, b}
	_, unexpected := reviewedSetFixture(t, setAttempt, 12, 21, "shard_b", 0, raw)
	assertNoReviewedAttemptSet(t, setAttempt, good, []Report{x})
	assertNoReviewedAttemptSet(t, setAttempt, []ReviewedAttemptEntry{a}, []Report{x, y})
	assertNoReviewedAttemptSet(t, setAttempt, good, []Report{x, unexpected})
	assertNoReviewedAttemptSet(t, setAttempt, good, []Report{x, x})
	assertNoReviewedAttemptSet(t, setAttempt, []ReviewedAttemptEntry{a, a}, []Report{x, y})
	duplicateSlot, wrongSlot := reviewedSetFixture(t, setAttempt, 11, 21, "shard_a", 0, raw)
	assertNoReviewedAttemptSet(t, setAttempt, []ReviewedAttemptEntry{a, duplicateSlot}, []Report{x, wrongSlot})
	for _, change := range []func(*ReviewedAttemptEntry){
		func(v *ReviewedAttemptEntry) { v.JobID++ },
		func(v *ReviewedAttemptEntry) { v.Shard = "shard_c" },
		func(v *ReviewedAttemptEntry) { v.Retry++ },
		func(v *ReviewedAttemptEntry) { v.XMLSHA256[0] ^= 1 },
		func(v *ReviewedAttemptEntry) { v.XMLSHA256 = [sha256.Size]byte{} },
	} {
		changed := b
		change(&changed)
		assertNoReviewedAttemptSet(t, setAttempt, []ReviewedAttemptEntry{a, changed}, []Report{x, y})
	}
	for _, change := range []func(*cihealth.AttemptKey){
		func(k *cihealth.AttemptKey) { k.Source = "other" },
		func(k *cihealth.AttemptKey) { k.RepositoryID++ },
		func(k *cihealth.AttemptKey) { k.WorkflowID++ },
		func(k *cihealth.AttemptKey) { k.RunID++ },
		func(k *cihealth.AttemptKey) { k.Attempt++ },
	} {
		foreign := setAttempt
		change(&foreign)
		_, outside := reviewedSetFixture(t, foreign, 11, 21, "shard_b", 0, raw)
		assertNoReviewedAttemptSet(t, setAttempt, good, []Report{x, outside})
	}
	for _, change := range []func(*ArtifactBinding){
		func(v *ArtifactBinding) { v.Job = nil },
		func(v *ArtifactBinding) { v.Shard = "" },
		func(v *ArtifactBinding) { v.Retry = nil },
	} {
		unknown := y.Binding()
		change(&unknown)
		report, err := ParseJUnit([]byte(raw), unknown)
		if err != nil {
			t.Fatal(err)
		}
		assertNoReviewedAttemptSet(t, setAttempt, good, []Report{x, report})
	}
	assertNoReviewedAttemptSet(t, setAttempt, nil, nil)
}

func TestReviewedAttemptSetBoundsAndKeepsKnownEmptyReports(t *testing.T) {
	var expected []ReviewedAttemptEntry
	var observed []Report
	for i := 1; i <= MaxReviewedAttemptReports+1; i++ {
		entry, report := reviewedSetFixture(t, setAttempt, int64(i), 20, fmt.Sprintf("fixture_%d", i), 0, `<testsuite tests="0"/>`)
		expected, observed = append(expected, entry), append(observed, report)
	}
	totals, err := MatchReviewedAttemptReports(setAttempt, expected[:MaxReviewedAttemptReports], observed[:MaxReviewedAttemptReports])
	if err != nil || totals != (ReviewedAttemptReportTotals{InitialArtifacts: MaxReviewedAttemptReports}) {
		t.Fatalf("known empty measured reports lost: %+v %v", totals, err)
	}
	assertNoReviewedAttemptSet(t, setAttempt, expected, observed)
}

func TestReviewedAttemptSetMatchesExactExtractedEntryNotArchiveDigest(t *testing.T) {
	entry, parsed := reviewedSetFixture(t, setAttempt, 10, 20, "shard_a", 0, string(zipTestXML))
	archive := buildJUnitZIP(t, fixtureZIPEntry(zipTestName, zipTestXML))
	zipReview := zipFixtureReview(archive, zipTestName, zipTestXML)
	zipReview.Binding = parsed.Binding()
	observed, err := ParseReviewedJUnitZIP(archive, zipReview)
	if err != nil {
		t.Fatal(err)
	}
	got, err := MatchReviewedAttemptReports(setAttempt, []ReviewedAttemptEntry{entry}, []Report{observed})
	if err != nil || got != (ReviewedAttemptReportTotals{Initial: Totals{Cases: 1, Passed: 1}, InitialArtifacts: 1}) {
		t.Fatalf("exact extracted XML did not match: %+v %v", got, err)
	}
	entry.XMLSHA256 = sha256.Sum256(archive)
	assertNoReviewedAttemptSet(t, setAttempt, []ReviewedAttemptEntry{entry}, []Report{observed})
}
