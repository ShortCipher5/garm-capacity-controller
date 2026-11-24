package cihealthreport

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"slices"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

var ErrReportIngest = errors.New("report ingestion refused")

// ReportScope is the existing authorized read scope gate for one exact run
// attempt, supplied by the caller (for example the minted, short-lived
// enrolled-repository attempt capability behind the CI artifact listing).
// Implementations must fail closed: a revoked, expired or unknown scope
// refuses authorization. A nil scope is never authorized.
type ReportScope interface {
	ReportIngestAuthorized(ctx context.Context, attempt cihealth.AttemptKey) bool
}

// ReportArchive is the exact downloaded artifact byte snapshot for one
// manifest row. Callers must not mutate Bytes while ingestion runs.
type ReportArchive struct {
	ArtifactID int64
	Bytes      []byte
}

// IngestInput binds one parsed manifest to its byte snapshots under the
// authorized scope. Missing, extra or repeated archives reject the whole
// ingestion; there is no partial publication.
type IngestInput struct {
	Scope    ReportScope
	Manifest ReportManifest
	Archives []ReportArchive
}

// ProvedReport is one job-level report whose producer, shard, retry and
// exact bytes were independently established. Only these may drive
// test-level results. Cases carries the reviewer-bound per-case observations
// when the manifest bound case identities; it stays empty for
// summary-only reports.
type ProvedReport struct {
	Provenance ReportProvenance
	Report     Report
	Cases      []BoundCaseObservation
}

// BoundCaseObservation is one reviewer-bound per-case observation from a
// proved report. Case identity is the reviewer's numeric ID; report names,
// classes and files are never retained.
type BoundCaseObservation struct {
	CaseID     int64
	Occurrence int
	Outcome    Outcome
	Timed      bool
	Duration   time.Duration
}

// MetadataReport is one run-only artifact retained as metadata. It carries
// archive pins but no report contents and must never be rendered as
// job-level evidence.
type MetadataReport struct {
	Provenance ReportProvenance
}

// IngestedReports is the complete validated result for one attempt's
// manifest. Slices are ordered by artifact ID for deterministic handling.
type IngestedReports struct {
	Proved   []ProvedReport
	Metadata []MetadataReport
}

// IngestReports validates the manifest against the authorized scope and the
// exact archive bytes, then projects job-level reports through the bounded
// reviewed ZIP/JUnit parsers. It authenticates nothing by itself: the scope
// gate and the manifest's reviewed producer bindings are caller-established
// facts, and any inconsistency, duplicate, unsafe member, ceiling violation
// or invalid content refuses the entire ingestion. No report bodies, log or
// environment output, tokenized URLs or archive bytes are retained in the
// results; only bounded projections and pins leave this function.
func IngestReports(ctx context.Context, in IngestInput) (IngestedReports, error) {
	fail := func() (IngestedReports, error) { return IngestedReports{}, ErrReportIngest }
	// Validate cardinality before allocating the archive map or visiting any
	// archive. Manifest validation supplies the fixed maximum artifact count.
	if ctx == nil || ctx.Err() != nil || in.Scope == nil ||
		len(in.Archives) != len(in.Manifest.Artifacts) || !in.Manifest.valid() {
		return fail()
	}
	attempt := cihealth.AttemptKey{
		Source: in.Manifest.Source, RepositoryID: in.Manifest.RepositoryID, WorkflowID: in.Manifest.WorkflowID,
		RunID: in.Manifest.RunID, Attempt: in.Manifest.Attempt,
	}
	if !in.Scope.ReportIngestAuthorized(ctx, attempt) || ctx.Err() != nil {
		return fail()
	}
	archives := make(map[int64][]byte, len(in.Archives))
	for _, archive := range in.Archives {
		if archive.ArtifactID <= 0 || len(archive.Bytes) == 0 || archives[archive.ArtifactID] != nil {
			return fail()
		}
		archives[archive.ArtifactID] = archive.Bytes
	}
	// Validate the archive set and every actual byte length against the
	// manifest claims before any hashing or parsing. Metadata-only input is
	// covered here too: an exact digest alone never substitutes for the
	// advertised length, and no bytes are hashed before this pass succeeds.
	rows := make(map[int64]ReportManifestArtifact, len(in.Manifest.Artifacts))
	for _, row := range in.Manifest.Artifacts {
		rows[row.ArtifactID] = row
	}
	for id, raw := range archives {
		row := rows[id]
		if int64(len(raw)) != row.ArchiveBytes || len(raw) > MaxJUnitZIPBytes {
			return fail()
		}
	}
	out := IngestedReports{}
	for _, row := range in.Manifest.Artifacts {
		raw := archives[row.ArtifactID]
		archiveDigest := sha256.Sum256(raw)
		if subtle.ConstantTimeCompare(archiveDigest[:], row.ArchiveSHA256[:]) != 1 {
			return fail()
		}
		p := ReportProvenance{
			Attempt: attempt, ArtifactID: row.ArtifactID,
			ArchiveBytes: row.ArchiveBytes, ArchiveSHA256: row.ArchiveSHA256,
		}
		if row.Job != nil {
			job := cihealth.JobKey{Attempt: attempt, JobID: row.Job.JobID}
			retry := row.Job.Retry
			p.Job, p.Shard, p.Retry = &job, row.Job.Shard, &retry
			p.EntryName, p.EntryBytes, p.EntrySHA256 = row.EntryName, row.EntryBytes, row.EntrySHA256
		}
		if !p.valid() {
			return fail()
		}
		if p.Level() == ProvenanceRunMetadata {
			out.Metadata = append(out.Metadata, MetadataReport{Provenance: p})
			continue
		}
		report, err := ParseReviewedJUnitZIP(raw, ReviewedJUnitZIP{
			Binding: ArtifactBinding{
				Attempt: attempt, ArtifactID: row.ArtifactID, Job: p.Job, Shard: p.Shard, Retry: p.Retry,
			},
			ArchiveBytes: row.ArchiveBytes, ArchiveSHA256: row.ArchiveSHA256,
			EntryName: row.EntryName, EntryBytes: row.EntryBytes, EntrySHA256: row.EntrySHA256,
		})
		if err != nil {
			return fail()
		}
		bound, ok := boundCaseObservations(report, row.Cases)
		if !ok {
			return fail()
		}
		out.Proved = append(out.Proved, ProvedReport{Provenance: p, Report: report, Cases: bound})
	}
	if ctx.Err() != nil {
		return fail()
	}
	slices.SortFunc(out.Proved, func(a, b ProvedReport) int { return cmp.Compare(a.Provenance.ArtifactID, b.Provenance.ArtifactID) })
	slices.SortFunc(out.Metadata, func(a, b MetadataReport) int { return cmp.Compare(a.Provenance.ArtifactID, b.Provenance.ArtifactID) })
	return out, nil
}

// boundCaseObservations projects reviewer-bound case identities onto the
// exact parsed occurrences. An occurrence outside the report refuses the
// whole ingestion: a binding that cannot be located is not a partial proof.
func boundCaseObservations(report Report, bindings []ReportManifestCase) ([]BoundCaseObservation, bool) {
	if len(bindings) == 0 {
		return nil, true
	}
	cases := report.Cases()
	out := make([]BoundCaseObservation, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Occurrence <= 0 || binding.Occurrence > len(cases) || binding.CaseID <= 0 {
			return nil, false
		}
		c := cases[binding.Occurrence-1]
		if c.Occurrence != binding.Occurrence {
			return nil, false
		}
		out = append(out, BoundCaseObservation{
			CaseID: binding.CaseID, Occurrence: binding.Occurrence,
			Outcome: c.Outcome, Timed: c.Timed, Duration: c.Duration,
		})
	}
	return out, true
}

// valid re-checks a manifest value independent of how it was decoded, so a
// hand-built manifest cannot skip the wire validation.
func (m ReportManifest) valid() bool {
	if !sourceAtom(m.Source) || m.RepositoryID <= 0 || m.WorkflowID <= 0 || m.RunID <= 0 || m.Attempt <= 0 ||
		len(m.Artifacts) == 0 || len(m.Artifacts) > cihealth.MaxRetainedAttemptReports {
		return false
	}
	artifacts := make(map[int64]bool, len(m.Artifacts))
	slots := make(map[manifestSlot]bool, len(m.Artifacts))
	for _, row := range m.Artifacts {
		p := ReportProvenance{
			Attempt: cihealth.AttemptKey{
				Source: m.Source, RepositoryID: m.RepositoryID, WorkflowID: m.WorkflowID, RunID: m.RunID, Attempt: m.Attempt,
			},
			ArtifactID: row.ArtifactID, ArchiveBytes: row.ArchiveBytes, ArchiveSHA256: row.ArchiveSHA256,
		}
		if row.Job != nil {
			job := cihealth.JobKey{Attempt: p.Attempt, JobID: row.Job.JobID}
			retry := row.Job.Retry
			p.Job, p.Shard, p.Retry = &job, row.Job.Shard, &retry
			p.EntryName, p.EntryBytes, p.EntrySHA256 = row.EntryName, row.EntryBytes, row.EntrySHA256
		}
		if !p.valid() || artifacts[row.ArtifactID] {
			return false
		}
		if row.Job != nil {
			if !validManifestCases(row.Cases) {
				return false
			}
			slot := manifestSlot{row.Job.Shard, row.Job.Retry}
			if slots[slot] {
				return false
			}
			slots[slot] = true
		} else if len(row.Cases) != 0 {
			return false
		}
		artifacts[row.ArtifactID] = true
	}
	return true
}

// validManifestCases re-checks reviewer-bound case identities for hand-built
// manifests: positive IDs, in-range occurrences, no repeated occurrence.
func validManifestCases(cases []ReportManifestCase) bool {
	if len(cases) > MaxCases {
		return false
	}
	seen := make(map[int]bool, len(cases))
	for _, c := range cases {
		if c.Occurrence <= 0 || c.Occurrence > MaxCases || c.CaseID <= 0 || seen[c.Occurrence] {
			return false
		}
		seen[c.Occurrence] = true
	}
	return true
}
