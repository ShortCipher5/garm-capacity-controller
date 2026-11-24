package cihealthreport

import (
	"crypto/sha256"
	"errors"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

var ErrProvenance = errors.New("report provenance unestablished")

// ProvenanceLevel distinguishes what the existing authorized read scope can
// prove about one artifact from what only an independent reviewer can prove.
type ProvenanceLevel string

const (
	// ProvenanceRunMetadata is all a run-scoped artifact listing can
	// establish: the artifact exists (or existed) in the repository's run
	// scope. It never authorizes test-level results.
	ProvenanceRunMetadata ProvenanceLevel = "run_metadata"
	// ProvenanceJobReport requires an independently reviewed producer job in
	// the exact run attempt, plus verified shard and framework retry bindings
	// and byte-exact archive/entry pins. Only this level may produce
	// test-level results.
	ProvenanceJobReport ProvenanceLevel = "job_report"
)

// ReportProvenance is the opt-in provenance contract for one bounded report
// artifact. Every field must be independently established by the authorized
// reviewer before ingestion; nothing here may be derived from an artifact
// name, an archive path, report XML, or the run-scoped artifact listing.
// A nil Job or nil Retry is not a partial proof: it demotes the artifact to
// ProvenanceRunMetadata and ingestion must not publish its test results.
type ReportProvenance struct {
	Attempt    cihealth.AttemptKey
	ArtifactID int64
	Job        *cihealth.JobKey
	Shard      string
	Retry      *uint16
	// ArchiveBytes/ArchiveSHA256 pin the exact downloaded archive. The entry
	// fields pin the exact extracted XML member and are populated only for
	// job-level reports; metadata rows never extract report XML.
	ArchiveBytes  int64
	ArchiveSHA256 [sha256.Size]byte
	EntryName     string
	EntryBytes    int64
	EntrySHA256   [sha256.Size]byte
}

// Level reports what the provenance claim actually proves. Anything short of
// a complete job-level claim is run-only metadata.
func (p ReportProvenance) Level() ProvenanceLevel {
	if p.Job == nil || p.Retry == nil {
		return ProvenanceRunMetadata
	}
	return ProvenanceJobReport
}

// RunArtifactMetadata is the metadata-only projection of one run-scoped
// artifact listing row. It deliberately carries no job, shard, retry or
// extracted-byte pin: the listing cannot supply them, and this type must
// never masquerade as job-level evidence.
type RunArtifactMetadata struct {
	ArtifactID int64
	// ListedUnexpired is a metadata observation, not proof that the archive
	// was downloaded or that any report exists inside it.
	ListedUnexpired       bool
	AdvertisedSHA256      [sha256.Size]byte
	AdvertisedSHA256Known bool
}

// ProvenanceFromRunMetadata projects one run-scoped listing row to its only
// honest provenance level. GitHub's run-level artifact metadata carries no
// run attempt, job, producer attestation, shard or retry, so the projection
// is always metadata-only and can never publish test-level results. An
// advertised digest, even when present, is not a pin of downloaded bytes and
// does not populate any archive or entry pin.
func ProvenanceFromRunMetadata(meta demandrepair.CIArtifactMetadata) RunArtifactMetadata {
	return RunArtifactMetadata{
		ArtifactID:            meta.ID,
		ListedUnexpired:       meta.Availability == demandrepair.CIArtifactListedUnexpired,
		AdvertisedSHA256:      meta.AdvertisedSHA256,
		AdvertisedSHA256Known: meta.AdvertisedSHA256Known,
	}
}

// MetadataProvenance builds the run-only provenance record for one run-scoped
// artifact. It is the only provenance a metadata row may carry: exact attempt
// scope and artifact identity with byte-exact archive pins, never a producer
// job and never report contents.
func MetadataProvenance(attempt cihealth.AttemptKey, meta RunArtifactMetadata, archiveBytes int64, archiveSHA256 [sha256.Size]byte) (ReportProvenance, bool) {
	p := ReportProvenance{Attempt: attempt, ArtifactID: meta.ArtifactID, ArchiveBytes: archiveBytes, ArchiveSHA256: archiveSHA256}
	if !p.valid() {
		return ReportProvenance{}, false
	}
	return p, true
}

func (p ReportProvenance) valid() bool {
	a := p.Attempt
	zero := [sha256.Size]byte{}
	if !sourceAtom(a.Source) || a.RepositoryID <= 0 || a.WorkflowID <= 0 || a.RunID <= 0 || a.Attempt <= 0 ||
		p.ArtifactID <= 0 || p.ArchiveBytes <= 0 || p.ArchiveBytes > MaxJUnitZIPBytes || p.ArchiveSHA256 == zero {
		return false
	}
	if p.Level() == ProvenanceRunMetadata {
		return p.Job == nil && p.Retry == nil && p.EntryName == "" && p.EntryBytes == 0 && p.EntrySHA256 == zero
	}
	j := *p.Job
	return j.JobID > 0 && j.Attempt == p.Attempt && (p.Shard == "" || sourceAtom(p.Shard)) &&
		validJUnitZIPName(p.EntryName) && p.EntryBytes > 0 && p.EntryBytes <= MaxReportBytes && p.EntrySHA256 != zero
}
