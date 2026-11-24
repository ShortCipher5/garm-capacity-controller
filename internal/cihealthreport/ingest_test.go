package cihealthreport

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

var fixtureAttemptKey = fixtureAttempt("fixture_source", 10, 11, 12, 1)

func TestIngestAuthorizedFixtureProvesShardAndRetryReports(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey,
		fixtureArtifact{id: 500, job: 9001, shard: "unit", retry: 0,
			xml: []byte(`<testsuite><testcase name="a" time="1"/><testcase name="b"><failure/></testcase></testsuite>`)},
		fixtureArtifact{id: 501, job: 9001, shard: "unit", retry: 1,
			xml: []byte(`<testsuite><testcase name="a" time="2"/><testcase name="b"/></testsuite>`)},
		fixtureArtifact{id: 502, job: 9002, shard: "integration", retry: 0,
			xml: []byte(`<testsuite><testcase name="c" time="3"/></testsuite>`)},
		fixtureArtifact{id: 503},
	)
	ingested, err := IngestReports(t.Context(), IngestInput{
		Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: manifest, Archives: archives,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ingested.Proved) != 3 || len(ingested.Metadata) != 1 {
		t.Fatalf("proved/metadata split wrong: %+v", ingested)
	}
	first, retry, other := ingested.Proved[0], ingested.Proved[1], ingested.Proved[2]
	if first.Provenance.ArtifactID != 500 || first.Provenance.Shard != "unit" || *first.Provenance.Retry != 0 ||
		first.Report.Summary().Totals != (Totals{Cases: 2, Passed: 1, Failed: 1}) {
		t.Fatalf("initial execution misread: %+v", first)
	}
	if retry.Provenance.ArtifactID != 501 || retry.Provenance.Shard != "unit" || *retry.Provenance.Retry != 1 ||
		retry.Provenance.Job.JobID != 9001 || retry.Report.Summary().Passed != 2 {
		t.Fatalf("framework retry not distinguished from the initial execution: %+v", retry)
	}
	if other.Provenance.ArtifactID != 502 || other.Provenance.Shard != "integration" ||
		other.Provenance.Job.JobID != 9002 {
		t.Fatalf("shard reports merged: %+v", other)
	}
	metadata := ingested.Metadata[0]
	if metadata.Provenance.ArtifactID != 503 || metadata.Provenance.Level() != ProvenanceRunMetadata {
		t.Fatalf("run-only artifact misclassified: %+v", metadata)
	}
	pub, err := NewPublication(ingested)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Attempt != fixtureAttemptKey || len(pub.Reports) != 3 || len(pub.Metadata) != 1 ||
		pub.Scope != (cihealth.HistoryScope{Source: "fixture_source", RepositoryID: 10, WorkflowID: 11}) {
		t.Fatalf("publication lost provenance or retention scope: %+v", pub)
	}
}

func TestIngestRefusesRevokedOrMissingScope(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	for name, scope := range map[string]ReportScope{
		"revoked": fixtureScope{},
		"nil":     nil,
	} {
		ingested, err := IngestReports(t.Context(), IngestInput{Scope: scope, Manifest: manifest, Archives: archives})
		if err == nil || len(ingested.Proved) != 0 {
			t.Fatalf("%s scope published test-level results: %+v %v", name, ingested, err)
		}
		if !errors.Is(err, ErrReportIngest) {
			t.Fatalf("%s scope returned an unexpected error: %v", name, err)
		}
	}
}

func TestIngestScopeGateSeesExactAttempt(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	foreign := fixtureAttempt("fixture_source", 10, 11, 12, 2)
	scope := fixtureScope{foreign: true} // authorizes a different attempt only
	if _, err := IngestReports(t.Context(), IngestInput{Scope: scope, Manifest: manifest, Archives: archives}); err == nil {
		t.Fatal("ingestion authorized under a different attempt")
	}
}

func TestIngestRefusesArchiveSetMismatch(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey,
		fixtureArtifact{id: 500, job: 9001, shard: "unit"},
		fixtureArtifact{id: 501, job: 9002, shard: "unit", retry: 1},
	)
	for name, set := range map[string][]ReportArchive{
		"missing":   archives[:1],
		"extra":     append(append([]ReportArchive(nil), archives...), ReportArchive{ArtifactID: 599, Bytes: archives[0].Bytes}),
		"duplicate": {archives[0], archives[0]},
	} {
		ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: manifest, Archives: set})
		if err == nil || len(ingested.Proved) != 0 {
			t.Fatalf("%s archive set published test-level results: %+v %v", name, ingested, err)
		}
	}
}

func TestIngestRefusesDigestMismatch(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	tampered := append([]ReportArchive(nil), archives...)
	raw := append([]byte(nil), tampered[0].Bytes...)
	raw[len(raw)-1] ^= 0xff
	tampered[0] = ReportArchive{ArtifactID: 500, Bytes: raw}
	if ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: manifest, Archives: tampered}); err == nil || len(ingested.Proved) != 0 {
		t.Fatalf("tampered archive published test-level results: %+v %v", ingested, err)
	}

	// A manifest whose entry digest does not match the extracted member is
	// refused even when the archive digest matches.
	altered := manifest
	altered.Artifacts[0].EntrySHA256 = sha256.Sum256([]byte("different entry bytes"))
	if ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: altered, Archives: archives}); err == nil || len(ingested.Proved) != 0 {
		t.Fatalf("entry digest mismatch published test-level results: %+v %v", ingested, err)
	}
}

func TestIngestRefusesOversizedAndInvalidArchives(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	oversized := manifest
	oversized.Artifacts[0].ArchiveBytes = MaxJUnitZIPBytes + 1
	if oversized.valid() {
		t.Fatal("oversized archive claim passed manifest validation")
	}
	// Hand-built manifests are re-validated by ingestion, so an oversized
	// claim must refuse before any parsing.
	if ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: oversized, Archives: archives}); err == nil {
		t.Fatalf("oversized archive claim ingested: %+v", ingested)
	}

	// A correctly pinned but structurally invalid archive (not a ZIP at all)
	// must refuse through the reviewed ZIP framing checks.
	garbage := []byte("this is not a zip archive at all........")
	garbageManifest, garbageArchives := fixtureManifest(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit", xml: garbage})
	garbageManifest.Artifacts[0].ArchiveBytes = int64(len(garbage))
	garbageManifest.Artifacts[0].ArchiveSHA256 = sha256.Sum256(garbage)
	garbageArchives[0] = ReportArchive{ArtifactID: 500, Bytes: garbage}
	if ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: garbageManifest, Archives: garbageArchives}); err == nil || len(ingested.Proved) != 0 {
		t.Fatalf("invalid archive published test-level results: %+v %v", ingested, err)
	}
}

func TestIngestRefusesInvalidReportContent(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey,
		fixtureArtifact{id: 500, job: 9001, shard: "unit", xml: []byte(`<testsuite><testcase name="broken"`)})
	if ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: manifest, Archives: archives}); err == nil || len(ingested.Proved) != 0 {
		t.Fatalf("truncated report published test-level results: %+v %v", ingested, err)
	}
}

func TestIngestRefusesUnsafeMembers(t *testing.T) {
	// The manifest claims only the reviewed entry, but the archive carries an
	// additional unsafe member; the reviewed ZIP framing must reject the
	// whole archive rather than silently skip the member.
	reviewed := []byte(`<testsuite><testcase name="a"/></testsuite>`)
	archive := buildJUnitZIP(t,
		fixtureZIPEntry("reports/fixture.xml", reviewed),
		fixtureZIPEntry("../escape.xml", []byte("<escape/>")),
	)
	manifest := ReportManifest{
		Source: "fixture_source", RepositoryID: 10, WorkflowID: 11, RunID: 12, Attempt: 1,
		Artifacts: []ReportManifestArtifact{{
			ArtifactID: 500, ArchiveBytes: int64(len(archive)), ArchiveSHA256: sha256.Sum256(archive),
			Job:       &ReportManifestJob{JobID: 9001, Shard: "unit", Retry: 0},
			EntryName: "reports/fixture.xml", EntryBytes: int64(len(reviewed)), EntrySHA256: sha256.Sum256(reviewed),
		}},
	}
	ingested, err := IngestReports(t.Context(), IngestInput{
		Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: manifest, Archives: []ReportArchive{{ArtifactID: 500, Bytes: archive}},
	})
	if err == nil || len(ingested.Proved) != 0 {
		t.Fatalf("unsafe member archive published test-level results: %+v %v", ingested, err)
	}
}

func TestIngestRefusesCancelledContext(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if ingested, err := IngestReports(ctx, IngestInput{Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: manifest, Archives: archives}); err == nil || len(ingested.Proved) != 0 {
		t.Fatalf("cancelled ingestion published test-level results: %+v %v", ingested, err)
	}
}

func TestIngestMetadataRowsNeverParseReportXML(t *testing.T) {
	// The metadata archive is pinned exactly but contains no XML member at
	// all; ingestion must retain it as metadata without opening it.
	body := []byte("plain metadata payload, not a report")
	archive := buildJUnitZIP(t, fixtureZIPEntry("reports/notes.txt", body))
	manifest := ReportManifest{
		Source: "fixture_source", RepositoryID: 10, WorkflowID: 11, RunID: 12, Attempt: 1,
		Artifacts: []ReportManifestArtifact{{
			ArtifactID: 500, ArchiveBytes: int64(len(archive)), ArchiveSHA256: sha256.Sum256(archive),
		}},
	}
	ingested, err := IngestReports(t.Context(), IngestInput{
		Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: manifest, Archives: []ReportArchive{{ArtifactID: 500, Bytes: archive}},
	})
	if err != nil || len(ingested.Metadata) != 1 || len(ingested.Proved) != 0 {
		t.Fatalf("metadata row must ingest without report parsing: %+v %v", ingested, err)
	}
	pub, err := NewPublication(ingested)
	if err != nil || pub.Attempt != fixtureAttemptKey || len(pub.Reports) != 0 || len(pub.Metadata) != 1 {
		t.Fatalf("metadata publication wrong: %+v %v", pub, err)
	}
}

func TestNewPublicationRejectsTamperedIngestion(t *testing.T) {
	manifest, archives := fixtureManifest(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{fixtureAttemptKey: true}, Manifest: manifest, Archives: archives})
	if err != nil {
		t.Fatal(err)
	}
	demoted := ingested.Proved[0]
	demoted.Provenance.Job = nil
	if _, err := NewPublication(IngestedReports{Proved: []ProvedReport{demoted}}); err == nil {
		t.Fatal("publication retained a demoted report as job-level evidence")
	}
	if _, err := NewPublication(IngestedReports{}); !errors.Is(err, ErrReportIngest) {
		t.Fatalf("empty ingestion must not publish: %v", err)
	}
	if pub, err := NewPublication(ingested); err != nil || pub.Reports[0].TotalTimedDuration != time.Second {
		t.Fatalf("exact timed duration not retained: %+v %v", pub, err)
	}
}
