package cihealthreport

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

// TestEndToEndReportToRanking proves the full trusted chain: reviewed
// manifests and exact bytes authorize ingestion, retention keeps per-case
// observations with provenance, and rankings plus retry histories render
// deterministically from the retained store.
func TestEndToEndReportToRanking(t *testing.T) {
	run12 := fixtureAttempt("fixture_source", 10, 11, 12, 1)
	run13 := fixtureAttempt("fixture_source", 10, 11, 13, 1)
	scope := fixtureScope{run12: true, run13: true}

	manifest12, archives12 := fixtureManifest(t, run12,
		fixtureArtifact{
			id: 600, job: 9101, shard: "unit",
			xml:   []byte(`<testsuite><testcase name="a" time="2"><failure/></testcase><testcase name="b" time="1"/></testsuite>`),
			cases: []ReportManifestCase{{Occurrence: 1, CaseID: 41}, {Occurrence: 2, CaseID: 42}},
		},
		fixtureArtifact{
			id: 601, job: 9101, shard: "unit", retry: 1,
			xml:   []byte(`<testsuite><testcase name="a" time="3"/></testsuite>`),
			cases: []ReportManifestCase{{Occurrence: 1, CaseID: 41}},
		},
		fixtureArtifact{id: 602}, // run-only metadata must never rank
	)
	manifest13, archives13 := fixtureManifest(t, run13,
		fixtureArtifact{
			id: 700, job: 9201, shard: "unit",
			xml:   []byte(`<testsuite><testcase name="a" time="4"/><testcase name="b" time="2"/></testsuite>`),
			cases: []ReportManifestCase{{Occurrence: 1, CaseID: 41}, {Occurrence: 2, CaseID: 42}},
		},
	)

	store, err := OpenPublicationStore(filepath.Join(t.TempDir(), "publications.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, step := range []struct {
		manifest ReportManifest
		archives []ReportArchive
	}{
		{manifest12, archives12},
		{manifest13, archives13},
	} {
		ingested, err := IngestReports(t.Context(), IngestInput{Scope: scope, Manifest: step.manifest, Archives: step.archives})
		if err != nil {
			t.Fatal(err)
		}
		pub, err := NewPublication(ingested)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SavePublication(t.Context(), pub); err != nil {
			t.Fatal(err)
		}
	}

	scopeKey := cihealth.HistoryScope{Source: "fixture_source", RepositoryID: 10, WorkflowID: 11}
	pubs, _, err := store.LoadScopePublications(t.Context(), scopeKey)
	if err != nil || len(pubs) != 2 {
		t.Fatalf("scope read must return both retained publications: %+v %v", pubs, err)
	}

	ranked, err := RankStoredCases(pubs)
	if err != nil || len(ranked) != 2 {
		t.Fatalf("ranking must cover both bound cases: %+v %v", ranked, err)
	}
	if ranked[0].CaseID != 41 || ranked[0].Rank != 1 || ranked[0].Failures != 1 || ranked[0].Observations != 3 || ranked[0].Runs != 2 {
		t.Fatalf("case 41 must rank first with its single failure across two runs: %+v", ranked[0])
	}
	if ranked[1].CaseID != 42 || ranked[1].Failures != 0 || ranked[1].Observations != 2 {
		t.Fatalf("case 42 must rank second, clean: %+v", ranked[1])
	}

	// Input order must not change the ranking.
	shuffled := []StoredPublication{pubs[1], pubs[0]}
	again, err := RankStoredCases(shuffled)
	if err != nil || !reflect.DeepEqual(ranked, again) {
		t.Fatalf("ranking input order changed the output: %+v %v", again, err)
	}

	histories, err := CaseHistories(pubs)
	if err != nil || len(histories) != 2 {
		t.Fatalf("histories must cover both bound cases: %+v %v", histories, err)
	}
	if histories[0].CaseID != 41 || !histories[0].FailedFirstExecution || !histories[0].EventualPass {
		t.Fatalf("case 41 history must show failed first execution and eventual pass: %+v", histories[0])
	}
	events := histories[0].Events
	if len(events) != 3 || events[0].Link.RunID != 12 || events[0].Retry != 0 || events[0].Outcome != Failure ||
		events[1].Link.RunID != 12 || events[1].Retry != 1 || events[1].Outcome != Pass ||
		events[2].Link.RunID != 13 || events[2].Outcome != Pass {
		t.Fatalf("case 41 events out of order: %+v", events)
	}
	if histories[1].CaseID != 42 || histories[1].FailedFirstExecution {
		t.Fatalf("case 42 history wrong: %+v", histories[1])
	}
	// Metadata-only input contributes no case observations anywhere.
	for _, row := range histories {
		for _, event := range row.Events {
			if event.Link.ArtifactID == 602 {
				t.Fatalf("run-only metadata leaked into case history: %+v", event)
			}
		}
	}

	// A tampered third run breaks the chain at ingestion and publishes nothing.
	run14 := fixtureAttempt("fixture_source", 10, 11, 14, 1)
	manifest14, archives14 := fixtureManifest(t, run14, fixtureArtifact{
		id: 800, job: 9301, shard: "unit",
		xml:   []byte(`<testsuite><testcase name="a"><failure/></testcase></testsuite>`),
		cases: []ReportManifestCase{{Occurrence: 1, CaseID: 41}},
	})
	archives14[0].Bytes[0] ^= 0xff
	if _, err := IngestReports(t.Context(), IngestInput{Scope: scope, Manifest: manifest14, Archives: archives14}); err == nil {
		t.Fatal("tampered archive must break the chain at ingestion")
	}
	pubs, _, err = store.LoadScopePublications(t.Context(), scopeKey)
	if err != nil || len(pubs) != 2 {
		t.Fatalf("failed ingestion must not change retained state: %+v %v", pubs, err)
	}
}

func TestRankStoredCasesRefusesMixedOrEmptyInput(t *testing.T) {
	if _, err := RankStoredCases(nil); err == nil {
		t.Fatal("empty ranking input must refuse")
	}
	if _, err := CaseHistories(nil); err == nil {
		t.Fatal("empty history input must refuse")
	}
	one := fixtureAttempt("fixture_source", 10, 11, 12, 1)
	two := fixtureAttempt("fixture_source", 10, 12, 12, 1) // different workflow scope
	pubs := []StoredPublication{
		storedPubWithCase(t, one, 500, 9001, 41, Failure),
		storedPubWithCase(t, two, 600, 9002, 41, Pass),
	}
	if _, err := RankStoredCases(pubs); err == nil {
		t.Fatal("mixed workflow scopes must refuse, not merge")
	}
	if _, err := CaseHistories(pubs); err == nil {
		t.Fatal("mixed workflow scopes must refuse histories too")
	}
}

func TestRankStoredCasesEnforcesObservationCeiling(t *testing.T) {
	attempt := fixtureAttempt("fixture_source", 10, 11, 12, 1)
	half := MaxComparisonObservations/2 + 1
	var pubs []StoredPublication
	for _, run := range []int64{12, 13} {
		a := cihealth.AttemptKey{Source: attempt.Source, RepositoryID: attempt.RepositoryID, WorkflowID: attempt.WorkflowID, RunID: run, Attempt: 1}
		pub := StoredPublication{
			Scope:   cihealth.HistoryScope{Source: a.Source, RepositoryID: a.RepositoryID, WorkflowID: a.WorkflowID},
			Attempt: a, ReceivedAt: time.Now().UTC(),
			Reports: []StoredReport{{
				ArtifactID: 500 + run, JobID: 9001, Shard: "unit",
				ArchiveSHA256: fixtureArchiveSHA, EntrySHA256: fixtureEntrySHA,
				Totals: Totals{Cases: half, Passed: half},
				Cases:  manyCases(half),
			}},
		}
		pubs = append(pubs, pub)
	}
	if _, err := RankStoredCases(pubs); err == nil {
		t.Fatal("observations beyond the comparison ceiling must refuse")
	}
}

func manyCases(n int) []StoredCaseObservation {
	out := make([]StoredCaseObservation, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, StoredCaseObservation{CaseID: int64(1000 + i), Occurrence: i, Outcome: Pass, Timed: true, Duration: time.Second})
	}
	return out
}

func storedPubWithCase(t *testing.T, attempt cihealth.AttemptKey, artifact, job, caseID int64, outcome Outcome) StoredPublication {
	t.Helper()
	return StoredPublication{
		Scope:      cihealth.HistoryScope{Source: attempt.Source, RepositoryID: attempt.RepositoryID, WorkflowID: attempt.WorkflowID},
		Attempt:    attempt,
		ReceivedAt: time.Now().UTC(),
		Reports: []StoredReport{{
			ArtifactID: artifact, JobID: job, Shard: "unit",
			ArchiveSHA256: fixtureArchiveSHA, EntrySHA256: fixtureEntrySHA,
			Totals: Totals{Cases: 1, Passed: 1},
			Cases:  []StoredCaseObservation{{CaseID: caseID, Occurrence: 1, Outcome: outcome, Timed: true, Duration: time.Second}},
		}},
	}
}

func TestLiveDisplayStaysGated(t *testing.T) {
	for _, scope := range []ReportScope{nil, fixtureScope{fixtureAttemptKey: true}} {
		availability := CheckLiveAvailability(scope, fixtureAttemptKey)
		if availability.Available || availability.Reason != liveReasonProducerTrust {
			t.Fatalf("live display must stay gated on producer trust: %+v", availability)
		}
	}
	view, err := RenderPublication(storedFixturePublication(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.Live.Available || view.Live.Reason != liveReasonProducerTrust {
		t.Fatalf("rendered view must carry the live gate label: %+v", view.Live)
	}
}
