// Package cihealth derives display timing from scoped GitHub facts. It neither
// collects nor authorizes those facts and has no connection to runner control.
package cihealth

import (
	"errors"
	"time"
)

const (
	MaxJobs        = 1000
	MaxStepsPerJob = 1000
	MaxTotalSteps  = 10000
)

var ErrTimingFacts = errors.New("invalid or oversized CI timing facts")

// All numeric IDs are lossless signed 64-bit values. An eventual web adapter
// must keep IDs as decimal strings, never in JavaScript Number values.
type AttemptKey struct {
	// Source is an opaque configured GitHub source label, not a URL or secret.
	Source                                   string
	RepositoryID, WorkflowID, RunID, Attempt int64
}

type JobKey struct {
	Attempt AttemptKey
	JobID   int64
}

type StepKey struct {
	Job    JobKey
	Number int64
}

// Phase classifies only the timing-relevant lifecycle facts. Skipped is an
// explicit result, not a synonym for failed, canceled or missing.
type Phase uint8

const (
	Unknown Phase = iota
	Queued
	Running
	Completed
	Skipped
)

// Conclusion is an explicit, normalized source result, independent of timing.
type Conclusion string

const (
	ConclusionUnknown       Conclusion = ""
	ConclusionSuccess       Conclusion = "success"
	ConclusionFailure       Conclusion = "failure"
	ConclusionCancelled     Conclusion = "cancelled"
	ConclusionTimedOut      Conclusion = "timed_out"
	ConclusionOtherTerminal Conclusion = "other_terminal"
)

func (c Conclusion) validFor(p Phase) bool {
	switch c {
	case ConclusionUnknown:
		return true
	case ConclusionOtherTerminal:
		return p == Completed || p == Skipped
	case ConclusionSuccess, ConclusionFailure, ConclusionCancelled, ConclusionTimedOut:
		return p == Completed
	default:
		return false
	}
}

// Clock uses the zero Time for an unavailable value. A completed record can
// lack one or both boundaries. A clock cannot be constructed from a run's
// creation or last-update time just because another timestamp is missing.
type Clock struct {
	Phase                  Phase
	StartedAt, CompletedAt time.Time
	// ObservedAt belongs to this entity's status sighting, not the overall
	// batch or page render. Without it no active elapsed interval is reported.
	ObservedAt time.Time
	Conclusion Conclusion `json:",omitempty"`
}

type AttemptFacts struct {
	Key   AttemptKey
	AsOf  time.Time // time of this evidence, never the page render time
	Clock Clock
	Jobs  []JobFacts
}

type JobFacts struct {
	Key   JobKey
	Clock Clock
	// QueuedAt is only supplied by a source that actually establishes the
	// queue transition. FirstQueuedObservedAt is a positive queued sighting
	// tied to this exact job and attempt, never an inferred run creation time.
	QueuedAt, FirstQueuedObservedAt time.Time
	Steps                           []StepFacts
}

type StepFacts struct {
	Key   StepKey
	Clock Clock
}

type Basis uint8

const (
	Unavailable Basis = iota
	ObservedEndpoints
	ObservedLowerBound
)

// Span distinguishes known zero (two provided equal timestamps) from missing
// evidence. A lower bound must not be combined with observed-endpoint samples.
type Span struct {
	Basis    Basis
	Duration time.Duration
}

type Timing struct {
	Key        AttemptKey
	AsOf       time.Time
	Phase      Phase
	Conclusion Conclusion
	Wall       Span
	Active     Span
	Jobs       []JobTiming
}

type JobTiming struct {
	Key         JobKey
	Phase       Phase
	Conclusion  Conclusion
	Queue       Span
	ActiveQueue Span
	Execution   Span
	Active      Span
	Steps       []StepTiming
}

type StepTiming struct {
	Key        StepKey
	Phase      Phase
	Conclusion Conclusion
	Duration   Span
	Active     Span
}

// Measure validates and projects one attempt's observations. Jobs and steps
// need not be exhaustive or contiguous. Missing rows do not imply completion,
// and equal display names across jobs/attempts cannot join or deduplicate them.
func Measure(in AttemptFacts) (Timing, error) {
	if !validKey(in.Key) || !validTime(in.AsOf) || len(in.Jobs) > MaxJobs {
		return Timing{}, ErrTimingFacts
	}
	stepCount := 0
	for _, job := range in.Jobs {
		n := len(job.Steps)
		if n > MaxStepsPerJob || n > MaxTotalSteps-stepCount {
			return Timing{}, ErrTimingFacts
		}
		stepCount += n
	}
	wall, active, ok := measureClock(in.Clock, in.AsOf)
	if !ok {
		return Timing{}, ErrTimingFacts
	}
	out := Timing{Key: in.Key, AsOf: in.AsOf, Phase: in.Clock.Phase, Conclusion: in.Clock.Conclusion, Wall: wall, Active: active, Jobs: make([]JobTiming, 0, len(in.Jobs))}
	seen := make(map[int64]bool, len(in.Jobs))
	for _, job := range in.Jobs {
		if job.Key.Attempt != in.Key || job.Key.JobID <= 0 || seen[job.Key.JobID] || !contains(in.Clock, job.Clock) {
			return Timing{}, ErrTimingFacts
		}
		seen[job.Key.JobID] = true
		row, ok := measureJob(job, in.Clock, in.AsOf)
		if !ok {
			return Timing{}, ErrTimingFacts
		}
		out.Jobs = append(out.Jobs, row)
	}
	return out, nil
}

func measureJob(job JobFacts, attempt Clock, asOf time.Time) (JobTiming, bool) {
	execution, active, ok := measureClock(job.Clock, asOf)
	if !ok || !validOptional(job.QueuedAt, asOf) || !validOptional(job.FirstQueuedObservedAt, asOf) ||
		!ordered(job.QueuedAt, job.FirstQueuedObservedAt) ||
		!ordered(job.QueuedAt, job.Clock.StartedAt) || !ordered(job.FirstQueuedObservedAt, job.Clock.StartedAt) ||
		!ordered(job.QueuedAt, job.Clock.CompletedAt) || !ordered(job.FirstQueuedObservedAt, job.Clock.CompletedAt) ||
		!ordered(job.QueuedAt, job.Clock.ObservedAt) || !ordered(job.FirstQueuedObservedAt, job.Clock.ObservedAt) {
		return JobTiming{}, false
	}
	out := JobTiming{Key: job.Key, Phase: job.Clock.Phase, Conclusion: job.Clock.Conclusion, Execution: execution, Active: active, Steps: make([]StepTiming, 0, len(job.Steps))}
	queued := job.QueuedAt
	basis := ObservedEndpoints
	if queued.IsZero() {
		queued = job.FirstQueuedObservedAt
		basis = ObservedLowerBound
	}
	switch job.Clock.Phase {
	case Queued:
		out.ActiveQueue, ok = span(queued, job.Clock.ObservedAt, basis)
	case Running, Completed:
		out.Queue, ok = span(queued, job.Clock.StartedAt, basis)
	}
	if !ok {
		return JobTiming{}, false
	}
	seen := make(map[int64]bool, len(job.Steps))
	for _, step := range job.Steps {
		if step.Key.Job != job.Key || step.Key.Number <= 0 || seen[step.Key.Number] || !contains(job.Clock, step.Clock) || !contains(attempt, step.Clock) {
			return JobTiming{}, false
		}
		seen[step.Key.Number] = true
		finished, elapsed, ok := measureClock(step.Clock, asOf)
		if !ok {
			return JobTiming{}, false
		}
		out.Steps = append(out.Steps, StepTiming{Key: step.Key, Phase: step.Clock.Phase, Conclusion: step.Clock.Conclusion, Duration: finished, Active: elapsed})
	}
	return out, true
}

func measureClock(c Clock, asOf time.Time) (finished, active Span, ok bool) {
	if c.Phase > Skipped || !c.Conclusion.validFor(c.Phase) || !validOptional(c.StartedAt, asOf) || !validOptional(c.CompletedAt, asOf) ||
		!validOptional(c.ObservedAt, asOf) || !ordered(c.StartedAt, c.CompletedAt) ||
		!ordered(c.StartedAt, c.ObservedAt) || !ordered(c.CompletedAt, c.ObservedAt) ||
		(c.Phase == Queued && (!c.StartedAt.IsZero() || !c.CompletedAt.IsZero())) ||
		(c.Phase == Running && !c.CompletedAt.IsZero()) {
		return Span{}, Span{}, false
	}
	switch c.Phase {
	case Completed:
		finished, ok = span(c.StartedAt, c.CompletedAt, ObservedEndpoints)
	case Running:
		active, ok = span(c.StartedAt, c.ObservedAt, ObservedEndpoints)
	default:
		ok = true // No timing supplied for skipped, queued or unknown execution.
	}
	return
}

// Where present, the parent's exact execution boundaries constrain child
// execution, not job queue time. Missing parent clocks impose no bound.
func contains(parent, child Clock) bool {
	if parent.Phase == Queued || parent.Phase == Skipped {
		return child.Phase == Unknown || child.Phase == Skipped ||
			(parent.Phase == Queued && child.Phase == Queued)
	}
	if parent.Phase == Completed && (child.Phase == Running || child.Phase == Queued) {
		return false
	}
	return ordered(parent.StartedAt, child.StartedAt) && ordered(parent.StartedAt, child.CompletedAt) &&
		ordered(child.StartedAt, parent.CompletedAt) && ordered(child.CompletedAt, parent.CompletedAt)
}

func span(from, to time.Time, basis Basis) (Span, bool) {
	if from.IsZero() || to.IsZero() {
		return Span{}, true
	}
	from, to = from.UTC(), to.UTC()
	d := to.Sub(from)
	// Time.Sub saturates on overflow, which is not a valid observation.
	if d < 0 || !from.Add(d).Equal(to) {
		return Span{}, false
	}
	return Span{Basis: basis, Duration: d}, true
}

func validKey(k AttemptKey) bool {
	return validSource(k.Source) && k.RepositoryID > 0 && k.WorkflowID > 0 && k.RunID > 0 && k.Attempt > 0
}

func validSource(source string) bool {
	if len(source) == 0 || len(source) > 64 {
		return false
	}
	for _, c := range source {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validTime(t time.Time) bool {
	year := t.UTC().Year()
	return !t.IsZero() && year >= 1 && year <= 9999
}
func validOptional(t, asOf time.Time) bool {
	return t.IsZero() || validTime(t) && !t.UTC().After(asOf.UTC())
}
func ordered(before, after time.Time) bool {
	return before.IsZero() || after.IsZero() || !before.UTC().After(after.UTC())
}
