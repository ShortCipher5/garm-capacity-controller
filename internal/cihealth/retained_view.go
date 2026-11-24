package cihealth

import (
	"sort"
	"time"
)

// MeasuredValue is one retained timing value with the provenance of the exact
// observation version that supplied it. A value is never copied forward as a
// fresh field of a newer record: it stays attributed to the observation that
// actually measured it.
type MeasuredValue struct {
	Span
	// MeasuredAt is the observation time (AttemptFacts.AsOf) of the retained
	// version whose endpoints supplied this value. It is the freshness anchor:
	// a render at wall time W displays age W-MeasuredAt. It is zero when no
	// retained version measured this value.
	MeasuredAt time.Time
	// Current is true when the latest retained version of the attempt itself
	// measured this value. When false, the value was recovered from an earlier
	// retained version because the latest observation omitted its endpoints;
	// that is the reason for uncertainty, and the value must be rendered with
	// its own observation time, never as fresh telemetry.
	Current bool
}

// RetainedAttemptView is one attempt's display projection over its retained
// observation versions. The phase, result and the job/step row set come from
// the latest version; queue, execution, wall and step durations are rendered
// separately, each with the exact observation that measured it. A row whose
// latest observation omitted its endpoints stays visibly incomplete instead
// of borrowing the newer record's timestamps.
type RetainedAttemptView struct {
	Key        AttemptKey
	AsOf       time.Time // latest observation time; retention windows and row freshness anchor
	Phase      Phase
	Conclusion Conclusion
	ObservedAt time.Time // latest attempt status sighting, never a render time
	Wall       MeasuredValue
	Active     MeasuredValue
	Jobs       []RetainedJobView
}

type RetainedJobView struct {
	Key JobKey
	// Queue evidence belongs to the retained row: it uses the observed queue
	// endpoints or a positive queued sighting, never job or run creation.
	Phase       Phase
	Conclusion  Conclusion
	ObservedAt  time.Time
	Queue       MeasuredValue
	ActiveQueue MeasuredValue
	Execution   MeasuredValue
	Active      MeasuredValue
	Steps       []RetainedStepView
}

type RetainedStepView struct {
	Key        StepKey
	Phase      Phase
	Conclusion Conclusion
	Duration   MeasuredValue
	Active     MeasuredValue
}

// ProjectRetainedAttempt reduces the retained observation versions of one
// exact attempt. Versions may arrive in any order; each must Measure and
// share the same key with a distinct observation time. The newest version is
// the status and result authority and defines which jobs and steps are
// current rows; earlier versions only backfill a timing value that the newest
// observation kept but could not measure. They never resurrect jobs or steps
// the newest observation dropped, and they never change a known result.
func ProjectRetainedAttempt(versions []AttemptFacts) (RetainedAttemptView, error) {
	if len(versions) == 0 {
		return RetainedAttemptView{}, ErrTimingFacts
	}
	sorted := append(make([]AttemptFacts, 0, len(versions)), versions...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].AsOf.After(sorted[j].AsOf) })
	key := sorted[0].Key
	timings := make([]Timing, len(sorted))
	for i, v := range sorted {
		if v.Key != key || i > 0 && sorted[i-1].AsOf.Equal(v.AsOf) {
			return RetainedAttemptView{}, ErrTimingFacts
		}
		t, err := Measure(v)
		if err != nil {
			return RetainedAttemptView{}, err
		}
		timings[i] = t
	}
	latest := sorted[0]
	out := RetainedAttemptView{
		Key: key, AsOf: latest.AsOf, Phase: latest.Clock.Phase, Conclusion: latest.Clock.Conclusion,
		ObservedAt: latest.Clock.ObservedAt,
		Wall:       retainedValue(timings, sorted, func(t Timing) Span { return t.Wall }),
		Active:     retainedValue(timings, sorted, func(t Timing) Span { return t.Active }),
		Jobs:       make([]RetainedJobView, 0, len(latest.Jobs)),
	}
	for _, current := range timings[0].Jobs {
		row := RetainedJobView{
			Key: current.Key, Phase: current.Phase, Conclusion: current.Conclusion,
			Queue:       retainedJobValue(timings, sorted, current.Key.JobID, func(j JobTiming) Span { return j.Queue }),
			ActiveQueue: retainedJobValue(timings, sorted, current.Key.JobID, func(j JobTiming) Span { return j.ActiveQueue }),
			Execution:   retainedJobValue(timings, sorted, current.Key.JobID, func(j JobTiming) Span { return j.Execution }),
			Active:      retainedJobValue(timings, sorted, current.Key.JobID, func(j JobTiming) Span { return j.Active }),
		}
		for _, step := range current.Steps {
			row.Steps = append(row.Steps, RetainedStepView{
				Key: step.Key, Phase: step.Phase, Conclusion: step.Conclusion,
				Duration: retainedStepValue(timings, sorted, step.Key, func(s StepTiming) Span { return s.Duration }),
				Active:   retainedStepValue(timings, sorted, step.Key, func(s StepTiming) Span { return s.Active }),
			})
		}
		out.Jobs = append(out.Jobs, row)
	}
	return out, nil
}

// retainedValue scans versions newest first and returns the first measured
// span, attributing it to the version that supplied it.
func retainedValue(timings []Timing, sorted []AttemptFacts, pick func(Timing) Span) MeasuredValue {
	for i, t := range timings {
		if s := pick(t); s.Basis != Unavailable {
			return MeasuredValue{Span: s, MeasuredAt: sorted[i].AsOf, Current: i == 0}
		}
	}
	return MeasuredValue{}
}

func retainedJobValue(timings []Timing, sorted []AttemptFacts, jobID int64, pick func(JobTiming) Span) MeasuredValue {
	for i, t := range timings {
		for _, j := range t.Jobs {
			if j.Key.JobID == jobID {
				if s := pick(j); s.Basis != Unavailable {
					return MeasuredValue{Span: s, MeasuredAt: sorted[i].AsOf, Current: i == 0}
				}
			}
		}
	}
	return MeasuredValue{}
}

func retainedStepValue(timings []Timing, sorted []AttemptFacts, key StepKey, pick func(StepTiming) Span) MeasuredValue {
	for i, t := range timings {
		for _, j := range t.Jobs {
			if j.Key != key.Job {
				continue
			}
			for _, s := range j.Steps {
				if s.Key == key {
					if v := pick(s); v.Basis != Unavailable {
						return MeasuredValue{Span: v, MeasuredAt: sorted[i].AsOf, Current: i == 0}
					}
				}
			}
		}
	}
	return MeasuredValue{}
}
