package incusapi

import (
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	MaxRunnerRuntimeSamples = 120
	MaxRunnerRuntimeWindow  = 24 * time.Hour
)

var ErrRunnerRuntimeWindow = errors.New("invalid or mixed Incus runtime health window")

// runnerRuntimeSample privately associates a fresh sampler result with its
// checked subject and reported process. Neither JSON round-trips nor edited
// public fields can invent evidence. It does not pin the effective profile,
// the cgroup namespace, or a reset-free counter epoch.
type runnerRuntimeSample struct {
	subject [32]byte
	at      time.Time
	values  [3]int64 // zero privately encodes an absent positive value
}

func newRunnerRuntimeSample(pin PhysicalServerIdentity, expected RunnerRuntimeExpected, state runnerHealthState, at time.Time) runnerRuntimeSample {
	subject := strings.Join([]string{
		pin.OriginFingerprint(), pin.ScopeFingerprint(), pin.ServerKeyFingerprint(), pin.ServerName(), pin.ServerVersion(),
		expected.Name, expected.ControllerID, expected.PoolID, expected.UUID, expected.Generation,
		expected.CreatedAt.UTC().Format(time.RFC3339Nano), strconv.FormatInt(state.pid, 10), state.started.UTC().Format(time.RFC3339Nano),
	}, "\x00")
	return runnerRuntimeSample{
		subject: sha256.Sum256([]byte(subject)), at: at,
		values: [3]int64{positiveHealthValue(state.cpu), positiveHealthValue(state.memory), positiveHealthValue(state.memoryPeak)},
	}
}

func positiveHealthValue(value *int64) int64 {
	if value != nil && *value > 0 {
		return *value
	}
	return 0
}

func (s runnerRuntimeSample) matches(row RunnerRuntimeHealth) bool {
	matches := func(value int64, shown *int64) bool {
		return value == 0 && shown == nil || value > 0 && shown != nil && *shown == value
	}
	return s.subject != ([32]byte{}) && s.at.Equal(row.SampledAt) &&
		matches(s.values[0], row.CPUCumulativeNanoseconds) &&
		matches(s.values[1], row.MemoryCurrentBytes) &&
		matches(s.values[2], row.MemoryCgroupPeakBytes)
}

// RunnerRuntimeSummary describes only the samples actually observed within
// one requested window. PeakSampledMemoryCurrentBytes is a maximum of the
// sampled current gauge, not an interval high-water mark. Latest values
// refer strictly to the final sample; an unavailable value never backfills.
type RunnerRuntimeSummary struct {
	Count, MemoryCurrentSamples    int
	FirstSampleAt, LatestSampleAt  time.Time
	LatestCPUCumulativeNanoseconds *int64
	LatestMemoryCurrentBytes       *int64
	LatestMemoryCgroupPeakBytes    *int64
	PeakSampledMemoryCurrentBytes  *int64
}

// SummarizeRunnerRuntimeHealth reduces a bounded, ascending set of real named
// sampler results for one exact pinned subject and observed process, within
// [from, until). An in-place profile change may contribute to the sampled
// maximum. Independent samples do not prove continuity or a comparable
// accounting interval; no rates, coverage, sizing, disk estimate or allocator
// evidence are derived.
func SummarizeRunnerRuntimeHealth(rows []RunnerRuntimeHealth, from, until time.Time) (RunnerRuntimeSummary, error) {
	var none RunnerRuntimeSummary
	from, until = from.UTC(), until.UTC()
	if len(rows) == 0 || len(rows) > MaxRunnerRuntimeSamples || from.IsZero() ||
		!until.After(from) || until.Sub(from) > MaxRunnerRuntimeWindow ||
		from.Year() < 1 || until.Year() > 9999 {
		return none, ErrRunnerRuntimeWindow
	}
	out := RunnerRuntimeSummary{Count: len(rows), FirstSampleAt: rows[0].sample.at}
	var cpu, peak, maxCurrent int64
	for _, row := range rows {
		e := row.sample
		if !e.matches(row) || e.subject != rows[0].sample.subject ||
			e.at.Before(from) || !e.at.Before(until) || !e.at.After(out.LatestSampleAt) {
			return none, ErrRunnerRuntimeWindow
		}
		c, mem, p := e.values[0], e.values[1], e.values[2]
		if (c > 0 && c < cpu) || (p > 0 && p < max(peak, maxCurrent, mem)) {
			return none, ErrRunnerRuntimeWindow
		}
		cpu, peak = max(cpu, c), max(peak, p)
		if mem > 0 {
			out.MemoryCurrentSamples++
			maxCurrent = max(maxCurrent, mem)
		}
		out.LatestSampleAt = e.at
	}
	value := func(v int64) *int64 {
		if v > 0 {
			return &v
		}
		return nil
	}
	last := rows[len(rows)-1].sample.values
	out.LatestCPUCumulativeNanoseconds, out.LatestMemoryCurrentBytes, out.LatestMemoryCgroupPeakBytes =
		value(last[0]), value(last[1]), value(last[2])
	out.PeakSampledMemoryCurrentBytes = value(maxCurrent)
	return out, nil
}
