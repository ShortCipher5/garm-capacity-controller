package cihealth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func stockResourceBindingFixture() (JobResourceScope, StockJobResourceBinding, StockJobResourceBinding, StockJobExecutionObservation, []JobResourceSample) {
	s := resourceScopeFixture(time.Minute, MaxJobResourceGap)
	s.RunnerIncarnation = strings.Repeat("1", 64)
	a := StockJobResourceBinding{
		Job: s.Job, RunnerIncarnation: s.RunnerIncarnation, RunnerAgentID: 9007199254740993,
		StockSourceID: strings.Repeat("2", 64), StockInstanceUUID: "11111111-1111-4111-8111-111111111111",
		StockPoolUUID: "22222222-2222-4222-8222-222222222222",
		Runtime: StockRunnerIncarnation{
			PeerFingerprint: strings.Repeat("3", 64), Member: "member-a", Project: "reviewed-project",
			InstanceName: "same-runner", UUID: "33333333-3333-4333-8333-333333333333",
			Generation: "44444444-4444-4444-8444-444444444444", BootID: "55555555-5555-4555-8555-555555555555",
			ProcessStartTicks: 123, StartedAt: s.StartedAt.Add(-time.Minute),
		},
		ObservedAt: s.StartedAt.Add(-time.Second), State: StockRunnerObservedRunning,
	}
	b := a
	b.ObservedAt = s.AsOf
	x, y := resourceSampleFixture(s, 0), resourceSampleFixture(s, time.Minute-time.Second)
	x.CPUTimeNanos, y.CPUTimeNanos = knownResource(100), knownResource(200)
	x.DiskReadBytes, y.DiskReadBytes = knownResource(250), knownResource(400)
	job := StockJobExecutionObservation{Job: s.Job, RunnerAgentID: a.RunnerAgentID, StartedAt: s.StartedAt, ObservedAt: s.AsOf, State: StockJobObservedExecuting}
	return s, a, b, job, []JobResourceSample{x, y}
}

func TestBoundStockJobResourcesAcceptsOnlyAlreadyBoundWindow(t *testing.T) {
	s, a, b, job, samples := stockResourceBindingFixture()
	// Time zone encodings of the same independently captured instant do not
	// create a different process lifetime.
	b.Runtime.StartedAt = b.Runtime.StartedAt.In(time.FixedZone("fixture", 3600))
	got, err := SummarizeBoundStockJobResources(s, &a, &b, &job, samples)
	if err != nil || got.Scope != s || got.Samples != 2 || got.DiskRead == nil || got.DiskRead.Bytes != 150 ||
		got.DiskRead.Coverage.Complete {
		t.Fatalf("exact compatible binding or independent sample coverage changed: %+v %v", got, err)
	}
	b.State, b.TeardownAt = StockRunnerObservedStopped, s.EndAt
	job.State, job.EndedAt = StockJobObservedEnded, s.EndAt
	if got, err := SummarizeBoundStockJobResources(s, &a, &b, &job, samples); err != nil || got.Samples != 2 {
		t.Fatalf("positive teardown after all samples should not erase past evidence: %+v %v", got, err)
	}
}

func TestBoundStockJobResourcesRefusesJobEvidenceContradictingTeardown(t *testing.T) {
	s, a, b, job, samples := stockResourceBindingFixture()
	b.State, b.TeardownAt = StockRunnerObservedStopped, s.EndAt
	job.ObservedAt = b.TeardownAt
	assertBoundStockRefused(t, s, &a, &b, &job, samples)
	job.ObservedAt = s.AsOf
	assertBoundStockRefused(t, s, &a, &b, &job, samples)
	job.State, job.EndedAt = StockJobObservedEnded, b.TeardownAt.Add(time.Nanosecond)
	assertBoundStockRefused(t, s, &a, &b, &job, samples)

	// Execution was sampled strictly before the later teardown. Observation
	// of the runner ending is independent; the reported job cutoff is earlier.
	b.TeardownAt = s.AsOf
	job.State, job.EndedAt, job.ObservedAt = StockJobObservedExecuting, time.Time{}, s.EndAt
	if got, err := SummarizeBoundStockJobResources(s, &a, &b, &job, samples); err != nil || got.Samples != len(samples) {
		t.Fatalf("positive execution strictly before later teardown rejected: %+v %v", got, err)
	}
}

func TestBoundStockJobResourcesRejectsSameNameReplacementEvenWhenCountersRise(t *testing.T) {
	for name, alter := range map[string]func(*StockJobResourceBinding){
		"registration":        func(b *StockJobResourceBinding) { b.RunnerAgentID++ },
		"garm source":         func(b *StockJobResourceBinding) { b.StockSourceID = strings.Repeat("4", 64) },
		"garm instance":       func(b *StockJobResourceBinding) { b.StockInstanceUUID = b.Runtime.UUID },
		"garm pool":           func(b *StockJobResourceBinding) { b.StockPoolUUID = b.Runtime.UUID },
		"incus peer":          func(b *StockJobResourceBinding) { b.Runtime.PeerFingerprint = strings.Repeat("4", 64) },
		"uuid":                func(b *StockJobResourceBinding) { b.Runtime.UUID = b.Runtime.Generation },
		"generation":          func(b *StockJobResourceBinding) { b.Runtime.Generation = b.Runtime.UUID },
		"boot":                func(b *StockJobResourceBinding) { b.Runtime.BootID = b.Runtime.Generation },
		"process start ticks": func(b *StockJobResourceBinding) { b.Runtime.ProcessStartTicks++ },
		"process start time":  func(b *StockJobResourceBinding) { b.Runtime.StartedAt = b.Runtime.StartedAt.Add(time.Nanosecond) },
		"member":              func(b *StockJobResourceBinding) { b.Runtime.Member = "member-b" },
		"project":             func(b *StockJobResourceBinding) { b.Runtime.Project = "another-project" },
		"job attempt":         func(b *StockJobResourceBinding) { b.Job.Attempt.Attempt++ },
		"unknown member":      func(b *StockJobResourceBinding) { b.Runtime.Member = "" },
	} {
		t.Run(name, func(t *testing.T) {
			s, a, b, job, samples := stockResourceBindingFixture()
			alter(&b)
			assertBoundStockRefused(t, s, &a, &b, &job, samples)
		})
	}
}

func TestBoundStockJobResourcesRejectsMissingUnbracketedAndTeardownSamples(t *testing.T) {
	s, a, b, job, samples := stockResourceBindingFixture()
	assertBoundStockRefused(t, s, nil, &b, &job, samples)
	assertBoundStockRefused(t, s, &a, nil, &job, samples)
	assertBoundStockRefused(t, s, &StockJobResourceBinding{}, &b, &job, samples)
	for name, alter := range map[string]func(*StockJobResourceBinding, *StockJobResourceBinding, []JobResourceSample){
		"initial after job start": func(a, b *StockJobResourceBinding, _ []JobResourceSample) {
			a.ObservedAt = s.StartedAt.Add(time.Second)
		},
		"stale initial": func(a, b *StockJobResourceBinding, _ []JobResourceSample) {
			a.ObservedAt = s.StartedAt.Add(-MaxJobResourceGap - time.Nanosecond)
			a.Runtime.StartedAt = a.ObservedAt
			b.Runtime.StartedAt = a.ObservedAt
		},
		"unproven initial": func(a, _ *StockJobResourceBinding, _ []JobResourceSample) { a.State = StockRunnerObservationUnknown },
		"unproven end":     func(_, b *StockJobResourceBinding, _ []JobResourceSample) { b.State = StockRunnerObservationUnknown },
		"closing before window ends": func(_, b *StockJobResourceBinding, _ []JobResourceSample) {
			b.ObservedAt = s.EndAt.Add(-time.Nanosecond)
		},
		"closing after report": func(_, b *StockJobResourceBinding, _ []JobResourceSample) { b.ObservedAt = s.AsOf.Add(time.Nanosecond) },
		"teardown inside window": func(_, b *StockJobResourceBinding, _ []JobResourceSample) {
			b.State, b.TeardownAt = StockRunnerObservedStopped, samples[1].At.Add(-time.Nanosecond)
		},
		"sample exactly at teardown": func(_, b *StockJobResourceBinding, s []JobResourceSample) {
			s[1].At = s[0].At.Add(time.Minute)
			b.State, b.TeardownAt = StockRunnerObservedStopped, s[1].At
		},
		"unbound sample": func(_, _ *StockJobResourceBinding, s []JobResourceSample) { s[1].RunnerIncarnation = "same-runner" },
	} {
		t.Run(name, func(t *testing.T) {
			s, a, b, job, samples := stockResourceBindingFixture()
			alter(&a, &b, samples)
			assertBoundStockRefused(t, s, &a, &b, &job, samples)
		})
	}
}

func assertBoundStockRefused(t *testing.T, s JobResourceScope, a, b *StockJobResourceBinding, job *StockJobExecutionObservation, v []JobResourceSample) {
	t.Helper()
	if got, err := SummarizeBoundStockJobResources(s, a, b, job, v); !errors.Is(err, ErrJobResourceFacts) || got != (JobResourceSummary{}) {
		t.Fatalf("unproven exact job window produced a partial metric: %+v %v", got, err)
	}
}

func TestBoundStockJobResourcesRequiresSeparateExecutionThroughCutoff(t *testing.T) {
	s, a, b, job, samples := stockResourceBindingFixture()
	if b.State != StockRunnerObservedRunning {
		t.Fatal("fixture must retain the exact runner after the workload ends")
	}
	assertBoundStockRefused(t, s, &a, &b, nil, samples)
	for name, alter := range map[string]func(*StockJobExecutionObservation){
		"runner present after job ended": func(j *StockJobExecutionObservation) {
			j.State, j.EndedAt = StockJobObservedEnded, s.EndAt.Add(-20*time.Second)
		},
		"unknown job execution":              func(j *StockJobExecutionObservation) { j.State = StockJobExecutionUnknown },
		"terminal state without end":         func(j *StockJobExecutionObservation) { j.State = StockJobObservedEnded },
		"job has no exact start":             func(j *StockJobExecutionObservation) { j.StartedAt = time.Time{} },
		"observed active before cutoff only": func(j *StockJobExecutionObservation) { j.ObservedAt = s.EndAt.Add(-time.Nanosecond) },
		"terminal endpoint after the report": func(j *StockJobExecutionObservation) {
			j.State, j.EndedAt = StockJobObservedEnded, s.AsOf.Add(time.Nanosecond)
		},
		"contradictory active endpoint":    func(j *StockJobExecutionObservation) { j.EndedAt = s.EndAt },
		"another job in exact same runner": func(j *StockJobExecutionObservation) { j.Job.JobID++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := job
			alter(&changed)
			assertBoundStockRefused(t, s, &a, &b, &changed, samples)
		})
	}
	job.State, job.EndedAt = StockJobObservedEnded, s.EndAt
	if got, err := SummarizeBoundStockJobResources(s, &a, &b, &job, samples); err != nil || got.Samples != len(samples) {
		t.Fatalf("independently proven job end at report cutoff rejected: %+v %v", got, err)
	}
}

func TestBoundStockJobResourcesRefusesDifferentOrMissingExecutionRunnerRegistration(t *testing.T) {
	for _, state := range []struct {
		name  string
		state StockJobExecutionState
	}{
		{"executing", StockJobObservedExecuting},
		{"ended", StockJobObservedEnded},
	} {
		t.Run(state.name, func(t *testing.T) {
			s, a, b, job, samples := stockResourceBindingFixture()
			job.State = state.state
			if job.State == StockJobObservedEnded {
				job.EndedAt = s.EndAt
			}
			for _, id := range []int64{0, -1, a.RunnerAgentID + 1} {
				changed := job
				changed.RunnerAgentID = id
				assertBoundStockRefused(t, s, &a, &b, &changed, samples)
			}
			if got, err := SummarizeBoundStockJobResources(s, &a, &b, &job, samples); err != nil || got.Samples != len(samples) {
				t.Fatalf("matching registration did not preserve the exact window: %+v %v", got, err)
			}
		})
	}
}
