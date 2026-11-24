package cihealthreport

import (
	"crypto/sha256"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func fixtureProvenance() ReportProvenance {
	attempt := fixtureAttempt("fixture_source", 10, 11, 12, 1)
	job := cihealth.JobKey{Attempt: attempt, JobID: 99}
	retry := uint16(0)
	return ReportProvenance{
		Attempt: attempt, ArtifactID: 500, Job: &job, Shard: "unit", Retry: &retry,
		ArchiveBytes: 100, ArchiveSHA256: sha256.Sum256([]byte("archive")),
		EntryName: "reports/fixture.xml", EntryBytes: 10, EntrySHA256: sha256.Sum256([]byte("entry")),
	}
}

func TestReportProvenanceLevelDemotesIncompleteClaims(t *testing.T) {
	p := fixtureProvenance()
	if p.Level() != ProvenanceJobReport || !p.valid() {
		t.Fatalf("complete claim must be job-level: %+v", p)
	}
	missingJob := p
	missingJob.Job = nil
	if missingJob.Level() != ProvenanceRunMetadata || missingJob.valid() {
		t.Fatalf("missing producer proof must demote to metadata: %+v", missingJob)
	}
	missingRetry := p
	missingRetry.Retry = nil
	if missingRetry.Level() != ProvenanceRunMetadata || missingRetry.valid() {
		t.Fatalf("unknown retry must demote to metadata: %+v", missingRetry)
	}
}

func TestReportProvenanceRejectsInvalidClaims(t *testing.T) {
	base := fixtureProvenance()
	jobOtherAttempt := cihealth.JobKey{Attempt: fixtureAttempt("fixture_source", 10, 11, 12, 2), JobID: 99}
	cases := map[string]ReportProvenance{
		"empty_source":      {Attempt: cihealth.AttemptKey{Source: "", RepositoryID: 10, WorkflowID: 11, RunID: 12, Attempt: 1}, ArtifactID: 500, ArchiveBytes: 1, ArchiveSHA256: base.ArchiveSHA256},
		"zero_artifact":     withArtifact(base, 0),
		"oversized_archive": {Attempt: base.Attempt, ArtifactID: 500, Job: base.Job, Shard: "unit", Retry: base.Retry, ArchiveBytes: MaxJUnitZIPBytes + 1, ArchiveSHA256: base.ArchiveSHA256, EntryName: base.EntryName, EntryBytes: 1, EntrySHA256: base.EntrySHA256},
		"zero_archive_pin":  {Attempt: base.Attempt, ArtifactID: 500, Job: base.Job, Shard: "unit", Retry: base.Retry, ArchiveBytes: 1, ArchiveSHA256: [sha256.Size]byte{}, EntryName: base.EntryName, EntryBytes: 1, EntrySHA256: base.EntrySHA256},
		"bad_entry_name":    {Attempt: base.Attempt, ArtifactID: 500, Job: base.Job, Shard: "unit", Retry: base.Retry, ArchiveBytes: 1, ArchiveSHA256: base.ArchiveSHA256, EntryName: "../evil.xml", EntryBytes: 1, EntrySHA256: base.EntrySHA256},
		"oversized_entry":   {Attempt: base.Attempt, ArtifactID: 500, Job: base.Job, Shard: "unit", Retry: base.Retry, ArchiveBytes: 1, ArchiveSHA256: base.ArchiveSHA256, EntryName: base.EntryName, EntryBytes: MaxReportBytes + 1, EntrySHA256: base.EntrySHA256},
		"job_other_attempt": {Attempt: base.Attempt, ArtifactID: 500, Job: &jobOtherAttempt, Shard: "unit", Retry: base.Retry, ArchiveBytes: 1, ArchiveSHA256: base.ArchiveSHA256, EntryName: base.EntryName, EntryBytes: 1, EntrySHA256: base.EntrySHA256},
		"bad_shard":         {Attempt: base.Attempt, ArtifactID: 500, Job: base.Job, Shard: "not/an/atom", Retry: base.Retry, ArchiveBytes: 1, ArchiveSHA256: base.ArchiveSHA256, EntryName: base.EntryName, EntryBytes: 1, EntrySHA256: base.EntrySHA256},
	}
	for name, claim := range cases {
		if claim.valid() {
			t.Fatalf("%s: invalid provenance claim accepted: %+v", name, claim)
		}
	}
}

func withArtifact(p ReportProvenance, id int64) ReportProvenance {
	p.ArtifactID = id
	return p
}

func TestProvenanceFromRunMetadataStaysMetadataOnly(t *testing.T) {
	digest := sha256.Sum256([]byte("advertised"))
	meta := demandrepair.CIArtifactMetadata{
		ID: 700, SizeInBytes: 2048, SizeKnown: true, Availability: demandrepair.CIArtifactListedUnexpired,
		AdvertisedSHA256: digest, AdvertisedSHA256Known: true,
	}
	projected := ProvenanceFromRunMetadata(meta)
	if projected.ArtifactID != 700 || !projected.ListedUnexpired || !projected.AdvertisedSHA256Known ||
		projected.AdvertisedSHA256 != digest {
		t.Fatalf("run metadata projection lost listing facts: %+v", projected)
	}
	attempt := fixtureAttempt("fixture_source", 10, 11, 12, 1)
	provenance, ok := MetadataProvenance(attempt, projected, 2048, sha256.Sum256([]byte("downloaded")))
	if !ok || provenance.Level() != ProvenanceRunMetadata || !provenance.valid() {
		t.Fatalf("run metadata must not become job-level evidence: %+v %v", provenance, ok)
	}
	if provenance.Job != nil || provenance.Retry != nil || provenance.EntryName != "" || provenance.EntryBytes != 0 ||
		provenance.EntrySHA256 != ([sha256.Size]byte{}) {
		t.Fatalf("metadata provenance carries report-level claims: %+v", provenance)
	}
	if bad, ok := MetadataProvenance(cihealth.AttemptKey{}, projected, 2048, sha256.Sum256([]byte("downloaded"))); ok || bad.valid() {
		t.Fatalf("metadata provenance accepted an unscoped attempt: %+v", bad)
	}
	expired := ProvenanceFromRunMetadata(demandrepair.CIArtifactMetadata{ID: 701, Availability: demandrepair.CIArtifactExpired})
	if expired.ListedUnexpired || expired.AdvertisedSHA256Known {
		t.Fatalf("expired artifact misreported: %+v", expired)
	}
}
