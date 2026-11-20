// Package cihealthsource projects one authenticated GitHub attempt into the CI
// timing model. It does not schedule, retain, publish or act on observations.
package cihealthsource

import (
	"context"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

var ErrAttemptObservation = errors.New("CI attempt timing observation unavailable")

const attemptReadDeadline = 30 * time.Second

// AttemptSource is the narrow read interface for explicit owner startup
// composition. An implementation does not confer authorization by itself.
type AttemptSource interface {
	SourceID() string
	ReadCIAttempt(context.Context, string, int64, int64) (demandrepair.CIAttempt, error)
	ListCIAttemptJobs(context.Context, demandrepair.CIAttempt, int) (demandrepair.CIJobPage, error)
}

// Observation is one bounded, non-atomic set of reported rows for an exact
// attempt. Ending pagination does not prove stable or complete job visibility.
// No display names, runner identifiers, URLs or arbitrary results are retained.
type Observation struct {
	Timing  cihealth.Timing
	facts   cihealth.AttemptFacts
	runLink demandrepair.WorkflowRunLink
}

// Facts returns an owned copy of validated model inputs for a separate
// publication/store owner. Modifying that copy cannot alter this observation.
func (o Observation) Facts() cihealth.AttemptFacts { return cloneFacts(o.facts) }

func cloneFacts(in cihealth.AttemptFacts) cihealth.AttemptFacts {
	out := in
	out.Jobs = append([]cihealth.JobFacts(nil), in.Jobs...)
	for i := range out.Jobs {
		out.Jobs[i].Steps = append([]cihealth.StepFacts(nil), in.Jobs[i].Steps...)
	}
	return out
}

type readBudget struct{ remaining int }

func (b *readBudget) take(n int) bool {
	if b == nil {
		return true
	}
	if n > b.remaining {
		return false
	}
	b.remaining -= n
	return true
}

var errReadLimit = errors.New("CI observation reached its local read limit")

// ReadAttempt mints one exact source-bound attempt proof and walks only that
// attempt's consecutive job pages. Locators are checked against the existing
// source enrollment; nothing here grants visibility or changes that enrollment.
func ReadAttempt(ctx context.Context, source AttemptSource, garmRepo string, runID, attempt int64) (Observation, error) {
	out, err := readAttempt(ctx, source, cycleSourceInstance{}, garmRepo, runID, attempt, nil, cihealth.MaxJobs, cihealth.MaxTotalSteps)
	if errors.Is(err, errReadLimit) {
		return Observation{}, ErrAttemptObservation
	}
	return out, err
}

func readAttempt(ctx context.Context, source AttemptSource, owner cycleSourceInstance, garmRepo string, runID, attempt int64, budget *readBudget, maxJobs, maxSteps int) (Observation, error) {
	if ctx == nil || ctx.Err() != nil || source == nil || runID <= 0 || attempt <= 0 {
		return Observation{}, ErrAttemptObservation
	}
	ctx, cancel := context.WithTimeout(ctx, attemptReadDeadline)
	defer cancel()

	if !budget.take(1) {
		return Observation{}, errReadLimit
	}
	proof, err := source.ReadCIAttempt(ctx, garmRepo, runID, attempt)
	if err != nil {
		return Observation{}, ErrAttemptObservation
	}
	run := proof.Run()
	key := cihealth.AttemptKey{
		Source: source.SourceID(), RepositoryID: proof.RepositoryID(),
		WorkflowID: run.WorkflowID, RunID: run.RunID, Attempt: run.Attempt,
	}
	if key.RunID != runID || key.Attempt != attempt || key.WorkflowID <= 0 || key.RepositoryID <= 0 || key.Source == "" || proof.ObservedAt().IsZero() {
		return Observation{}, ErrAttemptObservation
	}

	facts := cihealth.AttemptFacts{Key: key, AsOf: proof.ObservedAt()}
	facts.Clock = sourceClock(run.Status, run.Conclusion, run.StartedAt, nil, proof.ObservedAt())
	// GitHub can supply a run start-looking value before execution. There is
	// no evidenced execution boundary while the run is waiting to start.
	if facts.Clock.Phase == cihealth.Queued || facts.Clock.Phase == cihealth.Unknown {
		facts.Clock.StartedAt = time.Time{}
	}
	seenJobs := make(map[int64]bool)
	stepCount, reportedTotal := 0, -1
	for page := 1; page <= demandrepair.CIMaxPages; page++ {
		if !budget.take(1) {
			return Observation{}, errReadLimit
		}
		rows, err := source.ListCIAttemptJobs(ctx, proof, page)
		if err != nil || ctx.Err() != nil || rows.ObservedAt.IsZero() ||
			rows.ObservedAt.Before(facts.AsOf) || (reportedTotal >= 0 && rows.TotalCount != reportedTotal) ||
			(rows.NextPage != 0 && (page == demandrepair.CIMaxPages || rows.NextPage != page+1)) {
			return Observation{}, ErrAttemptObservation
		}
		reportedTotal = rows.TotalCount // Equal totals cannot prove atomic pagination.
		for _, row := range rows.Jobs {
			if row.RunID != key.RunID || row.RunAttempt != key.Attempt || row.ID <= 0 ||
				seenJobs[row.ID] || len(row.Steps) > cihealth.MaxStepsPerJob {
				return Observation{}, ErrAttemptObservation
			}
			seenJobs[row.ID] = true
			stepCount += len(row.Steps)
		}
		if len(rows.Jobs) > maxJobs-len(facts.Jobs) || stepCount > maxSteps {
			return Observation{}, errReadLimit
		}
		facts.AsOf = rows.ObservedAt
		for _, row := range rows.Jobs {
			jobKey := cihealth.JobKey{Attempt: key, JobID: row.ID}
			job := cihealth.JobFacts{
				Key: jobKey, Clock: sourceClock(row.Status, row.Conclusion, row.StartedAt, row.CompletedAt, rows.ObservedAt),
				Steps: make([]cihealth.StepFacts, 0, len(row.Steps)),
			}
			// GitHub jobs in these endpoints supply neither a proven queue
			// transition nor a historical positive queue sighting.
			for _, step := range row.Steps {
				job.Steps = append(job.Steps, cihealth.StepFacts{
					Key:   cihealth.StepKey{Job: jobKey, Number: step.Number},
					Clock: sourceClock(step.Status, step.Conclusion, step.StartedAt, step.CompletedAt, rows.ObservedAt),
				})
			}
			facts.Jobs = append(facts.Jobs, job)
		}
		if rows.NextPage == 0 {
			out, err := cihealth.Measure(facts)
			if err != nil || ctx.Err() != nil {
				return Observation{}, ErrAttemptObservation
			}
			link := owner.prepareWorkflowRunLink(ctx, proof)
			if ctx.Err() != nil {
				return Observation{}, ErrAttemptObservation
			}
			return Observation{Timing: out, facts: facts, runLink: link}, nil
		}
	}
	return Observation{}, ErrAttemptObservation
}

func sourceClock(status, conclusion string, started, completed *time.Time, at time.Time) cihealth.Clock {
	clock := cihealth.Clock{ObservedAt: at, Conclusion: sourceConclusion(conclusion)}
	switch {
	case status == "skipped" || status == "completed" && conclusion == "skipped":
		clock.Phase = cihealth.Skipped
	case status == "completed":
		clock.Phase = cihealth.Completed
	case status == "in_progress":
		clock.Phase = cihealth.Running
	case status == "queued":
		clock.Phase = cihealth.Queued
		// Requested, waiting and pending are not positive queue evidence.
	}
	if started != nil {
		clock.StartedAt = *started
	}
	if completed != nil {
		clock.CompletedAt = *completed
	}
	return clock
}

// sourceConclusion only projects the conclusions admitted by the existing
// authenticated CI source. An absent result remains unknown even on a record
// with a terminal phase or duration.
func sourceConclusion(conclusion string) cihealth.Conclusion {
	switch conclusion {
	case "success":
		return cihealth.ConclusionSuccess
	case "failure":
		return cihealth.ConclusionFailure
	case "cancelled":
		return cihealth.ConclusionCancelled
	case "timed_out":
		return cihealth.ConclusionTimedOut
	case "neutral", "skipped", "action_required", "stale", "startup_failure":
		return cihealth.ConclusionOtherTerminal
	default:
		return cihealth.ConclusionUnknown
	}
}
