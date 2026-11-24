package cihealthreport

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

// fixtureScope authorizes exactly the attempts in its set, so tests can prove
// the scope gate saw the manifest's exact attempt and can simulate revocation.
type fixtureScope map[cihealth.AttemptKey]bool

func (f fixtureScope) ReportIngestAuthorized(_ context.Context, attempt cihealth.AttemptKey) bool {
	return f[attempt]
}

type fixtureArtifact struct {
	id    int64
	job   int64 // zero retains the artifact as run-only metadata
	shard string
	retry uint16
	entry string
	xml   []byte
}

func fixtureAttempt(source string, repo, workflow, run, attempt int64) cihealth.AttemptKey {
	return cihealth.AttemptKey{Source: source, RepositoryID: repo, WorkflowID: workflow, RunID: run, Attempt: attempt}
}

// fixtureManifest binds one attempt's artifacts to their exact bytes. Job
// rows pin the extracted entry; metadata rows pin the archive only.
func fixtureManifest(t *testing.T, attempt cihealth.AttemptKey, artifacts ...fixtureArtifact) (ReportManifest, []ReportArchive) {
	t.Helper()
	manifest := ReportManifest{
		Source: attempt.Source, RepositoryID: attempt.RepositoryID, WorkflowID: attempt.WorkflowID,
		RunID: attempt.RunID, Attempt: attempt.Attempt,
		Artifacts: make([]ReportManifestArtifact, 0, len(artifacts)),
	}
	archives := make([]ReportArchive, 0, len(artifacts))
	for _, artifact := range artifacts {
		entry := artifact.entry
		if entry == "" {
			entry = "reports/fixture.xml"
		}
		body := artifact.xml
		if body == nil {
			body = []byte(`<testsuite><testcase name="fixture-private" time="1"/></testsuite>`)
		}
		archive := buildJUnitZIP(t, fixtureZIPEntry(entry, body))
		row := ReportManifestArtifact{
			ArtifactID: artifact.id, ArchiveBytes: int64(len(archive)), ArchiveSHA256: sha256.Sum256(archive),
		}
		if artifact.job > 0 {
			row.Job = &ReportManifestJob{JobID: artifact.job, Shard: artifact.shard, Retry: artifact.retry}
			row.EntryName, row.EntryBytes, row.EntrySHA256 = entry, int64(len(body)), sha256.Sum256(body)
		}
		manifest.Artifacts = append(manifest.Artifacts, row)
		archives = append(archives, ReportArchive{ArtifactID: artifact.id, Bytes: archive})
	}
	return manifest, archives
}
