package cihealthreport

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

var ErrReportStore = errors.New("report publication store refused")

// StoredCaseObservation is one bounded per-case observation retained with
// its report. Case identity is the reviewer-bound numeric ID; report names,
// classes and files are never retained.
type StoredCaseObservation struct {
	CaseID     int64
	Occurrence int
	Outcome    Outcome
	Timed      bool
	Duration   time.Duration
}

// StoredReport is the durable, display-safe projection of one proved
// job-level report. Pins are retained only to enforce idempotency and to
// reject cross-attempt reuse; they are never rendered. Report bodies, labels,
// logs and URLs are not retained. Cases stays empty for summary-only
// reports that bound no reviewer case identities.
type StoredReport struct {
	ArtifactID    int64
	JobID         int64
	Shard         string
	Retry         uint16 // zero is a known initial execution of this shard
	ArchiveSHA256 [sha256.Size]byte
	EntrySHA256   [sha256.Size]byte
	Totals        Totals
	Timed         int
	Mean, P95     time.Duration
	// TotalTimedDuration is the exact sum of timed case durations, so
	// aggregate sample means stay exact across reports.
	TotalTimedDuration time.Duration
	Cases              []StoredCaseObservation
}

// StoredMetadata is one run-only artifact retained as metadata. It carries
// no report contents and must never be rendered as job-level evidence.
type StoredMetadata struct {
	ArtifactID    int64
	ArchiveSHA256 [sha256.Size]byte
}

// Publication is the complete result of one authorized ingestion, in the
// form retained and rendered. Reports and metadata are ordered by artifact ID.
type Publication struct {
	Scope    cihealth.HistoryScope
	Attempt  cihealth.AttemptKey
	Reports  []StoredReport
	Metadata []StoredMetadata
}

// StoredPublication is one retained publication with its receipt time.
type StoredPublication struct {
	Scope      cihealth.HistoryScope
	Attempt    cihealth.AttemptKey
	ReceivedAt time.Time
	Reports    []StoredReport
	Metadata   []StoredMetadata
}

// NewPublication converts a validated ingestion result into the retained
// form. It re-checks every provenance claim and keeps only bounded
// projections plus retention pins.
func NewPublication(in IngestedReports) (Publication, error) {
	if len(in.Proved)+len(in.Metadata) == 0 {
		return Publication{}, ErrReportIngest
	}
	out := Publication{}
	for _, proved := range in.Proved {
		p := proved.Provenance
		if !p.valid() || p.Level() != ProvenanceJobReport || out.Attempt != (cihealth.AttemptKey{}) && out.Attempt != p.Attempt {
			return Publication{}, ErrReportIngest
		}
		summary := proved.Report.Summary()
		var total time.Duration
		for _, c := range proved.Report.Cases() {
			if c.Timed {
				total += c.Duration
			}
		}
		cases, ok := storedCaseObservations(proved.Cases)
		if !ok {
			return Publication{}, ErrReportIngest
		}
		out.Attempt = p.Attempt
		out.Reports = append(out.Reports, StoredReport{
			ArtifactID: p.ArtifactID, JobID: p.Job.JobID, Shard: p.Shard, Retry: *p.Retry,
			ArchiveSHA256: p.ArchiveSHA256, EntrySHA256: p.EntrySHA256,
			Totals: summary.Totals, Timed: summary.Timed, Mean: summary.Mean, P95: summary.P95,
			TotalTimedDuration: total,
			Cases:              cases,
		})
	}
	for _, metadata := range in.Metadata {
		p := metadata.Provenance
		if !p.valid() || p.Level() != ProvenanceRunMetadata || out.Attempt != (cihealth.AttemptKey{}) && out.Attempt != p.Attempt {
			return Publication{}, ErrReportIngest
		}
		out.Attempt = p.Attempt
		out.Metadata = append(out.Metadata, StoredMetadata{ArtifactID: p.ArtifactID, ArchiveSHA256: p.ArchiveSHA256})
	}
	out.Scope = cihealth.HistoryScope{Source: out.Attempt.Source, RepositoryID: out.Attempt.RepositoryID, WorkflowID: out.Attempt.WorkflowID}
	if len(out.Reports)+len(out.Metadata) > cihealth.MaxRetainedAttemptReports {
		return Publication{}, ErrReportIngest
	}
	return out, nil
}

// storedCaseObservations validates and converts reviewer-bound case
// observations into their retained form. Anything invalid refuses the
// publication rather than silently dropping cases.
func storedCaseObservations(in []BoundCaseObservation) ([]StoredCaseObservation, bool) {
	if len(in) == 0 {
		return nil, true
	}
	out := make([]StoredCaseObservation, 0, len(in))
	seen := make(map[int]bool, len(in))
	for _, c := range in {
		if c.CaseID <= 0 || c.Occurrence <= 0 || c.Occurrence > MaxCases || seen[c.Occurrence] ||
			c.Duration < 0 || c.Duration > MaxDuration || !c.Timed && c.Duration != 0 {
			return nil, false
		}
		switch c.Outcome {
		case Pass, Failure, Error, Skipped:
		default:
			return nil, false
		}
		seen[c.Occurrence] = true
		out = append(out, StoredCaseObservation{
			CaseID: c.CaseID, Occurrence: c.Occurrence, Outcome: c.Outcome, Timed: c.Timed, Duration: c.Duration,
		})
	}
	return out, true
}

// SaveResult reports whether a save created a new record or returned the
// identical existing record unchanged.
type SaveResult struct {
	Record  StoredPublication
	Created bool
}

// Store retains publications idempotently with provenance and retention.
// Implementations must reject conflicting or cross-attempt-reused evidence
// and purge by the shared history retention window. This interface is the
// Plan 05/06 convergence surface: the durable history lifecycle (Plan 05)
// owns long-lived wiring, while this package ships the contract and offline
// reference behavior.
type Store interface {
	SavePublication(ctx context.Context, pub Publication) (SaveResult, error)
	LoadPublication(ctx context.Context, attempt cihealth.AttemptKey) (StoredPublication, bool, error)
	Close() error
}
