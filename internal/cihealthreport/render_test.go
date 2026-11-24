package cihealthreport

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func storedFixturePublication(t *testing.T) StoredPublication {
	attempt := fixtureAttempt("fixture_source", 10, 11, 12, 1)
	return StoredPublication{
		Scope:      cihealth.HistoryScope{Source: "fixture_source", RepositoryID: 10, WorkflowID: 11},
		Attempt:    attempt,
		ReceivedAt: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		Reports: []StoredReport{
			{
				ArtifactID: 502, JobID: 9002, Shard: "integration", Retry: 0,
				ArchiveSHA256: fixtureArchiveSHA, EntrySHA256: fixtureEntrySHA,
				Totals: Totals{Cases: 1, Passed: 1}, Timed: 1, Mean: 3 * time.Second, P95: 3 * time.Second,
				TotalTimedDuration: 3 * time.Second,
			},
			{
				ArtifactID: 501, JobID: 9001, Shard: "unit", Retry: 1,
				ArchiveSHA256: fixtureArchiveSHA, EntrySHA256: fixtureEntrySHA,
				Totals: Totals{Cases: 2, Passed: 2}, Timed: 2, Mean: 1500 * time.Millisecond, P95: 2 * time.Second,
				TotalTimedDuration: 3 * time.Second,
			},
			{
				ArtifactID: 500, JobID: 9001, Shard: "unit", Retry: 0,
				ArchiveSHA256: fixtureArchiveSHA, EntrySHA256: fixtureEntrySHA,
				Totals: Totals{Cases: 2, Passed: 1, Failed: 1}, Timed: 2, Mean: 500 * time.Millisecond, P95: 1 * time.Second,
				TotalTimedDuration: 1 * time.Second,
			},
		},
		Metadata: []StoredMetadata{{ArtifactID: 503, ArchiveSHA256: fixtureArchiveSHA}},
	}
}

func TestRenderPublicationIsDeterministicAndExact(t *testing.T) {
	stored := storedFixturePublication(t)
	expected := []ExpectedShardSlot{{Shard: "unit", Retry: 0}, {Shard: "unit", Retry: 1}, {Shard: "integration", Retry: 0}}
	first, err := RenderPublication(stored, expected)
	if err != nil {
		t.Fatal(err)
	}
	// Scramble row order; the rendered view must not change.
	stored.Reports[0], stored.Reports[1], stored.Reports[2] = stored.Reports[2], stored.Reports[0], stored.Reports[1]
	again, err := RenderPublication(stored, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("input order changed the rendered view:\nfirst=%+v\nagain=%+v", first, again)
	}
	if first.Link != (ReportLink{Source: "fixture_source", RepositoryID: 10, WorkflowID: 11, RunID: 12, Attempt: 1}) {
		t.Fatalf("link must omit artifact/job at publication level: %+v", first.Link)
	}
	for i, id := range []int64{500, 501, 502} {
		if first.Reports[i].Link.ArtifactID != id || first.Reports[i].Link.JobID == 0 {
			t.Fatalf("report rows must be ordered by artifact with exact links: %+v", first.Reports[i])
		}
	}
}

func TestRenderPublicationSeparatesInitialFailureFromEventualSuccess(t *testing.T) {
	view, err := RenderPublication(storedFixturePublication(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Initial covers every first execution (retry 0) across shards: the unit
	// shard's failed first run and the integration shard's clean first run.
	// Mean is exact: 4s total over 3 timed initial cases, 3s over 2 retry cases.
	if view.Initial.Totals != (Totals{Cases: 3, Passed: 2, Failed: 1}) || view.Initial.Timed != 3 ||
		view.Initial.Mean != 4*time.Second/3 || view.Initial.FailureAmongPassFail != (Frequency{1, 3}) {
		t.Fatalf("initial execution sample wrong: %+v", view.Initial)
	}
	if view.Retry.Totals != (Totals{Cases: 2, Passed: 2}) || view.Retry.Mean != 1500*time.Millisecond ||
		view.Retry.FailureAmongPassFail != (Frequency{0, 2}) {
		t.Fatalf("retry sample must show the eventual pass: %+v", view.Retry)
	}
	if view.All.Totals != (Totals{Cases: 5, Passed: 4, Failed: 1}) || view.All.Mean != 7*time.Second/5 ||
		view.All.FailureAmongPassFail != (Frequency{1, 5}) || view.All.ErrorAmongPassFailError != (Frequency{0, 5}) {
		t.Fatalf("aggregate sample must combine initial and retry exactly: %+v", view.All)
	}
}

func TestRenderPublicationMakesPartialCoverageVisible(t *testing.T) {
	view, err := RenderPublication(storedFixturePublication(t), []ExpectedShardSlot{
		{Shard: "unit", Retry: 0}, {Shard: "unit", Retry: 1}, {Shard: "integration", Retry: 0},
		{Shard: "integration", Retry: 1}, {Shard: "e2e", Retry: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	coverage := view.Coverage
	if coverage.DeclaredArtifacts != 4 || coverage.ProvedArtifacts != 3 || coverage.MetadataArtifacts != 1 {
		t.Fatalf("artifact coverage wrong: %+v", coverage)
	}
	want := []ShardSlotCoverage{
		{Shard: "e2e", Retry: 0, Missing: true},
		{Shard: "integration", Retry: 0, Proved: true},
		{Shard: "integration", Retry: 1, Missing: true},
		{Shard: "unit", Retry: 0, Proved: true},
		{Shard: "unit", Retry: 1, Proved: true},
	}
	if !reflect.DeepEqual(coverage.Slots, want) {
		t.Fatalf("slot coverage must mark missing without outcomes: %+v", coverage.Slots)
	}
	for _, slot := range coverage.Slots {
		if slot.Missing && slot.Proved {
			t.Fatalf("missing slot also marked proved: %+v", slot)
		}
	}
}

func TestRenderPublicationLabelsMetadataWithoutJobEvidence(t *testing.T) {
	stored := storedFixturePublication(t)
	stored.Reports = nil
	view, err := RenderPublication(stored, nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.JobLevelEvidence || len(view.Reports) != 0 || len(view.Metadata) != 1 {
		t.Fatalf("metadata-only publication must publish no test results: %+v", view)
	}
	row := view.Metadata[0]
	if !row.RunMetadataOnly || row.Link.JobID != 0 || row.Link.ArtifactID != 503 {
		t.Fatalf("metadata row must be explicitly labelled without a job link: %+v", row)
	}
	if view.All.FailureAmongPassFail.Denominator != 0 || view.All.ErrorAmongPassFailError.Denominator != 0 {
		t.Fatalf("metadata-only publication must report insufficient observations, not zero rates: %+v", view.All)
	}
}

func TestRenderPublicationRejectsUnusableStoredData(t *testing.T) {
	stored := storedFixturePublication(t)
	stored.ReceivedAt = time.Time{}
	if _, err := RenderPublication(stored, nil); err == nil {
		t.Fatal("unreceipted publication rendered")
	}
	stored = storedFixturePublication(t)
	stored.Scope.RepositoryID = 0
	if _, err := RenderPublication(stored, nil); err == nil {
		t.Fatal("out-of-scope publication rendered")
	}
}

func TestRenderPublicationNeverExposesPinsOrReportContent(t *testing.T) {
	view, err := RenderPublication(storedFixturePublication(t), []ExpectedShardSlot{{Shard: "unit", Retry: 0}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{
		fmt.Sprintf("%x", fixtureArchiveSHA), fmt.Sprintf("%x", fixtureEntrySHA),
		"fixture-private", "<testsuite", "ArchiveSHA256", "EntrySHA256", "http",
	} {
		if strings.Contains(string(encoded), prohibited) {
			t.Fatalf("rendered view exposed %q", prohibited)
		}
	}
}

func TestRenderCaseRankingsDeterministicAcrossCategories(t *testing.T) {
	cohortA := compareCohort
	cohortB := compareCohort
	cohortB.RevisionID = 77 // same display identity, incompatible category
	inputsA := []ReviewedCaseImpactInput{{
		Cohort: cohortA,
		Observations: []ReviewedCaseObservation{
			reviewedTestCase(t, 1, 1, 0, 301, Failure, "3"),
			reviewedTestCase(t, 2, 1, 0, 302, Pass, "1"),
		},
	}}
	ranked, err := RenderCaseRankings(inputsA)
	if err != nil || len(ranked) != 1 || ranked[0].Rank != 1 || ranked[0].CaseID != cohortA.CaseID {
		t.Fatalf("ranking render wrong: %+v %v", ranked, err)
	}
	// Shuffled input order must not change the output.
	inputsShuffled := []ReviewedCaseImpactInput{{
		Cohort: cohortA,
		Observations: []ReviewedCaseObservation{
			reviewedTestCase(t, 2, 1, 0, 302, Pass, "1"),
			reviewedTestCase(t, 1, 1, 0, 301, Failure, "3"),
		},
	}}
	again, err := RenderCaseRankings(inputsShuffled)
	if err != nil || !reflect.DeepEqual(ranked, again) {
		t.Fatalf("ranking input order changed the output: %+v %v", again, err)
	}
	// The same numeric case identity under a different category must not merge.
	if _, err := RenderCaseRankings(append(inputsA, ReviewedCaseImpactInput{
		Cohort:       cohortB,
		Observations: []ReviewedCaseObservation{reviewedTestCase(t, 3, 1, 0, 303, Pass, "1")},
	})); err == nil {
		t.Fatal("incompatible cohorts merged in one ranking")
	}
	// Unverifiable input refuses instead of guessing.
	if _, err := RenderCaseRankings(nil); err == nil {
		t.Fatal("empty ranking input must refuse")
	}
}
