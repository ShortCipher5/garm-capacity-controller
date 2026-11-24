package cihealth

import "time"

// HistoryRetentionWindow is the shared retention surface for durable CI
// history (Plan 05) and retained test report publications (Plan 06). A report
// publication must not outlive the history it annotates: both kinds of
// retained data are scoped by HistoryScope and purged within the same window.
// Plan 05's durable history store and Plan 06's publication store must keep
// this value equal to the durable history retention window.
const HistoryRetentionWindow = 30 * 24 * time.Hour

// MaxRetainedAttemptReports bounds the report artifacts retained for one
// exact run attempt. It matches the bounded reviewed attempt report set:
// a larger claimed set cannot become retained evidence.
const MaxRetainedAttemptReports = 32

// ReportRetentionScope is the retention scope for one retained test report
// publication. It is the same identity as the durable history scope; report
// publications are retained and purged within the history scope lifecycle.
type ReportRetentionScope = HistoryScope

// RetainedReportKey locates one retained report artifact. The attempt key is
// exact: a report proven for one run attempt must never be retrievable as
// evidence for another attempt, run, workflow, repository or source.
type RetainedReportKey struct {
	Scope   ReportRetentionScope
	Attempt AttemptKey
	// ArtifactID is the numeric GitHub artifact identity within the
	// repository; EntrySHA256 pins the exact extracted report bytes.
	ArtifactID int64
}
