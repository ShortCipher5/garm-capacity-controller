// Package hostmetrics periodically samples host utilization from the same
// verified Incus backends the gateway provisions through and appends the
// observations to the admission usage history. Samples are monitoring data
// only: they never gate admission, fits, charges or retirement.
package hostmetrics

import (
	"context"
	"errors"
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
	// GuestStateSource labels samples derived from per-guest instance state.
	GuestStateSource = "guest-state"

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
	// Now overrides the clock; nil uses time.Now. Intended for tests.
	Now func() time.Time
}

type hostState struct {
	baselineMillis int64
	baselineAt     time.Time
	hasBaseline    bool
	skipUntil      time.Time
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
	c.collect(ctx, c.now())
	if ctx.Err() != nil {
		return
	}
	timer := time.NewTicker(c.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-timer.C:
			c.collect(ctx, at)
		}
	}
}

// collect samples every host whose backoff window has expired. Hosts are
// sampled concurrently so one slow or failing host cannot delay another.
func (c *Collector) collect(ctx context.Context, at time.Time) {
	var wg sync.WaitGroup
	for host, source := range c.sources {
		c.mu.Lock()
		skip := at.Before(c.states[host].skipUntil)
		c.mu.Unlock()
		if skip {
			continue
		}
		wg.Add(1)
		go func(host string, source Source) {
			defer wg.Done()
			c.collectHost(ctx, host, source, at)
		}(host, source)
	}
	wg.Wait()
}

// collectHost samples exactly one host at `at`. A failed utilization read,
// a failed host-totals witness or a failed record backs the host off without
// touching its baseline; a counter reset (daemon restart) re-baselines
// silently. The recorded cpu_used_millis rate is derived from consecutive
// cumulative readings over the actual elapsed wall clock, so collection
// gaps dilute the rate instead of fabricating one.
func (c *Collector) collectHost(ctx context.Context, host string, source Source, at time.Time) {
	c.mu.Lock()
	state := c.states[host]
	c.mu.Unlock()
	sweep, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	usage, err := source.SampleUtilization(sweep)
	if err != nil {
		c.backOff(host, at, "utilization sample unavailable")
		return
	}
	// HostTotals witnesses the host capacity in the same sweep; a complete
	// sample requires it, but the durable usage row carries guest-state
	// utilization only because that is what the history schema records.
	if _, err := source.HostTotals(sweep); err != nil {
		c.backOff(host, at, "host totals witness unavailable")
		return
	}
	delta := usage.CPUUsedMillis - state.baselineMillis
	elapsed := at.Sub(state.baselineAt)
	state.baselineMillis = usage.CPUUsedMillis
	state.baselineAt = at
	switch {
	case !state.hasBaseline:
		state.hasBaseline = true
		return
	case delta < 0:
		// Cumulative counters reset on daemon restart: never derive a
		// negative rate; the fresh reading above is the new baseline.
		c.log("host metrics counter reset re-baselined " + host)
		return
	case elapsed < minSweepElapsed:
		return
	}
	rate := delta * int64(1000) / int64(elapsed/time.Millisecond)
	if err := c.recorder.RecordUsageSample(sweep, host, at, rate, usage.RAMUsedMiB, usage.DiskUsedMiB, GuestStateSource); err != nil {
		c.backOff(host, at, "usage sample record unavailable")
	}
}

func (c *Collector) backOff(host string, at time.Time, reason string) {
	c.mu.Lock()
	c.states[host].skipUntil = at.Add(c.backoff)
	c.mu.Unlock()
	c.log(reason + " for " + host + "; backing off")
}
