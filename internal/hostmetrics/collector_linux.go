// Package hostmetrics periodically samples project guest utilization from the same
// verified Incus backends the gateway provisions through and appends the
// observations to the admission usage history. Samples are monitoring data
// only: they cannot establish host headroom, admission or retirement.
// Recent high observations may separately withhold allocator proposals.
package hostmetrics

import (
	"context"
	"errors"
	"math"
	"math/bits"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

var ErrConfiguration = errors.New("invalid host metrics configuration")

const (
	// DefaultInterval is the collection cadence when none is configured.
	DefaultInterval = time.Minute
	// MinInterval bounds collection frequency; shorter intervals are rejected.
	MinInterval = time.Second * 15
	// GuestStateSource identifies rows from the legacy collector.
	GuestStateSource = "guest-state"
	// GuestStateSourceV2 identifies project observations with incomplete
	// sweeps excluded and native disk reports included. Its disk totals
	// cannot establish backing-store pressure.
	GuestStateSourceV2 = "guest-state-v2"

	defaultSweepTimeout = 20 * time.Second
	minSweepElapsed     = time.Millisecond
)

// Source is the narrow backend read surface the collector needs. It is
// satisfied by *incusapi.Client and kept minimal so tests can fixture it.
type Source interface {
	HostTotals(context.Context) (incusapi.HostTotals, error)
	SampleUtilization(context.Context) (incusapi.UtilizationSample, error)
}

// Recorder persists one observed sample. It is satisfied by
// *admission.SQLiteStore.
type Recorder interface {
	RecordUsageSample(ctx context.Context, host string, at time.Time, cpuUsedMillis, ramUsedMiB, diskUsedMiB int64, source string) error
}

type Options struct {
	// Interval is the collection cadence; zero selects DefaultInterval and
	// values below MinInterval are rejected.
	Interval time.Duration
	// Timeout bounds one host sweep; zero selects a conservative default.
	Timeout time.Duration
	// Backoff is how long a failing host is skipped before retry; zero
	// selects twice the Interval.
	Backoff time.Duration
	// Log receives redacted operational notes; nil discards them.
	Log func(string)
	// Now overrides the clock; nil uses time.Now. Intended for tests and
	// must be safe for concurrent calls from independent host sweeps.
	Now func() time.Time
}

type hostState struct {
	baseline  *guestCPUBaseline
	skipUntil time.Time
	lastStart time.Time
}

type guestCPUBaseline struct {
	at     time.Time
	millis int64
	guests map[string]incusapi.GuestCPUObservation
	scope  incusapi.UtilizationSample
}

// Collector samples every configured host independently: one host failing
// never blocks or discards another host's observation.
type Collector struct {
	sources  map[string]Source
	recorder Recorder
	interval time.Duration
	timeout  time.Duration
	backoff  time.Duration
	log      func(string)
	now      func() time.Time
	mu       sync.Mutex
	states   map[string]*hostState
}

func validHost(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// New validates the configuration and returns a ready-to-run Collector.
// Sources must be non-empty with valid host ids; the recorder is mandatory.
func New(sources map[string]Source, recorder Recorder, options Options) (*Collector, error) {
	if len(sources) == 0 || recorder == nil {
		return nil, ErrConfiguration
	}
	for id, source := range sources {
		if !validHost(id) || source == nil {
			return nil, ErrConfiguration
		}
	}
	interval := options.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	if interval < MinInterval {
		return nil, ErrConfiguration
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultSweepTimeout
	}
	backoff := options.Backoff
	if backoff <= 0 {
		backoff = 2 * interval
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	log := options.Log
	if log == nil {
		log = func(string) {}
	}
	states := make(map[string]*hostState, len(sources))
	for id := range sources {
		states[id] = &hostState{}
	}
	return &Collector{sources: sources, recorder: recorder, interval: interval, timeout: timeout, backoff: backoff, log: log, now: now, states: states}, nil
}

// Run collects one baseline sweep immediately, then sweeps on every interval
// tick until ctx is canceled, which stops the collector promptly. Collection
// jitter is expected; sweeps never overlap.
func (c *Collector) Run(ctx context.Context) {
	c.collectWithClock(ctx, c.now)
	if ctx.Err() != nil {
		return
	}
	timer := time.NewTicker(c.interval)
	defer timer.Stop()
	c.runTicks(ctx, timer.C)
}

func (c *Collector) runTicks(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok || ctx.Err() != nil {
				return
			}
			// A tick only wakes the poll. The scheduled time can be old by
			// the time the previous sweep finishes.
			c.collectWithClock(ctx, c.now)
		}
	}
}

// collectWithClock starts every eligible host independently and reads its
// own sweep-start time, not a scheduled tick, a shared start, or completion.
func (c *Collector) collectWithClock(ctx context.Context, now func() time.Time) {
	var wg sync.WaitGroup
	for host, source := range c.sources {
		wg.Add(1)
		go func(host string, source Source) {
			defer wg.Done()
			at := now()
			c.mu.Lock()
			state := c.states[host]
			skip := at.Before(state.skipUntil)
			c.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			if skip {
				// A retained baseline must not cross a rollback seen
				// during backoff, even if the clock recovers before
				// the next eligible sweep.
				state.clearBaselineOnWallRollback(at.UTC())
				return
			}
			c.collectHost(ctx, host, source, at)
		}(host, source)
	}
	wg.Wait()
}

// collectHost samples exactly one host at `at`. A failed utilization read,
// a failed host-totals witness clears the baseline and backs the host off;
// a failed record backs the host off without discarding its valid baseline.
// A counter reset or a change in observed guest membership or runtime
// re-baselines. A complete sweep without continuity data cannot bridge rates.
// The recorded cpu_used_millis rate is derived from consecutive
// cumulative readings over elapsed sweep-start time (monotonic when
// available). The start is a conservative history timestamp, not the
// measurement time of every individual guest. Matching observed runtime
// fields are not proof of immutable guest identity or a physical host rate.
func (c *Collector) collectHost(ctx context.Context, host string, source Source, at time.Time) {
	c.mu.Lock()
	state := c.states[host]
	c.mu.Unlock()
	// Time.Before can prefer a monotonic reading when both times have one.
	// Compare the wall coordinates separately so a clock correction never
	// replays a history time or bridges a CPU rate across the rollback.
	wall := at.UTC()
	if state.clearBaselineOnWallRollback(wall) {
		return
	}
	if wall.Equal(state.lastStart) {
		return // No new observation time and no continuity inferred.
	}
	state.lastStart = wall
	sweep, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	usage, err := source.SampleUtilization(sweep)
	if err != nil || sweep.Err() != nil || len(usage.Errors) > 0 {
		state.baseline = nil
		c.backOff(host, at, "complete utilization sample unavailable")
		return
	}
	// HostTotals witnesses the host capacity in the same sweep; a complete
	// sample requires it, but the durable usage row carries guest-state
	// utilization only because that is what the history schema records.
	if _, err := source.HostTotals(sweep); err != nil || sweep.Err() != nil {
		state.baseline = nil
		c.backOff(host, at, "host totals witness unavailable")
		return
	}
	if usage.CPUUsedMillis < 0 || usage.RAMUsedMiB < 0 || usage.DiskUsedMiB < 0 || usage.Guests < 0 {
		state.baseline = nil
		c.backOff(host, at, "invalid guest utilization observation")
		return
	}
	previous := state.baseline
	current := newGuestCPUBaseline(usage, at)
	if sweep.Err() != nil {
		state.baseline = nil
		return
	}
	state.baseline = current
	if previous == nil || current == nil || !current.continues(previous) {
		return
	}
	elapsed := at.Sub(previous.at)
	if elapsed < minSweepElapsed {
		return
	}
	rate, ok := cpuRate(current.millis-previous.millis, elapsed)
	if !ok {
		c.backOff(host, at, "CPU observation rate out of range")
		return
	}
	if err := c.recorder.RecordUsageSample(sweep, host, at, rate, usage.RAMUsedMiB, usage.DiskUsedMiB, GuestStateSourceV2); err != nil {
		c.backOff(host, at, "usage sample record unavailable")
	}
}

// wall must not carry a monotonic reading. Per-host sweeps are serialized.
func (s *hostState) clearBaselineOnWallRollback(wall time.Time) bool {
	if wall.IsZero() || wall.Before(s.lastStart) {
		s.baseline = nil
		return true
	}
	return false
}

func newGuestCPUBaseline(usage incusapi.UtilizationSample, at time.Time) *guestCPUBaseline {
	if usage.GuestCPU == nil || len(usage.GuestCPU) != usage.Guests ||
		(usage.Guests == 0 && (usage.RAMUsedMiB != 0 || usage.DiskUsedMiB != 0)) {
		return nil
	}
	// Copy observations into owned storage. Do not keep a producer's slice
	// as the baseline for the next sweep.
	guests := make(map[string]incusapi.GuestCPUObservation, len(usage.GuestCPU))
	pids := make(map[int64]struct{}, len(usage.GuestCPU))
	var millis int64
	for _, guest := range usage.GuestCPU {
		if !validHost(guest.Name) || guest.PID <= 0 || guest.CreatedAt.IsZero() || guest.StartedAt.IsZero() ||
			guest.StartedAt.Before(guest.CreatedAt) || guest.CPUUsedNanos <= 0 {
			return nil
		}
		if _, duplicate := guests[guest.Name]; duplicate {
			return nil
		}
		if _, duplicate := pids[guest.PID]; duplicate {
			// One Linux host PID cannot identify two concurrent container
			// processes. Sequential reads may have crossed PID reuse.
			return nil
		}
		pids[guest.PID] = struct{}{}
		counterMillis := guest.CPUUsedNanos / int64(time.Millisecond)
		if counterMillis > math.MaxInt64-millis {
			return nil
		}
		millis += counterMillis
		guests[guest.Name] = guest
	}
	if millis != usage.CPUUsedMillis {
		return nil
	}
	// Retain the producer's private sweep identity without its mutable slices.
	usage.GuestCPU, usage.Errors = nil, nil
	return &guestCPUBaseline{at: at, millis: millis, guests: guests, scope: usage}
}

func (b *guestCPUBaseline) continues(previous *guestCPUBaseline) bool {
	if len(b.guests) != len(previous.guests) || b.scope.GuestCPUObservedScopeChanged(previous.scope) {
		return false
	}
	for name, current := range b.guests {
		prior, exists := previous.guests[name]
		if !exists || current.PID != prior.PID || !current.CreatedAt.Equal(prior.CreatedAt) ||
			!current.StartedAt.Equal(prior.StartedAt) || current.CPUUsedNanos < prior.CPUUsedNanos {
			return false
		}
	}
	return true
}

func (c *Collector) backOff(host string, at time.Time, reason string) {
	c.mu.Lock()
	c.states[host].skipUntil = at.Add(c.backoff)
	c.mu.Unlock()
	c.log(reason + " for " + host + "; backing off")
}

// cpuRate converts cumulative CPU milliseconds into millicores without allowing
// multiplication overflow to turn a high observation into apparently low usage.
// An unrepresentable rate is unavailable, never clamped or reported as zero.
func cpuRate(delta int64, elapsed time.Duration) (int64, bool) {
	if delta < 0 || elapsed < minSweepElapsed {
		return 0, false
	}
	divisor := uint64(elapsed / time.Millisecond)
	hi, lo := bits.Mul64(uint64(delta), 1000)
	if hi >= divisor {
		return 0, false
	}
	rate, _ := bits.Div64(hi, lo, divisor)
	if rate > math.MaxInt64 {
		return 0, false
	}
	return int64(rate), true
}
