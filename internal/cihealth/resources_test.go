package cihealth

import (
	"math"
	"testing"
	"time"
)

func resourceScopeFixture(window, maxGap time.Duration) JobResourceScope {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return JobResourceScope{
		Job:               JobKey{Attempt: AttemptKey{Source: "fixture", RepositoryID: 1, WorkflowID: 2, RunID: 3, Attempt: 4}, JobID: 5},
		RunnerIncarnation: "fixture-incarnation-1", StartedAt: start, EndAt: start.Add(window), AsOf: start.Add(window + time.Second), MaxGap: maxGap,
	}
}

func resourceSampleFixture(s JobResourceScope, offset time.Duration) JobResourceSample {
	return JobResourceSample{Job: s.Job, RunnerIncarnation: s.RunnerIncarnation, At: s.StartedAt.Add(offset)}
}

func knownResource(v uint64) ResourceValue { return ResourceValue{Known: true, Value: v} }

func TestJobResourcesRatesWeightedGaugesAndOptionalDisk(t *testing.T) {
	scope := resourceScopeFixture(6*time.Second, 4*time.Second)
	samples := []JobResourceSample{resourceSampleFixture(scope, 0), resourceSampleFixture(scope, 2*time.Second), resourceSampleFixture(scope, 6*time.Second)}
	for i := range samples {
		samples[i].CPUTimeNanos = knownResource([]uint64{10e9, 11e9, 16e9}[i])
		samples[i].RAMBytes = knownResource([]uint64{10, 30, 50}[i])
		samples[i].DiskReadBytes = knownResource([]uint64{5, 15, 55}[i])
		samples[i].DiskUsedBytes = knownResource([]uint64{100, 9, 20}[i])
	}
	samples[1].DiskWriteBytes, samples[2].DiskWriteBytes = knownResource(6), knownResource(6)
	out, err := SummarizeJobResources(scope, samples)
	full := ResourceCoverage{Intervals: 2, Duration: 6 * time.Second, Complete: true}
	if err != nil || out.Window != 6*time.Second || out.Samples != 3 || out.Sampling != (ResourceSamplingCoverage{}) || out.CPU == nil || *out.CPU != (JobCPURates{1000, 1250, full}) ||
		out.RAMMean == nil || *out.RAMMean != (JobRAMAverage{33, full}) || out.RAM == nil || *out.RAM != (ResourceGaugeSummary{3, 50, 50, scope.EndAt}) ||
		out.DiskRead == nil || *out.DiskRead != (JobResourceCounterDelta{50, full}) ||
		out.DiskWrite == nil || *out.DiskWrite != (JobResourceCounterDelta{0, ResourceCoverage{Intervals: 1, Duration: 4 * time.Second}}) ||
		out.DiskUsed == nil || *out.DiskUsed != (ResourceGaugeSummary{3, 100, 20, scope.EndAt}) {
		t.Fatalf("unexpected resource summary: %+v, %v", out, err)
	}
}

func TestJobResourcesDoesNotBridgeLostCadenceOrMissingMetrics(t *testing.T) {
	scope := resourceScopeFixture(10*time.Second, 2*time.Second)
	var samples []JobResourceSample
	for i, second := range []time.Duration{1, 2, 5, 6, 7, 9} {
		s := resourceSampleFixture(scope, second*time.Second)
		s.CPUTimeNanos = knownResource([]uint64{0, 1e9, 20e9, 21e9, 22e9, 26e9}[i])
		s.RAMBytes = knownResource(uint64(second))
		samples = append(samples, s)
	}
	// A fresh sample with an unavailable CPU metric is not a usable endpoint.
	samples[3].CPUTimeNanos = ResourceValue{}
	out, err := SummarizeJobResources(scope, samples)
	if err != nil || out.CPU == nil || *out.CPU != (JobCPURates{1666, 2000, ResourceCoverage{Intervals: 2, Duration: 3 * time.Second}}) ||
		out.RAMMean == nil || out.RAMMean.Coverage != (ResourceCoverage{Intervals: 4, Duration: 5 * time.Second}) ||
		out.DiskRead != nil || out.DiskWrite != nil || out.DiskUsed != nil {
		t.Fatalf("missing metrics or cadence were bridged: %+v, %v", out, err)
	}
	want := ResourceSamplingCoverage{
		BeforeFirstSample: time.Second, AfterLastSample: time.Second,
		CadenceGaps: 1, CadenceGapDuration: 3 * time.Second,
	}
	if out.Sampling != want {
		t.Fatalf("unexpected sampling coverage: got %+v, want %+v", out.Sampling, want)
	}
	// Missed CPU values cost two more seconds than the shared sampling holes.
	samplingHoles := out.Sampling.BeforeFirstSample + out.Sampling.AfterLastSample + out.Sampling.CadenceGapDuration
	if missingCPU := out.Window - out.CPU.Coverage.Duration - samplingHoles; missingCPU != 2*time.Second {
		t.Fatalf("unexpected missing CPU coverage beyond sampling holes: %s", missingCPU)
	}
	none, err := SummarizeJobResources(scope, nil)
	if err != nil || none.Sampling != (ResourceSamplingCoverage{NoSamples: scope.EndAt.Sub(scope.StartedAt)}) || none.RAM != nil || none.CPU != nil {
		t.Fatalf("empty window not distinct from a complete sample series: %+v, %v", none, err)
	}
	// A single observed gauge keeps its sample strength without inventing time.
	single := samples[:1]
	single[0].RAMBytes = knownResource(0)
	one, err := SummarizeJobResources(scope, single)
	if err != nil || one.CPU != nil || one.RAMMean != nil || one.RAM == nil || one.RAM.Samples != 1 || one.RAM.SampleMax != 0 ||
		one.Sampling != (ResourceSamplingCoverage{BeforeFirstSample: time.Second, AfterLastSample: 9 * time.Second}) {
		t.Fatalf("observed zero or uncovered metrics lost: %+v, %v", one, err)
	}
}

func TestJobResourcesRejectsInvalidScopeResetOrderAndOversize(t *testing.T) {
	for name, change := range map[string]func(*JobResourceScope, []JobResourceSample){
		"foreign job":         func(_ *JobResourceScope, s []JobResourceSample) { s[1].Job.JobID++ },
		"foreign attempt":     func(_ *JobResourceScope, s []JobResourceSample) { s[1].Job.Attempt.Attempt++ },
		"foreign incarnation": func(_ *JobResourceScope, s []JobResourceSample) { s[1].RunnerIncarnation = "fixture-incarnation-2" },
		"stale/outside job":   func(_ *JobResourceScope, s []JobResourceSample) { s[0].At = s[0].At.Add(-time.Nanosecond) },
		"after job cutoff":    func(w *JobResourceScope, s []JobResourceSample) { s[1].At = w.EndAt.Add(time.Nanosecond) },
		"duplicate clock":     func(_ *JobResourceScope, s []JobResourceSample) { s[1].At = s[0].At },
		"out of order":        func(_ *JobResourceScope, s []JobResourceSample) { s[0], s[1] = s[1], s[0] },
		"cutoff in future":    func(w *JobResourceScope, _ []JobResourceSample) { w.AsOf = w.EndAt.Add(-time.Nanosecond) },
		"unbounded gap":       func(w *JobResourceScope, _ []JobResourceSample) { w.MaxGap = MaxJobResourceGap + time.Nanosecond },
		"unbounded window": func(w *JobResourceScope, _ []JobResourceSample) {
			w.EndAt = w.StartedAt.Add(MaxJobResourceWindow + 1)
			w.AsOf = w.EndAt
		},
		"unknown with value": func(_ *JobResourceScope, s []JobResourceSample) { s[1].DiskUsedBytes.Value = 1 },
		"CPU reset":          func(_ *JobResourceScope, s []JobResourceSample) { s[1].CPUTimeNanos = knownResource(0) },
		"IO reset over gap": func(w *JobResourceScope, s []JobResourceSample) {
			w.MaxGap = time.Millisecond
			s[1].DiskReadBytes = knownResource(0)
		},
		"CPU rate overflow": func(_ *JobResourceScope, s []JobResourceSample) {
			s[1].At = s[0].At.Add(time.Nanosecond)
			s[1].CPUTimeNanos = knownResource(math.MaxUint64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := resourceScopeFixture(time.Second, time.Second)
			s := []JobResourceSample{resourceSampleFixture(w, 0), resourceSampleFixture(w, time.Second)}
			for i := range s {
				s[i].CPUTimeNanos, s[i].DiskReadBytes = knownResource(1), knownResource(1)
			}
			change(&w, s)
			if got, err := SummarizeJobResources(w, s); err != ErrJobResourceFacts || got != (JobResourceSummary{}) {
				t.Fatalf("invalid data returned partial result: %+v, %v", got, err)
			}
		})
	}
	if got, err := SummarizeJobResources(resourceScopeFixture(time.Minute, time.Second), make([]JobResourceSample, MaxJobResourceSamples+1)); err != ErrJobResourceFacts || got != (JobResourceSummary{}) {
		t.Fatalf("oversize returned data: %+v, %v", got, err)
	}
}

func TestJobResourcesWeightAtMaximumGaugeDoesNotOverflow(t *testing.T) {
	w := resourceScopeFixture(2*time.Minute, MaxJobResourceGap)
	s := []JobResourceSample{resourceSampleFixture(w, 0), resourceSampleFixture(w, 2*time.Minute)}
	s[0].RAMBytes, s[1].RAMBytes = knownResource(math.MaxUint64), knownResource(math.MaxUint64-1)
	got, err := SummarizeJobResources(w, s)
	if err != nil || got.RAMMean == nil || got.RAMMean.Bytes != math.MaxUint64-1 || !got.RAMMean.Coverage.Complete {
		t.Fatalf("exact weighted mean overflowed: %+v, %v", got, err)
	}
}
