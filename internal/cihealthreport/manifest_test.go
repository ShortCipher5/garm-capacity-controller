package cihealthreport

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var (
	fixtureArchiveSHA = sha256.Sum256([]byte("archive"))
	fixtureEntrySHA   = sha256.Sum256([]byte("entry"))
)

// manifestDoc builds one manifest document with the supplied artifact rows.
func manifestDoc(rows ...string) string {
	return fmt.Sprintf(`{
  "format": %q,
  "source": "fixture_source",
  "repository_id": 10,
  "workflow_id": 11,
  "run_id": 12,
  "attempt": 1,
  "artifacts": [%s]
}`, ReportManifestFormat, strings.Join(rows, ","))
}

// metadataRowJSON is one run-only artifact row.
func metadataRowJSON(id int64) string {
	return fmt.Sprintf(`{"artifact_id":%d,"archive_bytes":100,"archive_sha256":"%x"}`, id, fixtureArchiveSHA)
}

// jobRowJSON is one proved job-level artifact row; distinct IDs keep the
// (job, shard, retry) slot unique unless a test overrides it.
func jobRowJSON(id, jobID int64, shard string, retry uint16) string {
	return fmt.Sprintf(`{
  "artifact_id": %d,
  "archive_bytes": 100,
  "archive_sha256": "%x",
  "entry_name": "reports/fixture.xml",
  "entry_bytes": 10,
  "entry_sha256": "%x",
  "job": {"job_id": %d, "shard": %q, "retry": %d}
}`, id, fixtureArchiveSHA, fixtureEntrySHA, jobID, shard, retry)
}

func TestParseReportManifestAcceptsMetadataAndJobRows(t *testing.T) {
	parsed, err := ParseReportManifest([]byte(manifestDoc(metadataRowJSON(500), jobRowJSON(501, 9001, "unit", 0))))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Source != "fixture_source" || parsed.RepositoryID != 10 || parsed.WorkflowID != 11 ||
		parsed.RunID != 12 || parsed.Attempt != 1 || len(parsed.Artifacts) != 2 {
		t.Fatalf("manifest scope misread: %+v", parsed)
	}
	if row := parsed.Artifacts[0]; row.ArtifactID != 500 || row.ArchiveBytes != 100 || row.Job != nil ||
		row.EntryName != "" || row.EntryBytes != 0 || row.EntrySHA256 != ([sha256.Size]byte{}) {
		t.Fatalf("metadata row must pin the archive only: %+v", row)
	}
	row := parsed.Artifacts[1]
	if row.Job == nil || row.Job.JobID != 9001 || row.Job.Shard != "unit" || row.Job.Retry != 0 ||
		row.EntryName != "reports/fixture.xml" || row.EntryBytes != 10 || row.EntrySHA256 != fixtureEntrySHA {
		t.Fatalf("job row lost its reviewed binding: %+v", row)
	}
}

func TestParseReportManifestRejectsInvalidDocuments(t *testing.T) {
	validJob := jobRowJSON(500, 9001, "unit", 0)
	cases := map[string]string{
		"empty":         ``,
		"not_object":    `[]`,
		"trailing_data": manifestDoc(metadataRowJSON(500)) + ` {}`,
		"unknown_field": `{"format": "` + ReportManifestFormat + `", "source": "fixture_source", "repository_id": 10, "workflow_id": 11, "run_id": 12, "attempt": 1, "artifacts": [], "extra": 1}` +
			``, // unknown top-level field, even with an empty artifact list
		"duplicate_key":   `{"format":"` + ReportManifestFormat + `","source":"first","source":"second","repository_id":10,"workflow_id":11,"run_id":12,"attempt":1,"artifacts":[]}`,
		"folded_key":      `{"format":"` + ReportManifestFormat + `","source":"first","SOURCE":"second","repository_id":10,"workflow_id":11,"run_id":12,"attempt":1,"artifacts":[]}`,
		"float_id":        strings.Replace(manifestDoc(metadataRowJSON(500)), `"repository_id": 10`, `"repository_id": 10.5`, 1),
		"string_id":       strings.Replace(manifestDoc(metadataRowJSON(500)), `"attempt": 1`, `"attempt": "1"`, 1),
		"wrong_format":    strings.Replace(manifestDoc(metadataRowJSON(500)), ReportManifestFormat, "other-format", 1),
		"bad_source":      strings.Replace(manifestDoc(metadataRowJSON(500)), `"fixture_source"`, `"not an atom"`, 1),
		"zero_run":        strings.Replace(manifestDoc(metadataRowJSON(500)), `"run_id": 12`, `"run_id": 0`, 1),
		"empty_artifacts": manifestDoc(),
		"null_artifacts":  `{"format":"` + ReportManifestFormat + `","source":"fixture_source","repository_id":10,"workflow_id":11,"run_id":12,"attempt":1,"artifacts":null}`,
		"dup_artifact":    manifestDoc(metadataRowJSON(500), jobRowJSON(500, 9001, "unit", 0)),
		"entry_without_job": manifestDoc(fmt.Sprintf(
			`{"artifact_id":500,"archive_bytes":100,"archive_sha256":"%x","entry_name":"reports/fixture.xml","entry_bytes":10,"entry_sha256":"%x"}`,
			fixtureArchiveSHA, fixtureEntrySHA)),
		"short_sha":         manifestDoc(`{"artifact_id":500,"archive_bytes":100,"archive_sha256":"abcd"}`),
		"zero_sha":          manifestDoc(`{"artifact_id":500,"archive_bytes":100,"archive_sha256":"` + strings.Repeat("0", 64) + `"}`),
		"oversized_archive": manifestDoc(fmt.Sprintf(`{"artifact_id":500,"archive_bytes":%d,"archive_sha256":"%x"}`, MaxJUnitZIPBytes+1, fixtureArchiveSHA)),
		"oversized_entry": manifestDoc(fmt.Sprintf(
			`{"artifact_id":500,"archive_bytes":100,"archive_sha256":"%x","entry_name":"reports/fixture.xml","entry_bytes":%d,"entry_sha256":"%x","job":{"job_id":9001,"shard":"unit","retry":0}}`,
			fixtureArchiveSHA, MaxReportBytes+1, fixtureEntrySHA)),
		"bad_entry_name": manifestDoc(strings.Replace(validJob, `"reports/fixture.xml"`, `"../evil.xml"`, 1)),
		"dup_slot":       manifestDoc(jobRowJSON(500, 9001, "unit", 0), jobRowJSON(501, 9002, "unit", 0)),
		"bad_job_shard":  strings.Replace(manifestDoc(validJob), `"shard": "unit"`, `"shard": "bad/shard"`, 1),
		"negative_retry": strings.Replace(manifestDoc(validJob), `"retry": 0`, `"retry": -1`, 1),
		"zero_job":       strings.Replace(manifestDoc(validJob), `"job_id": 9001`, `"job_id": 0`, 1),
	}
	for name, doc := range cases {
		if _, err := ParseReportManifest([]byte(doc)); err == nil {
			t.Fatalf("%s: invalid manifest accepted: %s", name, doc)
		}
	}
	if _, err := ParseReportManifest(make([]byte, MaxReportManifestBytes+1)); err == nil {
		t.Fatal("oversized manifest accepted")
	}
	if _, err := ParseReportManifest([]byte("\xff" + manifestDoc(metadataRowJSON(500))[1:])); err == nil {
		t.Fatal("non-UTF-8 manifest accepted")
	}
}

func TestParseReportManifestRejectsArtifactCountCeiling(t *testing.T) {
	rows := make([]string, 0, 33)
	for i := 0; i < 33; i++ {
		rows = append(rows, jobRowJSON(int64(500+i), 9000+int64(i), fmt.Sprintf("shard%d", i), 0))
	}
	if _, err := ParseReportManifest([]byte(manifestDoc(rows...))); err == nil {
		t.Fatal("manifest above the artifact ceiling accepted")
	}
}

func TestReportManifestRoundTripsThroughJSON(t *testing.T) {
	attempt := fixtureAttempt("fixture_source", 10, 11, 12, 1)
	manifest, archives := fixtureManifest(t, attempt,
		fixtureArtifact{id: 500, job: 9001, shard: "unit", retry: 0},
		fixtureArtifact{id: 501, job: 9001, shard: "unit", retry: 1},
		fixtureArtifact{id: 502},
	)
	encoded, err := json.Marshal(manifestWireView(manifest))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseReportManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{attempt: true}, Manifest: parsed, Archives: archives})
	if err != nil || len(ingested.Proved) != 2 || len(ingested.Metadata) != 1 {
		t.Fatalf("round-tripped manifest did not ingest: %+v %v", ingested, err)
	}
}

// manifestWireView re-encodes a manifest as the published wire shape so the
// strict parser, not just the in-memory value, is exercised end to end.
func manifestWireView(m ReportManifest) map[string]any {
	artifacts := make([]map[string]any, 0, len(m.Artifacts))
	for _, row := range m.Artifacts {
		wire := map[string]any{
			"artifact_id": row.ArtifactID, "archive_bytes": row.ArchiveBytes,
			"archive_sha256": fmt.Sprintf("%x", row.ArchiveSHA256),
		}
		if row.Job != nil {
			wire["entry_name"] = row.EntryName
			wire["entry_bytes"] = row.EntryBytes
			wire["entry_sha256"] = fmt.Sprintf("%x", row.EntrySHA256)
			wire["job"] = map[string]any{"job_id": row.Job.JobID, "shard": row.Job.Shard, "retry": row.Job.Retry}
		}
		artifacts = append(artifacts, wire)
	}
	return map[string]any{
		"format": ReportManifestFormat, "source": m.Source, "repository_id": m.RepositoryID,
		"workflow_id": m.WorkflowID, "run_id": m.RunID, "attempt": m.Attempt, "artifacts": artifacts,
	}
}
