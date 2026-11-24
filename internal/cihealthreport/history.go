package cihealthreport

import (
	"cmp"
	"errors"
	"slices"
	"time"
)

var ErrReportHistory = errors.New("report history unavailable")

// CaseObservationEvent is one execution of one reviewer-bound case in one
// retained report. It carries exact numeric source references only.
type CaseObservationEvent struct {
	CaseID   int64
	Link     ReportLink
	Shard    string
	Retry    uint16
	Outcome  Outcome
	Timed    bool
	Duration time.Duration
}

// RankedStoredCase is one deterministic ranking row over retained case
// observations. Ranking never merges cases across runs by name: identity is
// the reviewer-bound numeric case ID.
type RankedStoredCase struct {
	Rank          int // one-based, stable for one input set
	CaseID        int64
	Observations  int
	Failures      int
	TotalDuration time.Duration // exact sum of timed durations
	Runs          int           // distinct runs contributing observations
}

// CaseHistory is one case's retained executions across publications, ordered
// by run, attempt, retry and artifact. A failed first execution stays
// distinct from an eventual pass.
type CaseHistory struct {
	CaseID int64
	Events []CaseObservationEvent
	// FailedFirstExecution reports whether the earliest retained execution
	// failed; EventualPass reports whether any later execution passed.
	FailedFirstExecution bool
	EventualPass         bool
}

// RankStoredCases ranks reviewer-bound cases across retained publications.
// The caller supplies one verified comparable boundary: every publication
// must share one source/repository/workflow scope, and the caller must
// already have established that they are otherwise comparable. Mixed scopes,
// invalid stored data, empty input and observations beyond the comparison
// ceiling refuse the whole ranking.
func RankStoredCases(pubs []StoredPublication) ([]RankedStoredCase, error) {
	byCase, err := collectCaseObservations(pubs)
	if err != nil {
		return nil, err
	}
	out := make([]RankedStoredCase, 0, len(byCase))
	for caseID, events := range byCase {
		row := RankedStoredCase{CaseID: caseID, Observations: len(events)}
		runs := make(map[int64]bool, len(events))
		for _, event := range events {
			runs[event.Link.RunID] = true
			if event.Outcome == Failure {
				row.Failures++
			}
			if event.Timed {
				row.TotalDuration += event.Duration
			}
		}
		row.Runs = len(runs)
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b RankedStoredCase) int {
		return cmp.Or(
			cmp.Compare(b.Failures, a.Failures),
			cmp.Compare(b.TotalDuration, a.TotalDuration),
			cmp.Compare(b.Observations, a.Observations),
			cmp.Compare(a.CaseID, b.CaseID),
		)
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out, nil
}

// CaseHistories renders per-case retry history from retained publications,
// with the same comparable-boundary and ceiling rules as RankStoredCases.
func CaseHistories(pubs []StoredPublication) ([]CaseHistory, error) {
	byCase, err := collectCaseObservations(pubs)
	if err != nil {
		return nil, err
	}
	out := make([]CaseHistory, 0, len(byCase))
	for caseID, events := range byCase {
		slices.SortFunc(events, func(a, b CaseObservationEvent) int {
			return cmp.Or(
				cmp.Compare(a.Link.RunID, b.Link.RunID),
				cmp.Compare(a.Link.Attempt, b.Link.Attempt),
				cmp.Compare(a.Retry, b.Retry),
				cmp.Compare(a.Link.ArtifactID, b.Link.ArtifactID),
			)
		})
		history := CaseHistory{CaseID: caseID, Events: events}
		if len(events) > 0 {
			history.FailedFirstExecution = events[0].Outcome == Failure
			for _, event := range events[1:] {
				history.EventualPass = history.EventualPass || event.Outcome == Pass
			}
		}
		out = append(out, history)
	}
	slices.SortFunc(out, func(a, b CaseHistory) int { return cmp.Compare(a.CaseID, b.CaseID) })
	return out, nil
}

// collectCaseObservations validates the comparable boundary and projects
// every retained case observation once. Run-only metadata rows contribute no
// events: they carry no test-level evidence.
func collectCaseObservations(pubs []StoredPublication) (map[int64][]CaseObservationEvent, error) {
	if len(pubs) == 0 {
		return nil, ErrReportHistory
	}
	scope := pubs[0].Scope
	total := 0
	for _, pub := range pubs {
		if !validStoredPublication(pub) || pub.Scope != scope {
			return nil, ErrReportHistory
		}
		for _, report := range pub.Reports {
			total += len(report.Cases)
		}
	}
	if total == 0 || total > MaxComparisonObservations {
		return nil, ErrReportHistory
	}
	out := make(map[int64][]CaseObservationEvent, total)
	for _, pub := range pubs {
		base := ReportLink{
			Source: pub.Attempt.Source, RepositoryID: pub.Attempt.RepositoryID, WorkflowID: pub.Attempt.WorkflowID,
			RunID: pub.Attempt.RunID, Attempt: pub.Attempt.Attempt,
		}
		for _, report := range pub.Reports {
			for _, c := range report.Cases {
				link := base
				link.ArtifactID, link.JobID = report.ArtifactID, report.JobID
				out[c.CaseID] = append(out[c.CaseID], CaseObservationEvent{
					CaseID: c.CaseID, Link: link, Shard: report.Shard, Retry: report.Retry,
					Outcome: c.Outcome, Timed: c.Timed, Duration: c.Duration,
				})
			}
		}
	}
	return out, nil
}
