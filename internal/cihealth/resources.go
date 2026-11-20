package cihealth

import (
	"errors"
	"math"
	"math/bits"
	"time"
)

const (
	MaxJobResourceSamples = 4096
	MaxJobResourceWindow  = 24 * time.Hour
	MaxJobResourceGap     = 2 * time.Minute
)

var ErrJobResourceFacts = errors.New("invalid or oversized CI job resource facts")

// JobResourceScope is supplied by an independent verified job/runner join.
// The opaque runner incarnation is not a runner name, host ID or route. This
// reducer checks equality but does not authenticate or establish attribution.
type JobResourceScope struct {
	Job               JobKey
	RunnerIncarnation string
	StartedAt, EndAt  time.Time // exact job start and report cutoff
	AsOf              time.Time // when these facts were observed, not render time
	MaxGap            time.Duration
}

// ResourceValue distinguishes an observed zero from a missing metric.
type ResourceValue struct {
	Known bool
	Value uint64
}

type JobResourceSample struct {
	Job               JobKey
	RunnerIncarnation string
	At                time.Time
	// CPUTimeNanos and Disk{Read,Write}Bytes are cumulative counters for
	// this incarnation. RAMBytes and DiskUsedBytes are point-in-time gauges.
	CPUTimeNanos, RAMBytes, DiskReadBytes, DiskWriteBytes, DiskUsedBytes ResourceValue
}

type ResourceCoverage struct {
	Intervals int
	Duration  time.Duration
	Complete  bool // covers the entire requested window, not the whole runner
}

type JobCPURates struct {
	// Average is over covered time only. Peak is the largest observed
	// interval average, not an instantaneous CPU peak. Both are rounded down.
	AverageMilliCPU, PeakIntervalMilliCPU uint64
	Coverage                              ResourceCoverage
}

type ResourceGaugeSummary struct {
	Samples           int
	SampleMax, Latest uint64
	LatestAt          time.Time
}

type JobRAMAverage struct {
	// Time-weighted estimate using linear interpolation between covered pairs.
	Bytes    uint64
	Coverage ResourceCoverage
}

type JobResourceCounterDelta struct {
	Bytes    uint64 // observed increase only; never extrapolated over gaps
	Coverage ResourceCoverage
}

type JobResourceSummary struct {
	Scope     JobResourceScope
	Window    time.Duration
	Samples   int
	CPU       *JobCPURates
	RAM       *ResourceGaugeSummary
	RAMMean   *JobRAMAverage
	DiskRead  *JobResourceCounterDelta
	DiskWrite *JobResourceCounterDelta
	DiskUsed  *ResourceGaugeSummary
}

// SummarizeJobResources analyzes one bounded, independent job/incarnation
// window; it does not estimate missing edges or bridge missing values/gaps.
// Historical windows remain historical: AsOf bounds the observation, not a
// request to certify current pressure. Results are display facts, never host
// headroom, resource recommendations or scaling/admission permissions.
func SummarizeJobResources(scope JobResourceScope, samples []JobResourceSample) (JobResourceSummary, error) {
	start, end, asOf := scope.StartedAt.UTC(), scope.EndAt.UTC(), scope.AsOf.UTC()
	window := end.Sub(start)
	if !validKey(scope.Job.Attempt) || scope.Job.JobID <= 0 || !validSource(scope.RunnerIncarnation) ||
		!validTime(start) || !validTime(end) || !validTime(asOf) || end.After(asOf) ||
		window <= 0 || window > MaxJobResourceWindow || scope.MaxGap <= 0 || scope.MaxGap > MaxJobResourceGap || len(samples) > MaxJobResourceSamples {
		return JobResourceSummary{}, ErrJobResourceFacts
	}
	out := JobResourceSummary{Scope: scope, Window: window, Samples: len(samples)}
	var lastAt time.Time
	var cpu, diskRead, diskWrite resourceCounter
	var ramWeight wideResourceSum
	var ramCoverage ResourceCoverage
	var peakCPU uint64
	for i, s := range samples {
		at := s.At.UTC()
		if s.Job != scope.Job || s.RunnerIncarnation != scope.RunnerIncarnation || !validTime(at) ||
			at.Before(start) || at.After(end) || !lastAt.IsZero() && !at.After(lastAt) ||
			!validResourceValue(s.CPUTimeNanos) || !validResourceValue(s.RAMBytes) || !validResourceValue(s.DiskReadBytes) ||
			!validResourceValue(s.DiskWriteBytes) || !validResourceValue(s.DiskUsedBytes) {
			return JobResourceSummary{}, ErrJobResourceFacts
		}
		var dt time.Duration
		var prev JobResourceSample
		if i != 0 {
			prev = samples[i-1]
			dt = at.Sub(lastAt)
			if dt > scope.MaxGap {
				dt = 0 // don't credit an interval whose sampling cadence was lost
			}
		}
		lastAt = at
		if delta, ok := cpu.include(prev.CPUTimeNanos, s.CPUTimeNanos, dt); !ok {
			return JobResourceSummary{}, ErrJobResourceFacts
		} else if dt > 0 && prev.CPUTimeNanos.Known && s.CPUTimeNanos.Known {
			rate, ok := milliCPURate(delta, dt)
			if !ok {
				return JobResourceSummary{}, ErrJobResourceFacts
			}
			peakCPU = max(peakCPU, rate)
		}
		if _, ok := diskRead.include(prev.DiskReadBytes, s.DiskReadBytes, dt); !ok {
			return JobResourceSummary{}, ErrJobResourceFacts
		}
		if _, ok := diskWrite.include(prev.DiskWriteBytes, s.DiskWriteBytes, dt); !ok {
			return JobResourceSummary{}, ErrJobResourceFacts
		}
		out.RAM = appendResourceGauge(out.RAM, s.RAMBytes, at)
		out.DiskUsed = appendResourceGauge(out.DiskUsed, s.DiskUsedBytes, at)
		if dt > 0 && prev.RAMBytes.Known && s.RAMBytes.Known {
			// Doubling the numerator avoids rounding intermediate endpoint means.
			ramWeight.addProduct(prev.RAMBytes.Value, uint64(dt))
			ramWeight.addProduct(s.RAMBytes.Value, uint64(dt))
			ramCoverage.add(dt)
		}
	}
	if cpu.coverage.Intervals != 0 {
		average, ok := milliCPURate(cpu.delta, cpu.coverage.Duration)
		if !ok {
			return JobResourceSummary{}, ErrJobResourceFacts
		}
		out.CPU = &JobCPURates{average, peakCPU, cpu.coverage.withWindow(window)}
	}
	if ramCoverage.Intervals != 0 {
		denominator := 2 * uint64(ramCoverage.Duration)
		average, _ := bits.Div64(ramWeight.hi, ramWeight.lo, denominator)
		out.RAMMean = &JobRAMAverage{average, ramCoverage.withWindow(window)}
	}
	out.DiskRead, out.DiskWrite = diskRead.summarize(window), diskWrite.summarize(window)
	return out, nil
}

func validResourceValue(v ResourceValue) bool { return v.Known || v.Value == 0 }

func appendResourceGauge(out *ResourceGaugeSummary, v ResourceValue, at time.Time) *ResourceGaugeSummary {
	if v.Known {
		if out == nil {
			out = new(ResourceGaugeSummary)
		}
		out.Samples++
		out.SampleMax = max(out.SampleMax, v.Value)
		out.Latest, out.LatestAt = v.Value, at
	}
	return out
}

func (c *ResourceCoverage) add(dt time.Duration) {
	c.Intervals++
	c.Duration += dt
}

func (c ResourceCoverage) withWindow(window time.Duration) ResourceCoverage {
	c.Complete = c.Duration == window
	return c
}

type resourceCounter struct {
	last     ResourceValue
	delta    uint64
	coverage ResourceCoverage
}

func (c *resourceCounter) include(prev, latest ResourceValue, dt time.Duration) (uint64, bool) {
	if !latest.Known {
		return 0, true
	}
	// A gap doesn't justify treating a lower counter as a new incarnation.
	if c.last.Known && latest.Value < c.last.Value {
		return 0, false
	}
	c.last = latest
	if dt <= 0 || !prev.Known {
		return 0, true
	}
	delta := latest.Value - prev.Value
	if delta > math.MaxUint64-c.delta {
		return 0, false
	}
	c.delta += delta
	c.coverage.add(dt)
	return delta, true
}

func (c resourceCounter) summarize(window time.Duration) *JobResourceCounterDelta {
	if c.coverage.Intervals == 0 {
		return nil
	}
	return &JobResourceCounterDelta{c.delta, c.coverage.withWindow(window)}
}

func milliCPURate(nanos uint64, elapsed time.Duration) (uint64, bool) {
	hi, lo := bits.Mul64(nanos, 1000)
	denominator := uint64(elapsed)
	if elapsed <= 0 || hi >= denominator {
		return 0, false
	}
	rate, _ := bits.Div64(hi, lo, denominator)
	return rate, true
}

type wideResourceSum struct{ hi, lo uint64 }

func (s *wideResourceSum) addProduct(value, weight uint64) {
	// Sum of weights for both endpoints is at most 2*MaxJobResourceWindow
	// in nanoseconds, so at most 2^112 even for maximal gauges.
	hi, lo := bits.Mul64(value, weight)
	var carry uint64
	s.lo, carry = bits.Add64(s.lo, lo, 0)
	s.hi, _ = bits.Add64(s.hi, hi, carry)
}
