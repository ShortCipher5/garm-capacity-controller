package cihealth

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func at(seconds int) time.Time { return epoch.Add(time.Duration(seconds) * time.Second) }
func exact(seconds int) Span   { return Span{ObservedEndpoints, time.Duration(seconds) * time.Second} }
func lower(seconds int) Span   { return Span{ObservedLowerBound, time.Duration(seconds) * time.Second} }

func fixture() AttemptFacts {
	key := AttemptKey{Source: "github_main", RepositoryID: math.MaxInt64 - 8, WorkflowID: math.MaxInt64 - 5, RunID: math.MaxInt64 - 1, Attempt: 1}
	job := JobKey{Attempt: key, JobID: math.MaxInt64}
	return AttemptFacts{
		Key: key, AsOf: at(45), Clock: Clock{Phase: Completed, StartedAt: at(10), CompletedAt: at(40)},
		Jobs: []JobFacts{{
			Key: job, Clock: Clock{Phase: Completed, StartedAt: at(12), CompletedAt: at(32)}, QueuedAt: at(1), FirstQueuedObservedAt: at(5),
			Steps: []StepFacts{
				{StepKey{job, 1}, Clock{Phase: Completed, StartedAt: at(12), CompletedAt: at(17)}},
				{StepKey{job, 3}, Clock{Phase: Skipped, StartedAt: at(18), CompletedAt: at(18)}},
				{StepKey{job, 15}, Clock{Phase: Completed, StartedAt: at(30), CompletedAt: at(30)}},
				{StepKey{job, 16}, Clock{Phase: Completed, StartedAt: time.Time{}, CompletedAt: at(31)}},
			},
		}},
	}
}

func TestMeasureUsesExactNestedIDsAndSeparateTiming(t *testing.T) {
	f := fixture()
	got, err := Measure(f)
	if err != nil || got.Key != f.Key || got.Wall != exact(30) || got.Active != (Span{}) || len(got.Jobs) != 1 {
		t.Fatalf("attempt timing: %+v / %v", got, err)
	}
	job := got.Jobs[0]
	if job.Key.JobID != math.MaxInt64 || job.Queue != exact(11) || job.Execution != exact(20) ||
		job.ActiveQueue != (Span{}) || job.Active != (Span{}) || len(job.Steps) != 4 {
		t.Fatalf("job timing: %+v", job)
	}
	want := []Span{exact(5), {}, exact(0), {}}
	for i, step := range job.Steps {
		if step.Key != f.Jobs[0].Steps[i].Key || step.Duration != want[i] || step.Active != (Span{}) {
			t.Errorf("step %d: %+v / want %+v", i, step, want[i])
		}
	}

	// A selective rerun has only its own reported rows. No prior runtime or
	// steps are inferred even if the numeric run/workflow keys repeat.
	rerun := AttemptFacts{Key: f.Key, AsOf: at(80), Clock: Clock{Phase: Running}}
	rerun.Key.Attempt++
	rerun.Jobs = []JobFacts{{Key: JobKey{rerun.Key, math.MaxInt64 - 3}, Clock: Clock{Phase: Queued}}}
	next, err := Measure(rerun)
	if err != nil || next.Key.Attempt != 2 || next.Wall != (Span{}) || next.Active != (Span{}) ||
		len(next.Jobs) != 1 || len(next.Jobs[0].Steps) != 0 || next.Jobs[0].Queue != (Span{}) ||
		next.Jobs[0].ActiveQueue != (Span{}) || next.Jobs[0].Key.JobID == job.Key.JobID {
		t.Fatalf("selective rerun: %+v / %v", next, err)
	}
}

func TestMeasureLeavesMissingQueueUnknownAndSeparatesActive(t *testing.T) {
	f := fixture()
	f.Clock = Clock{Phase: Running, StartedAt: at(2), ObservedAt: at(45)}
	first := &f.Jobs[0]
	first.Clock = Clock{Phase: Running, StartedAt: at(12), ObservedAt: at(43)}
	first.QueuedAt = time.Time{}
	first.Steps = first.Steps[:1]
	first.Steps[0].Clock = Clock{Phase: Running, StartedAt: at(14), ObservedAt: at(42)}
	f.Jobs = append(f.Jobs, JobFacts{
		Key: JobKey{f.Key, math.MaxInt64 - 1}, Clock: Clock{Phase: Queued, ObservedAt: at(40)}, FirstQueuedObservedAt: at(9),
	}, JobFacts{
		Key: JobKey{f.Key, math.MaxInt64 - 2}, Clock: Clock{Phase: Completed, CompletedAt: at(20)},
	})
	got, err := Measure(f)
	if err != nil || got.Wall != (Span{}) || got.Active != exact(43) {
		t.Fatalf("attempt active: %+v / %v", got, err)
	}
	a, b, c := got.Jobs[0], got.Jobs[1], got.Jobs[2]
	if a.Queue != lower(7) || a.Execution != (Span{}) || a.Active != exact(31) ||
		a.Steps[0].Active != exact(28) || a.Steps[0].Duration != (Span{}) ||
		b.Queue != (Span{}) || b.ActiveQueue != lower(31) || b.Active != (Span{}) ||
		c.Queue != (Span{}) || c.Execution != (Span{}) || c.Active != (Span{}) {
		t.Fatalf("mixed observations: %+v", got.Jobs)
	}

	// Attempt start was present the whole time, but is not a job queue event.
	f.Jobs[0].FirstQueuedObservedAt = time.Time{}
	f.Jobs[0].Clock.ObservedAt = time.Time{}
	f.Jobs[1].Clock.ObservedAt = time.Time{}
	unknown, err := Measure(f)
	if err != nil || unknown.Jobs[0].Queue != (Span{}) || unknown.Jobs[0].Active != (Span{}) || unknown.Jobs[1].ActiveQueue != (Span{}) {
		t.Fatalf("invented queue time: %+v / %v", unknown, err)
	}
}

func TestMeasureRejectsCorrelatedRowAndClockConflicts(t *testing.T) {
	for name, mutate := range map[string]func(*AttemptFacts){
		"unscoped source":           func(f *AttemptFacts) { f.Key.Source = "https://untrusted.invalid" },
		"missing capture":           func(f *AttemptFacts) { f.AsOf = time.Time{} },
		"wrong repository":          func(f *AttemptFacts) { f.Jobs[0].Key.Attempt.RepositoryID-- },
		"wrong workflow":            func(f *AttemptFacts) { f.Jobs[0].Key.Attempt.WorkflowID-- },
		"wrong run":                 func(f *AttemptFacts) { f.Jobs[0].Key.Attempt.RunID-- },
		"wrong attempt":             func(f *AttemptFacts) { f.Jobs[0].Key.Attempt.Attempt++ },
		"wrong source":              func(f *AttemptFacts) { f.Jobs[0].Key.Attempt.Source = "other" },
		"duplicate job":             func(f *AttemptFacts) { f.Jobs = append(f.Jobs, f.Jobs[0]) },
		"wrong step parent":         func(f *AttemptFacts) { f.Jobs[0].Steps[0].Key.Job.JobID-- },
		"duplicate step":            func(f *AttemptFacts) { f.Jobs[0].Steps = append(f.Jobs[0].Steps, f.Jobs[0].Steps[0]) },
		"invalid step":              func(f *AttemptFacts) { f.Jobs[0].Steps[0].Key.Number = 0 },
		"unknown phase":             func(f *AttemptFacts) { f.Jobs[0].Clock.Phase = Phase(99) },
		"foredated boundary":        func(f *AttemptFacts) { f.AsOf = at(37) },
		"inverted clock":            func(f *AttemptFacts) { f.Jobs[0].Clock.CompletedAt = at(8) },
		"before exact queue":        func(f *AttemptFacts) { f.Jobs[0].FirstQueuedObservedAt = at(0) },
		"queued sighting after run": func(f *AttemptFacts) { f.Jobs[0].FirstQueuedObservedAt = at(13) },
		"step outside job":          func(f *AttemptFacts) { f.Jobs[0].Steps[0].Clock.CompletedAt = at(33) },
		"duration overflow": func(f *AttemptFacts) {
			f.Clock.StartedAt = time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := fixture()
			mutate(&f)
			got, err := Measure(f)
			if !errors.Is(err, ErrTimingFacts) || got.Key != (AttemptKey{}) || got.Jobs != nil {
				t.Fatalf("did not refuse entire observation: %+v / %v", got, err)
			}
		})
	}
}

func TestMeasureStepsInheritKnownAttemptBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  Clock
		step Clock
	}{
		{"active after completion", Clock{Phase: Completed, StartedAt: at(10), CompletedAt: at(40)}, Clock{Phase: Running, StartedAt: at(41), ObservedAt: at(45)}},
		{"active within completed attempt", Clock{Phase: Completed, StartedAt: at(10), CompletedAt: at(40)}, Clock{Phase: Running, StartedAt: at(30), ObservedAt: at(35)}},
		{"ended after completion", Clock{Phase: Completed, StartedAt: at(10), CompletedAt: at(40)}, Clock{Phase: Completed, StartedAt: at(35), CompletedAt: at(41)}},
		{"ended before start", Clock{Phase: Running, StartedAt: at(10)}, Clock{Phase: Completed, StartedAt: at(2), CompletedAt: at(8)}},
		{"completed within skipped attempt", Clock{Phase: Skipped}, Clock{Phase: Completed, StartedAt: at(15), CompletedAt: at(17)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture()
			f.Clock = tc.run
			f.Jobs[0].Clock = Clock{}
			f.Jobs[0].Steps = f.Jobs[0].Steps[:1]
			f.Jobs[0].Steps[0].Clock = tc.step
			if got, err := Measure(f); !errors.Is(err, ErrTimingFacts) || got.Key != (AttemptKey{}) {
				t.Fatalf("unknown intermediate job hid attempt bounds: %+v / %v", got, err)
			}
		})
	}
	f := fixture()
	f.Jobs[0].Clock = Clock{}
	if got, err := Measure(f); err != nil || got.Jobs[0].Steps[0].Duration != exact(5) {
		t.Fatalf("bounded child evidence should remain valid: %+v / %v", got, err)
	}
}

func manyFacts(jobs, steps int) AttemptFacts {
	f := fixture()
	f.Jobs = make([]JobFacts, 0, jobs)
	for i := range jobs {
		job := JobFacts{Key: JobKey{f.Key, int64(i + 1)}}
		job.Steps = make([]StepFacts, 0, steps)
		for n := range steps {
			job.Steps = append(job.Steps, StepFacts{Key: StepKey{job.Key, int64(n + 1)}})
		}
		f.Jobs = append(f.Jobs, job)
	}
	return f
}

func TestMeasureEnforcesExactBounds(t *testing.T) {
	if _, err := Measure(manyFacts(MaxJobs, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := Measure(manyFacts(MaxTotalSteps/MaxStepsPerJob, MaxStepsPerJob)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []AttemptFacts{
		manyFacts(MaxJobs+1, 0), manyFacts(1, MaxStepsPerJob+1),
		manyFacts(MaxTotalSteps/MaxStepsPerJob+1, MaxStepsPerJob),
	} {
		if _, err := Measure(f); !errors.Is(err, ErrTimingFacts) {
			t.Fatal("oversized observations accepted")
		}
	}
	f := fixture()
	f.Key.Source = strings.Repeat("a", 65)
	if _, err := Measure(f); !errors.Is(err, ErrTimingFacts) {
		t.Fatal("oversized source accepted")
	}
}

func TestMeasureConclusionsRemainExplicitAndPhaseBound(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	attempt := AttemptKey{Source: "fixture", RepositoryID: 1, WorkflowID: 2, RunID: 3, Attempt: 4}
	job := JobKey{Attempt: attempt, JobID: 5}
	facts := AttemptFacts{Key: attempt, AsOf: now, Clock: Clock{Phase: Completed, Conclusion: ConclusionSuccess}, Jobs: []JobFacts{{
		Key: job, Clock: Clock{Phase: Completed, Conclusion: ConclusionFailure},
		Steps: []StepFacts{
			{Key: StepKey{job, 1}, Clock: Clock{Phase: Skipped, Conclusion: ConclusionOtherTerminal}},
			{Key: StepKey{job, 2}, Clock: Clock{Phase: Completed, StartedAt: now, CompletedAt: now}},
		},
	}}}
	got, err := Measure(facts)
	if err != nil || got.Conclusion != ConclusionSuccess || got.Wall != (Span{}) ||
		got.Jobs[0].Conclusion != ConclusionFailure || got.Jobs[0].Execution != (Span{}) ||
		got.Jobs[0].Steps[0].Conclusion != ConclusionOtherTerminal ||
		got.Jobs[0].Steps[1].Conclusion != ConclusionUnknown ||
		got.Jobs[0].Steps[1].Duration != (Span{Basis: ObservedEndpoints}) {
		t.Fatalf("result changed timing or vanished: %+v / %v", got, err)
	}
	for _, invalid := range []Clock{
		{Phase: Completed, Conclusion: "dynamic-source-payload"},
		{Phase: Running, Conclusion: ConclusionSuccess},
		{Phase: Queued, Conclusion: ConclusionFailure},
		{Phase: Skipped, Conclusion: ConclusionCancelled},
		{Phase: Unknown, Conclusion: ConclusionOtherTerminal},
	} {
		facts.Jobs[0].Steps[1].Clock = invalid
		if got, err := Measure(facts); err != ErrTimingFacts || len(got.Jobs) != 0 {
			t.Fatalf("invalid result returned timing: %+v / %v", got, err)
		}
	}
}
