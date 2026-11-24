package cihealth

import (
	"errors"
	"testing"
	"time"
)

var retainedViewScope = HistoryScope{Source: "fixture", RepositoryID: 9007199254740993, WorkflowID: 44}

func copyViewAttempt(a AttemptFacts) AttemptFacts {
	out := a
	out.Jobs = append([]JobFacts(nil), a.Jobs...)
	for i := range out.Jobs {
		out.Jobs[i].Steps = append([]StepFacts(nil), a.Jobs[i].Steps...)
	}
	return out
}

func retainedViewAttempt(run, attempt int64, at time.Time) AttemptFacts {
	key := AttemptKey{retainedViewScope.Source, retainedViewScope.RepositoryID, retainedViewScope.WorkflowID, run, attempt}
	started := at.Add(-4 * time.Minute)
	completed := at.Add(-time.Minute)
	return AttemptFacts{
		Key: key, AsOf: at,
		Clock: Clock{Phase: Completed, StartedAt: started, CompletedAt: completed, ObservedAt: at, Conclusion: ConclusionSuccess},
		Jobs: []JobFacts{{
			Key:   JobKey{Attempt: key, JobID: 7000000000000000003},
			Clock: Clock{Phase: Completed, StartedAt: started, CompletedAt: completed, ObservedAt: at, Conclusion: ConclusionSuccess},
			Steps: []StepFacts{{
				Key:   StepKey{Job: JobKey{Attempt: key, JobID: 7000000000000000003}, Number: 2},
				Clock: Clock{Phase: Completed, StartedAt: started, CompletedAt: completed, ObservedAt: at, Conclusion: ConclusionSuccess},
			}},
		}},
	}
}

// The retained view must keep a previously measured result visible when a
// later terminal observation omits its endpoints, attribute that value to
// the observation that measured it, and leave the newer row visibly
// incomplete without blocking unrelated rows.
func TestRetainedViewRecoversPriorMeasurementForSparseLaterObservation(t *testing.T) {
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	later := at.Add(2 * time.Minute)
	measured := retainedViewAttempt(20, 1, at)
	sparse := copyViewAttempt(measured)
	sparse.AsOf = later
	sparse.Clock.StartedAt, sparse.Clock.CompletedAt = time.Time{}, time.Time{}
	sparse.Jobs[0].Clock.StartedAt, sparse.Jobs[0].Clock.CompletedAt = time.Time{}, time.Time{}
	sparse.Jobs[0].Steps[0].Clock.StartedAt, sparse.Jobs[0].Steps[0].Clock.CompletedAt = time.Time{}, time.Time{}
	// A genuinely new, still-unmeasured job arrives in the later observation.
	extraJob := sparse.Jobs[0]
	extraJob.Key.JobID++
	extraJob.Steps = nil
	extraJob.Clock.Conclusion = ConclusionUnknown
	sparse.Jobs = append(sparse.Jobs, extraJob)

	view, err := ProjectRetainedAttempt([]AttemptFacts{sparse, measured}) // Any order.
	if err != nil {
		t.Fatal(err)
	}
	if view.Key != measured.Key || !view.AsOf.Equal(later) || view.Phase != Completed || view.Conclusion != ConclusionSuccess {
		t.Fatalf("latest version is not the status authority: %+v", view)
	}
	if view.Wall != (MeasuredValue{Span: Span{Basis: ObservedEndpoints, Duration: 3 * time.Minute}, MeasuredAt: at}) || view.Wall.Current {
		t.Fatalf("wall not recovered from the measuring observation: %+v", view.Wall)
	}
	if len(view.Jobs) != 2 {
		t.Fatalf("latest row set not authoritative: %+v", view.Jobs)
	}
	job, extra := view.Jobs[0], view.Jobs[1]
	if job.Key.JobID != measured.Jobs[0].Key.JobID || job.Execution.MeasuredAt != at || job.Execution.Current {
		t.Fatalf("job execution not recovered with provenance: %+v", job.Execution)
	}
	if job.Steps[0].Duration.MeasuredAt != at || job.Steps[0].Duration.Current {
		t.Fatalf("step duration not recovered with provenance: %+v", job.Steps[0].Duration)
	}
	if extra.Key.JobID == job.Key.JobID || extra.Execution.MeasuredAt != (time.Time{}) || extra.Execution.Current || extra.Execution.Basis != Unavailable {
		t.Fatalf("new unmeasured row must stay visibly incomplete, not filled: %+v", extra.Execution)
	}
	if !errors.Is(func() error { _, err := Measure(sparse); return err }(), nil) || sparse.AsOf != later {
		t.Fatal("projection mutated its input versions")
	}
	// Freshness anchor: age at render time is measured from the version that
	// supplied the value, not from the latest observation.
	if age := later.Sub(view.Wall.MeasuredAt); age != 2*time.Minute {
		t.Fatalf("wrong freshness anchor: %v", age)
	}
}

func TestRetainedViewCurrentAttemptsAndIdenticalMatrixLabelsStayDistinct(t *testing.T) {
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	initial := retainedViewAttempt(30, 1, at)
	initial.Clock.Conclusion = ConclusionFailure
	initial.Jobs[0].Clock.Conclusion = ConclusionFailure
	initial.Jobs[0].Steps[0].Clock.Conclusion = ConclusionFailure
	retry := retainedViewAttempt(30, 2, at.Add(time.Minute)) // Same run, higher attempt.
	// Two matrix jobs can carry the exact same label; only numeric IDs exist.
	twinA := copyViewAttempt(initial).Jobs[0]
	twinA.Key.Attempt = retry.Key
	twinA.Clock = retry.Jobs[0].Clock
	twinA.Key.JobID++
	twinA.Steps = nil
	twinB := twinA
	twinB.Key.JobID++
	retry.Jobs = append(retry.Jobs, twinA, twinB)

	for _, versions := range [][]AttemptFacts{{initial}, {retry}} {
		view, err := ProjectRetainedAttempt(versions)
		if err != nil {
			t.Fatal(err)
		}
		if view.Key.Attempt == 1 && view.Conclusion != ConclusionFailure || view.Key.Attempt == 2 && view.Conclusion != ConclusionSuccess {
			t.Fatalf("attempt identity or result blurred: %+v", view)
		}
	}
	one, err := ProjectRetainedAttempt([]AttemptFacts{initial})
	if err != nil {
		t.Fatal(err)
	}
	two, err := ProjectRetainedAttempt([]AttemptFacts{retry})
	if err != nil {
		t.Fatal(err)
	}
	if one.Key == two.Key || one.Jobs[0].Key == two.Jobs[1].Key || two.Jobs[1].Key == two.Jobs[2].Key || len(two.Jobs) != 3 {
		t.Fatalf("exact identities or equal-label twins conflated: %+v / %+v", one.Key, two.Jobs)
	}
}

func TestRetainedViewNeverDerivesQueueFromJobCreation(t *testing.T) {
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a := retainedViewAttempt(40, 1, at)
	a.Jobs[0].CreatedAt = at.Add(-time.Hour) // Live-only field; must stay invisible.
	a.Jobs[0].Clock.StartedAt, a.Jobs[0].Clock.CompletedAt = time.Time{}, time.Time{}
	a.Jobs[0].QueuedAt, a.Jobs[0].FirstQueuedObservedAt = time.Time{}, time.Time{}
	view, err := ProjectRetainedAttempt([]AttemptFacts{a})
	if err != nil {
		t.Fatal(err)
	}
	if view.Jobs[0].Queue != (MeasuredValue{}) || view.Jobs[0].ActiveQueue != (MeasuredValue{}) {
		t.Fatalf("queue wait derived from job creation: %+v", view.Jobs[0])
	}
}

func TestRetainedViewDistinguishesSkippedZeroAndMissingTime(t *testing.T) {
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a := retainedViewAttempt(50, 1, at)
	zero := a.Jobs[0]
	zero.Key.JobID++
	zero.Clock.StartedAt = zero.Clock.CompletedAt // Genuine zero execution.
	zero.Steps = nil
	skipped := zero
	skipped.Key.JobID++
	skipped.Clock = Clock{Phase: Skipped, ObservedAt: at, Conclusion: ConclusionOtherTerminal}
	a.Jobs = append(a.Jobs, zero, skipped)
	view, err := ProjectRetainedAttempt([]AttemptFacts{a})
	if err != nil {
		t.Fatal(err)
	}
	got := view.Jobs
	if got[1].Execution != (MeasuredValue{Span: Span{Basis: ObservedEndpoints, Duration: 0}, MeasuredAt: at, Current: true}) {
		t.Fatalf("genuine zero execution became missing or vice versa: %+v", got[1].Execution)
	}
	if got[2].Execution != (MeasuredValue{}) || got[2].Phase != Skipped {
		t.Fatalf("skipped job must not carry a duration: %+v", got[2])
	}
}

func TestRetainedViewRejectsInvalidVersionSets(t *testing.T) {
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	valid := retainedViewAttempt(60, 1, at)
	foreign := valid
	foreign.Key.RunID++
	duplicate := valid
	broken := valid
	broken.Jobs[0].Key.JobID = 0
	for name, versions := range map[string][]AttemptFacts{
		"empty":         {},
		"mixed keys":    {valid, foreign},
		"same time":     {valid, duplicate},
		"invalid facts": {broken},
	} {
		t.Run(name, func(t *testing.T) {
			if view, err := ProjectRetainedAttempt(versions); !errors.Is(err, ErrTimingFacts) || view.Key != (AttemptKey{}) {
				t.Fatalf("expected refusal, got %+v, %v", view, err)
			}
		})
	}
}
