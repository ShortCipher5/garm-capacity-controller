package incusapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const (
	runnerHealthVersion = "7.0.2"
	runnerHealthTimeout = 10 * time.Second
)

// RunnerRuntimeExpected identifies a previously and separately selected
// incarnation. Current names or mutable runner tags alone are insufficient.
// The caller uses an existing client for the previously reviewed daemon and
// project; this method cannot choose, discover or pin either.
type RunnerRuntimeExpected struct {
	RunnerRootVolumeExpected
	UUID, Generation string
	CreatedAt        time.Time
}

// RunnerRuntimeHealth is a non-atomic instance health observation, not
// attribution to a GitHub job or proof of continuity, placement or available
// capacity. SampledAt is the start of the final state request. Nil values
// indicate unobserved counters, never zero utilization. MemoryCgroupPeakBytes
// is the reported cgroup-lifetime high-water mark, not a job or sample-window
// peak and not a substitute for current memory.
type RunnerRuntimeHealth struct {
	SampledAt                time.Time `json:"sampled_at"`
	CPUCumulativeNanoseconds *int64    `json:"cpu_cumulative_nanoseconds,omitempty"`
	MemoryCurrentBytes       *int64    `json:"memory_current_bytes,omitempty"`
	MemoryCgroupPeakBytes    *int64    `json:"memory_cgroup_peak_bytes,omitempty"`
	sample                   runnerRuntimeSample
}

func (e RunnerRuntimeExpected) valid(now time.Time) bool {
	const nilUUID = "00000000-0000-0000-0000-000000000000"
	return e.RunnerRootVolumeExpected.valid() && operationID(e.UUID) && e.UUID != nilUUID &&
		operationID(e.Generation) && e.Generation != nilUUID && !e.CreatedAt.IsZero() && !e.CreatedAt.After(now)
}

// ReadRunnerRuntimeHealth brackets two named state reads with authenticated
// named metadata and pinned standalone root reads. It rejects detectable
// incarnation, process and counter drift. These independent GETs are not a
// fence against changes between reads. No recursion, backups, snapshots, job
// binding, disk gauges, rate derivation or runtime control are exposed.
func (c *Client) ReadRunnerRuntimeHealth(ctx context.Context, expected RunnerRuntimeExpected) (RunnerRuntimeHealth, error) {
	var none RunnerRuntimeHealth
	if c == nil || ctx == nil || !name(c.project) || c.pin == nil || !expected.valid(time.Now()) {
		return none, ErrConfiguration
	}
	pin := c.pin.observed.Load()
	if pin == nil || !pin.Valid() {
		return none, ErrConfiguration
	}
	if pin.ServerVersion() != runnerHealthVersion {
		return none, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, runnerHealthTimeout)
	defer cancel()
	if err := c.trusted(ctx, false); err != nil {
		return none, err
	}

	path := "/1.0/instances/" + url.PathEscape(expected.Name)
	read := func(target string) (json.RawMessage, error) {
		reply, err := c.request(ctx, http.MethodGet, target, nil, nil)
		if err != nil {
			return nil, err
		}
		if reply.kind != "sync" || reply.serverKeyFingerprint != pin.ServerKeyFingerprint() || ctx.Err() != nil {
			return nil, ErrResponse
		}
		return reply.metadata, nil
	}
	checkInstance := func() error {
		raw, err := read(path)
		if err != nil {
			return err
		}
		if !runnerHealthInstanceMatches(raw, c.project, expected) {
			return ErrResponse
		}
		return nil
	}
	readState := func() (runnerHealthState, time.Time, error) {
		at := time.Now().UTC()
		raw, err := read(path + "/state")
		if err != nil {
			return runnerHealthState{}, time.Time{}, err
		}
		result, err := parseRunnerHealthState(raw, expected.CreatedAt, at)
		return result, at, err
	}

	if err := checkInstance(); err != nil {
		return none, err
	}
	before, beforeAt, err := readState()
	if err != nil {
		return none, err
	}
	after, sampledAt, err := readState()
	if err != nil {
		return none, err
	}
	if !sampledAt.After(beforeAt) || before.pid != after.pid || !before.started.Equal(after.started) ||
		runnerHealthCounterDecreased(before.cpu, after.cpu) ||
		runnerHealthCounterDecreased(before.memoryPeak, after.memoryPeak) ||
		runnerHealthCounterDecreased(before.memory, after.memoryPeak) {
		return none, ErrResponse
	}
	if err := checkInstance(); err != nil {
		return none, err
	}
	if err := c.trusted(ctx, false); err != nil {
		return none, err
	}
	if ctx.Err() != nil || time.Now().Before(sampledAt) || (after.cpu == nil && after.memory == nil && after.memoryPeak == nil) {
		return none, ErrResponse
	}
	return RunnerRuntimeHealth{
		SampledAt: sampledAt, CPUCumulativeNanoseconds: after.cpu,
		MemoryCurrentBytes: after.memory, MemoryCgroupPeakBytes: after.memoryPeak,
		sample: newRunnerRuntimeSample(*pin, expected, after, sampledAt),
	}, nil
}

const (
	runnerUUIDKey       = "volatile.uuid"
	runnerGenerationKey = "volatile.uuid.generation"
)

func runnerHealthInstanceMatches(raw []byte, project string, expected RunnerRuntimeExpected) bool {
	m, err := runnerRootVolumeObject(raw, "name", "project", "type", "status", "status_code", "created_at", "config", "expanded_config")
	if err != nil || !runnerHealthRunning(m) {
		return false
	}
	for key, want := range map[string]string{"name": expected.Name, "project": project, "type": "container"} {
		got, err := field[string](m, key)
		if err != nil || got != want {
			return false
		}
	}
	created, err := identityTime(m, "created_at")
	if err != nil || !created.Equal(expected.CreatedAt) {
		return false
	}
	for _, source := range [...]string{"config", "expanded_config"} {
		config, err := runnerRootVolumeObject(m[source], stockRunnerControllerKey, stockRunnerPoolKey, runnerUUIDKey, runnerGenerationKey)
		if err != nil {
			return false
		}
		for key, want := range map[string]string{
			stockRunnerControllerKey: expected.ControllerID, stockRunnerPoolKey: expected.PoolID,
			runnerUUIDKey: expected.UUID, runnerGenerationKey: expected.Generation,
		} {
			got, err := field[string](config, key)
			if err != nil || got != want {
				return false
			}
		}
	}
	return true
}

type runnerHealthState struct {
	pid                     int64
	started                 time.Time
	cpu, memory, memoryPeak *int64
}

func parseRunnerHealthState(raw []byte, created, bound time.Time) (runnerHealthState, error) {
	var none runnerHealthState
	m, err := runnerRootVolumeObject(raw, "status", "status_code", "pid", "started_at", "cpu", "memory")
	if err != nil || !runnerHealthRunning(m) {
		return none, ErrResponse
	}
	pid, err := field[int64](m, "pid")
	if err != nil || pid <= 1 || pid > 1<<31-1 {
		return none, ErrResponse
	}
	started, err := identityTime(m, "started_at")
	if err != nil || started.Before(created) || started.After(bound) {
		return none, ErrResponse
	}
	cpuFields, err := runnerHealthCounterObject(m["cpu"], "usage")
	if err != nil {
		return none, err
	}
	cpu, err := runnerHealthCounter(cpuFields, "usage")
	if err != nil {
		return none, err
	}
	memoryFields, err := runnerHealthCounterObject(m["memory"], "usage", "usage_peak")
	if err != nil {
		return none, err
	}
	memory, err := runnerHealthCounter(memoryFields, "usage")
	if err != nil {
		return none, err
	}
	peak, err := runnerHealthCounter(memoryFields, "usage_peak")
	if err != nil || runnerHealthCounterDecreased(memory, peak) {
		return none, ErrResponse
	}
	return runnerHealthState{pid: pid, started: started, cpu: cpu, memory: memory, memoryPeak: peak}, nil
}

func runnerHealthRunning(m map[string]json.RawMessage) bool {
	status, a := field[string](m, "status")
	code, b := field[int](m, "status_code")
	return a == nil && b == nil && status == "Running" && code == 103
}

func runnerHealthCounterObject(raw json.RawMessage, critical ...string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	return runnerRootVolumeObject(raw, critical...)
}

func runnerHealthCounter(m map[string]json.RawMessage, key string) (*int64, error) {
	if raw, present := m[key]; !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	v, err := field[int64](m, key)
	if err != nil || v < -1 {
		return nil, ErrResponse
	}
	if v <= 0 {
		// Incus serializes its zero-value structs when controllers/readings
		// are unavailable and may report CPU -1 for failed accounting.
		return nil, nil
	}
	return &v, nil
}

func runnerHealthCounterDecreased(before, after *int64) bool {
	return before != nil && after != nil && *before > *after
}
