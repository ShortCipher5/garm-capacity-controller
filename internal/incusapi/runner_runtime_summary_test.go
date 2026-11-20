package incusapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunnerRuntimeSummaryConsumesRealNamedSamplesWithoutBackfill(t *testing.T) {
	f := runtimeHealthFixture(t, runnerHealthVersion, func(path string, n int, d map[string]any) {
		if !strings.HasSuffix(path, "/state") {
			return
		}
		if n <= 2 {
			d["memory"] = map[string]int64{"usage": 4200, "usage_peak": 8400}
		} else {
			d["cpu"] = map[string]int64{"usage": -1}
			d["memory"] = map[string]int64{"usage_peak": 8400}
		}
	})
	f.pin(t)
	start := time.Now()
	first, err := f.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.client.ReadRunnerRuntimeHealth(t.Context(), healthExpected)
	if err != nil {
		t.Fatal(err)
	}
	got, err := SummarizeRunnerRuntimeHealth([]RunnerRuntimeHealth{first, second}, start, time.Now().Add(time.Second))
	if err != nil || got.Count != 2 || got.MemoryCurrentSamples != 1 ||
		!got.FirstSampleAt.Equal(first.SampledAt) || !got.LatestSampleAt.Equal(second.SampledAt) ||
		got.LatestCPUCumulativeNanoseconds != nil || got.LatestMemoryCurrentBytes != nil ||
		got.PeakSampledMemoryCurrentBytes == nil || *got.PeakSampledMemoryCurrentBytes != 4200 ||
		got.LatestMemoryCgroupPeakBytes == nil || *got.LatestMemoryCgroupPeakBytes != 8400 {
		t.Fatalf("invented or conflated runtime-window evidence: %+v / %v", got, err)
	}
	*got.LatestMemoryCgroupPeakBytes = 4
	if *second.MemoryCgroupPeakBytes != 8400 {
		t.Fatal("summary aliased caller's sample")
	}
	blob, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RunnerRuntimeHealth
	if err = json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err = SummarizeRunnerRuntimeHealth([]RunnerRuntimeHealth{decoded}, start, time.Now().Add(time.Second)); !errors.Is(err, ErrRunnerRuntimeWindow) {
		t.Fatal("untrusted JSON was accepted as named observation", err)
	}
}

func TestRunnerRuntimeSummaryRefusesDriftResetEditedEvidenceAndBounds(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pin := PhysicalServerIdentity{origin: hash, scope: hash, key: hash, serverName: "fixture", serverVersion: runnerHealthVersion, verified: true}
	at := time.Date(2026, 2, 1, 1, 0, 0, 0, time.UTC)
	counter := func(v int64) *int64 { return &v }
	firstState := runnerHealthState{pid: 41, started: at.Add(-time.Hour), cpu: counter(100), memory: counter(4000), memoryPeak: counter(6000)}
	row := func(s runnerHealthState, p PhysicalServerIdentity, e RunnerRuntimeExpected, at time.Time) RunnerRuntimeHealth {
		return RunnerRuntimeHealth{
			SampledAt: at, CPUCumulativeNanoseconds: s.cpu, MemoryCurrentBytes: s.memory, MemoryCgroupPeakBytes: s.memoryPeak,
			sample: newRunnerRuntimeSample(p, e, s, at),
		}
	}
	first := row(firstState, pin, healthExpected, at)
	for label, change := range map[string]func(*runnerHealthState, *PhysicalServerIdentity, *RunnerRuntimeExpected){
		"replacement UUID": func(_ *runnerHealthState, _ *PhysicalServerIdentity, e *RunnerRuntimeExpected) { e.UUID = healthGen },
		"generation": func(_ *runnerHealthState, _ *PhysicalServerIdentity, e *RunnerRuntimeExpected) {
			e.Generation = healthUUID
		},
		"creation drift": func(_ *runnerHealthState, _ *PhysicalServerIdentity, e *RunnerRuntimeExpected) {
			e.CreatedAt = e.CreatedAt.Add(time.Second)
		},
		"process restart": func(s *runnerHealthState, _ *PhysicalServerIdentity, _ *RunnerRuntimeExpected) {
			s.started = s.started.Add(time.Minute)
		},
		"PID drift": func(s *runnerHealthState, _ *PhysicalServerIdentity, _ *RunnerRuntimeExpected) { s.pid++ },
		"same name other project": func(_ *runnerHealthState, p *PhysicalServerIdentity, _ *RunnerRuntimeExpected) {
			p.scope = strings.Repeat("b", 64)
		},
		"same project other peer": func(_ *runnerHealthState, p *PhysicalServerIdentity, _ *RunnerRuntimeExpected) {
			p.key = strings.Repeat("b", 64)
		},
		"CPU resets": func(s *runnerHealthState, _ *PhysicalServerIdentity, _ *RunnerRuntimeExpected) {
			s.cpu = counter(50)
		},
		"peak resets": func(s *runnerHealthState, _ *PhysicalServerIdentity, _ *RunnerRuntimeExpected) {
			s.memoryPeak = counter(5000)
		},
	} {
		t.Run(label, func(t *testing.T) {
			s, p, e := firstState, pin, healthExpected
			change(&s, &p, &e)
			second := row(s, p, e, at.Add(time.Minute))
			if got, err := SummarizeRunnerRuntimeHealth([]RunnerRuntimeHealth{first, second}, at, at.Add(time.Hour)); !errors.Is(err, ErrRunnerRuntimeWindow) || got != (RunnerRuntimeSummary{}) {
				t.Fatalf("invalid runtime series escaped: %+v / %v", got, err)
			}
		})
	}
	second := row(firstState, pin, healthExpected, at.Add(time.Minute))
	aliased := second
	aliased.CPUCumulativeNanoseconds = counter(101)
	for _, rows := range [][]RunnerRuntimeHealth{nil, {first, first}, {second, first}, {aliased}, make([]RunnerRuntimeHealth, MaxRunnerRuntimeSamples+1)} {
		if got, err := SummarizeRunnerRuntimeHealth(rows, at, at.Add(time.Hour)); !errors.Is(err, ErrRunnerRuntimeWindow) || got != (RunnerRuntimeSummary{}) {
			t.Fatalf("unordered, edited or overlimit series escaped: %+v / %v", got, err)
		}
	}
	for _, bounds := range [][2]time.Time{{at, at}, {at, at.Add(MaxRunnerRuntimeWindow + time.Nanosecond)}, {at, second.SampledAt}, {at.Add(time.Nanosecond), at.Add(time.Hour)}} {
		if _, err := SummarizeRunnerRuntimeHealth([]RunnerRuntimeHealth{first, second}, bounds[0], bounds[1]); !errors.Is(err, ErrRunnerRuntimeWindow) {
			t.Fatalf("invalid time window accepted: %v", err)
		}
	}
	limit := make([]RunnerRuntimeHealth, MaxRunnerRuntimeSamples)
	for i := range limit {
		limit[i] = row(firstState, pin, healthExpected, at.Add(time.Duration(i)*time.Minute))
	}
	until := at.Add(MaxRunnerRuntimeWindow)
	got, err := SummarizeRunnerRuntimeHealth(limit, at, until)
	if err != nil || got.Count != MaxRunnerRuntimeSamples || got.LatestCPUCumulativeNanoseconds == nil || *got.LatestCPUCumulativeNanoseconds != 100 {
		t.Fatalf("valid limit or absolute CPU rejected: %+v / %v", got, err)
	}
}
