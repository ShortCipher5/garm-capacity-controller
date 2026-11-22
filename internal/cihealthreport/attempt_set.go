package cihealthreport

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const MaxReviewedAttemptReports = 32

var ErrReviewedAttemptReportSet = errors.New("JUnit reports do not match the reviewed attempt set")

// ReviewedAttemptEntry is an independently reviewed expectation for one
// selected XML entry per artifact, not a claim read from the artifact.
// The digest pins extracted XML bytes, not the enclosing ZIP.
type ReviewedAttemptEntry struct {
	ArtifactID, JobID int64
	Shard             string
	Retry             uint16 // zero means a known initial execution of this shard
	XMLSHA256         [sha256.Size]byte
}

// ReviewedAttemptReportTotals counts occurrences across the supplied planned
// set, not distinct tests. The same test may intentionally run in multiple
// shards. No conclusion across GitHub run attempts is implied.
type ReviewedAttemptReportTotals struct {
	Initial, Retry                   Totals
	InitialArtifacts, RetryArtifacts int
}

type attemptShardSlot struct {
	shard string
	retry uint16
}

// MatchReviewedAttemptReports requires the exact reviewed set for one attempt.
// Missing, unexpected and repeated artifacts all return a zero result and a
// generic error. This is a consistency comparison only: it does not fetch,
// authenticate or independently verify the expected entries, assert archive
// provenance, or prove that the supplied expected set covers all upstream runs
// or tests. There is no partial aggregate on failure.
func MatchReviewedAttemptReports(attempt cihealth.AttemptKey, expected []ReviewedAttemptEntry, observed []Report) (ReviewedAttemptReportTotals, error) {
	fail := func() (ReviewedAttemptReportTotals, error) {
		return ReviewedAttemptReportTotals{}, ErrReviewedAttemptReportSet
	}
	if len(expected) == 0 || len(expected) > MaxReviewedAttemptReports || len(expected) != len(observed) {
		return fail()
	}
	pending := make(map[int64]ReviewedAttemptEntry, len(expected))
	slots := make(map[attemptShardSlot]bool, len(expected))
	for _, entry := range expected {
		slot := attemptShardSlot{entry.Shard, entry.Retry}
		_, duplicate := pending[entry.ArtifactID]
		if duplicate || slots[slot] || entry.ArtifactID <= 0 || entry.JobID <= 0 ||
			!sourceAtom(entry.Shard) || entry.XMLSHA256 == ([sha256.Size]byte{}) {
			return fail()
		}
		pending[entry.ArtifactID], slots[slot] = entry, true
	}
	var result ReviewedAttemptReportTotals
	for _, report := range observed {
		b := report.binding
		entry, ok := pending[b.ArtifactID]
		if !ok || b.Attempt != attempt || b.Job == nil || b.Job.Attempt != attempt || b.Job.JobID != entry.JobID ||
			b.Shard != entry.Shard || b.Retry == nil || *b.Retry != entry.Retry ||
			subtle.ConstantTimeCompare(entry.XMLSHA256[:], report.digest[:]) != 1 {
			return fail()
		}
		delete(pending, b.ArtifactID)
		totals := &result.Initial
		if entry.Retry == 0 {
			result.InitialArtifacts++
		} else {
			result.RetryArtifacts++
			totals = &result.Retry
		}
		// Parser-created totals are bounded by MaxCases per report. Never
		// expose an aggregate unless the complete expected set matches.
		got := report.summary.Totals
		totals.Cases += got.Cases
		totals.Passed += got.Passed
		totals.Failed += got.Failed
		totals.Errors += got.Errors
		totals.Skipped += got.Skipped
	}
	return result, nil // Equal length plus deletion proves no expected row remains.
}
