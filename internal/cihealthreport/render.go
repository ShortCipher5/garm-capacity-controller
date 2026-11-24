package cihealthreport

import (
	"cmp"
	"errors"
	"slices"
	"time"
)

var ErrReportView = errors.New("report view unavailable")

// ReportLink is an exact canonical source reference, not a URL. It carries
// numeric identities only — no host, path, credential or token — so a UI
// with an authorized linker can resolve it while the reference itself stays
// display-safe. JobID is zero exactly when the producing job is not proved.
type ReportLink struct {
	Source                   string
	RepositoryID, WorkflowID int64
	RunID, Attempt           int64
	ArtifactID, JobID        int64
}

// ReportSample aggregates bounded projections across a set of reports.
// A Frequency denominator of zero means "insufficient observations," never
// a measured zero rate. Mean is exact; an aggregated P95 is not defined and
// stays absent.
type ReportSample struct {
	Totals
	Timed                   int
	Mean                    time.Duration
	FailureAmongPassFail    Frequency
	ErrorAmongPassFailError Frequency
}

func reportSample(t Totals, timed int, total time.Duration) ReportSample {
	var mean time.Duration
	if timed > 0 {
		mean = total / time.Duration(timed)
	}
	decisive := t.Passed + t.Failed
	return ReportSample{
		Totals: t, Timed: timed, Mean: mean,
		FailureAmongPassFail:    Frequency{t.Failed, decisive},
		ErrorAmongPassFailError: Frequency{t.Errors, decisive + t.Errors},
	}
}

// ExpectedShardSlot is one reviewer-declared (shard, retry) execution slot
// used for coverage. It is out-of-band plan knowledge; expected shards are
// never inferred from artifact or report names.
type ExpectedShardSlot struct {
	Shard string
	Retry uint16
}

// ShardSlotCoverage renders one declared slot's evidence state. A missing
// slot is neither passed nor failed; it only has no proved report.
type ShardSlotCoverage struct {
	Shard   string
	Retry   uint16
	Proved  bool
	Missing bool
}

// ReportCoverage makes partial coverage visible instead of conflating
// missing evidence with success.
type ReportCoverage struct {
	DeclaredArtifacts, ProvedArtifacts, MetadataArtifacts int
	Slots                                                 []ShardSlotCoverage
}

// ReportView is one proved job-level report row.
type ReportView struct {
	Link      ReportLink
	Shard     string
	Retry     uint16
	Totals    Totals
	Timed     int
	Mean, P95 time.Duration
}

// MetadataView is one run-only artifact, explicitly labelled so it can never
// masquerade as job-level evidence.
type MetadataView struct {
	Link            ReportLink
	RunMetadataOnly bool
}

// PublicationView is the deterministic rendering of one stored publication.
// Rows are ordered by artifact identity; unverifiable results are omitted or
// explicitly labelled.
type PublicationView struct {
	Link             ReportLink
	ReceivedAt       time.Time
	JobLevelEvidence bool
	Coverage         ReportCoverage
	All, Initial     ReportSample
	Retry            ReportSample
	Reports          []ReportView
	Metadata         []MetadataView
}

// RenderPublication projects one stored publication into its display form.
// Expected slots come from the reviewer; a slot without a proved report
// renders missing, and a first-execution failure stays distinct from an
// eventual retry success through the separate Initial and Retry samples.
func RenderPublication(pub StoredPublication, expected []ExpectedShardSlot) (PublicationView, error) {
	if !validStoredPublication(pub) {
		return PublicationView{}, ErrReportView
	}
	link := ReportLink{
		Source: pub.Attempt.Source, RepositoryID: pub.Attempt.RepositoryID, WorkflowID: pub.Attempt.WorkflowID,
		RunID: pub.Attempt.RunID, Attempt: pub.Attempt.Attempt,
	}
	view := PublicationView{
		Link: link, ReceivedAt: pub.ReceivedAt,
		JobLevelEvidence: len(pub.Reports) > 0,
		Reports:          make([]ReportView, 0, len(pub.Reports)),
		Metadata:         make([]MetadataView, 0, len(pub.Metadata)),
	}
	var all, initial, retry totalsBuilder
	for _, report := range pub.Reports {
		rowLink := link
		rowLink.ArtifactID, rowLink.JobID = report.ArtifactID, report.JobID
		view.Reports = append(view.Reports, ReportView{
			Link: rowLink, Shard: report.Shard, Retry: report.Retry,
			Totals: report.Totals, Timed: report.Timed, Mean: report.Mean, P95: report.P95,
		})
		all.add(report.Totals, report.Timed, report.TotalTimedDuration)
		if report.Retry == 0 {
			initial.add(report.Totals, report.Timed, report.TotalTimedDuration)
		} else {
			retry.add(report.Totals, report.Timed, report.TotalTimedDuration)
		}
	}
	for _, metadata := range pub.Metadata {
		rowLink := link
		rowLink.ArtifactID = metadata.ArtifactID
		view.Metadata = append(view.Metadata, MetadataView{Link: rowLink, RunMetadataOnly: true})
	}
	view.All = all.sample()
	view.Initial = initial.sample()
	view.Retry = retry.sample()
	view.Coverage = renderCoverage(pub, expected)
	slices.SortFunc(view.Reports, func(a, b ReportView) int {
		return cmp.Or(
			cmp.Compare(a.Link.ArtifactID, b.Link.ArtifactID),
			cmp.Compare(a.Shard, b.Shard),
			cmp.Compare(a.Retry, b.Retry),
		)
	})
	slices.SortFunc(view.Metadata, func(a, b MetadataView) int { return cmp.Compare(a.Link.ArtifactID, b.Link.ArtifactID) })
	return view, nil
}

func renderCoverage(pub StoredPublication, expected []ExpectedShardSlot) ReportCoverage {
	coverage := ReportCoverage{
		DeclaredArtifacts: len(pub.Reports) + len(pub.Metadata),
		ProvedArtifacts:   len(pub.Reports),
		MetadataArtifacts: len(pub.Metadata),
	}
	proved := make(map[ExpectedShardSlot]bool, len(pub.Reports))
	for _, report := range pub.Reports {
		proved[ExpectedShardSlot{report.Shard, report.Retry}] = true
	}
	seen := make(map[ExpectedShardSlot]bool, len(expected)+len(pub.Reports))
	for _, slot := range expected {
		if (slot.Shard != "" && !sourceAtom(slot.Shard)) || seen[slot] {
			continue // Invalid or repeated declarations do not fabricate slots.
		}
		seen[slot] = true
		coverage.Slots = append(coverage.Slots, ShardSlotCoverage{
			Shard: slot.Shard, Retry: slot.Retry, Proved: proved[slot], Missing: !proved[slot],
		})
	}
	for reportSlot := range proved {
		if !seen[reportSlot] {
			coverage.Slots = append(coverage.Slots, ShardSlotCoverage{Shard: reportSlot.Shard, Retry: reportSlot.Retry, Proved: true})
		}
	}
	slices.SortFunc(coverage.Slots, func(a, b ShardSlotCoverage) int {
		return cmp.Or(cmp.Compare(a.Shard, b.Shard), cmp.Compare(a.Retry, b.Retry))
	})
	return coverage
}

type totalsBuilder struct {
	totals Totals
	timed  int
	total  time.Duration
}

func (b *totalsBuilder) add(t Totals, timed int, total time.Duration) {
	b.totals.Cases += t.Cases
	b.totals.Passed += t.Passed
	b.totals.Failed += t.Failed
	b.totals.Errors += t.Errors
	b.totals.Skipped += t.Skipped
	b.timed += timed
	b.total += total
}

func (b totalsBuilder) sample() ReportSample { return reportSample(b.totals, b.timed, b.total) }

// CaseRankingView is one deterministic ranking row. Case identity is the
// reviewer-bound numeric cohort identity; display names never merge rows.
type CaseRankingView struct {
	Rank            int // one-based, stable for one input set
	CaseID, ShardID int64
	Comparison      CaseComparison
}

// RenderCaseRankings wraps the reviewed impact ranking with explicit rank
// positions. Comparisons stay within one verified category: mixed or
// incompatible cohorts reject the whole ranking instead of merging.
func RenderCaseRankings(inputs []ReviewedCaseImpactInput) ([]CaseRankingView, error) {
	ranked, err := RankReviewedCaseImpact(inputs)
	if err != nil {
		return nil, err
	}
	out := make([]CaseRankingView, 0, len(ranked))
	for i, row := range ranked {
		out = append(out, CaseRankingView{Rank: i + 1, CaseID: row.CaseID, ShardID: row.ShardID, Comparison: row.Comparison})
	}
	return out, nil
}
