package demandrepair

import "time"

const ciRunnerAssignmentLifetime = 30 * time.Second

// Each exact attempt read gets a distinct non-zero-size seal. The seal
// retains no source, transport, token, locator, raw runner name or URL.
type ciRunnerAssignmentSeal struct{ tag byte }

// CIRunnerAssignmentReceipt records a numeric assignment positively reported
// by an exact GitHub attempt-jobs read. It does not prove historic coverage,
// uninterrupted job execution, a stock registration, or any Incus identity.
type CIRunnerAssignmentReceipt struct {
	// A nested fmt cannot format closure state by reflection. Keeping the
	// facts directly in this struct (even in unexported fields) would expose
	// gated evidence through a printable containing page.
	unseal func(CIAttempt, time.Time) (CIRunnerAssignmentFacts, bool)
}

type CIRunnerAssignmentFacts struct {
	SourceID                                                  string
	RepositoryID, WorkflowID, RunID, Attempt, JobID, RunnerID int64
	ObservedAt, ExpiresAt                                     time.Time
	StartedAt, EndedAt                                        time.Time
}

// RunnerAssignment selects evidence from the read itself, not the mutable
// public Jobs projection. The caller must retain the exact attempt proof.
func (p CIJobPage) RunnerAssignment(attempt CIAttempt, jobID int64) (CIRunnerAssignmentReceipt, bool) {
	now := time.Now()
	for _, receipt := range p.runnerAssignments {
		if facts, ok := receipt.evidence(attempt, now); ok && facts.JobID == jobID {
			return receipt, true
		}
	}
	return CIRunnerAssignmentReceipt{}, false
}

// Evidence rechecks the original concrete attempt and short local lifetime.
// Closing a different consumer or matching a source label cannot revoke or
// revive a receipt. Only this call releases the immutable numeric projection.
func (r CIRunnerAssignmentReceipt) Evidence(attempt CIAttempt) (CIRunnerAssignmentFacts, bool) {
	return r.evidence(attempt, time.Now())
}

func (r CIRunnerAssignmentReceipt) evidence(a CIAttempt, now time.Time) (CIRunnerAssignmentFacts, bool) {
	if r.unseal == nil {
		return CIRunnerAssignmentFacts{}, false
	}
	return r.unseal(a, now)
}

// Only the existing validated jobs-page loop can call this across the
// transport boundary. It deliberately does not inherit the legacy omitted
// run_attempt compatibility of the ordinary timing projection.
func mintCIRunnerAssignment(a CIAttempt, wire ciJobWire, observed time.Time) (CIRunnerAssignmentReceipt, bool) {
	var none CIRunnerAssignmentReceipt
	wall := observed.UTC()
	if a.runnerAssignmentSeal == nil || a.source == nil || a.source.id == "" || a.repository.GitHubID <= 0 ||
		a.run.WorkflowID <= 0 || a.run.RunID <= 0 || a.run.Attempt <= 0 || a.at.IsZero() || a.minted.IsZero() ||
		observed.Before(a.minted) || wall.Before(a.at) || wire.RunID != a.run.RunID || wire.RunAttempt == nil ||
		*wire.RunAttempt != a.run.Attempt || wire.ID <= 0 || wire.RunnerID <= 0 || wire.StartedAt == nil {
		return none, false
	}
	started := wire.StartedAt.UTC()
	if !ciAssignmentTime(started) || started.After(wall) {
		return none, false
	}
	if a.run.StartedAt != nil && started.Before(a.run.StartedAt.UTC()) {
		return none, false
	}
	var ended time.Time
	switch wire.Status {
	case "in_progress":
		if wire.Conclusion != "" || wire.CompletedAt != nil {
			return none, false
		}
	case "completed":
		if wire.CompletedAt == nil || wire.Conclusion == "" || wire.Conclusion == "skipped" ||
			!ciValidState(wire.Status, wire.Conclusion) {
			return none, false
		}
		ended = wire.CompletedAt.UTC()
		if !ciAssignmentTime(ended) || ended.Before(started) || ended.After(wall) {
			return none, false
		}
	default:
		return none, false
	}
	expiresMonotonic := observed.Add(ciRunnerAssignmentLifetime)
	if cutoff := a.minted.Add(ciAttemptLifetime); cutoff.Before(expiresMonotonic) {
		expiresMonotonic = cutoff
	}
	// Sampling wall time can move independently of the original attempt's
	// monotonic timestamp. Derive the two caps separately; a forward wall
	// shift must not extend the original attempt's absolute UTC lifetime.
	expiresWall := wall.Add(ciRunnerAssignmentLifetime)
	if cutoff := a.at.UTC().Add(ciAttemptLifetime); cutoff.Before(expiresWall) {
		expiresWall = cutoff
	}
	if !observed.Before(expiresMonotonic) || !ciAssignmentTime(wall) || !ciAssignmentTime(expiresWall) || !wall.Before(expiresWall) {
		return none, false
	}
	facts := CIRunnerAssignmentFacts{
		SourceID: a.source.id, RepositoryID: a.repository.GitHubID, WorkflowID: a.run.WorkflowID,
		RunID: a.run.RunID, Attempt: a.run.Attempt, JobID: wire.ID, RunnerID: wire.RunnerID,
		ObservedAt: wall, ExpiresAt: expiresWall, StartedAt: started, EndedAt: ended,
	}
	seal := a.runnerAssignmentSeal // Do not capture the source-bearing attempt.
	return CIRunnerAssignmentReceipt{unseal: func(proof CIAttempt, now time.Time) (CIRunnerAssignmentFacts, bool) {
		if proof.runnerAssignmentSeal != seal || proof.source == nil ||
			facts.SourceID != proof.source.id || facts.RepositoryID != proof.repository.GitHubID ||
			facts.WorkflowID != proof.run.WorkflowID || facts.RunID != proof.run.RunID || facts.Attempt != proof.run.Attempt ||
			now.Before(observed) || !now.Before(expiresMonotonic) ||
			now.UTC().Before(facts.ObservedAt) || !now.UTC().Before(facts.ExpiresAt) {
			return CIRunnerAssignmentFacts{}, false
		}
		return facts, true
	}}, true
}

func ciAssignmentTime(t time.Time) bool {
	y := t.UTC().Year()
	return !t.IsZero() && y >= 1 && y <= 9999
}
