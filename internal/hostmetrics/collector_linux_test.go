package hostmetrics

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type fakeSource struct {
	mu          sync.Mutex
	sample      incusapi.UtilizationSample
	totals      incusapi.HostTotals
	sampleErr   error
	totalsErr   error
	sampleCalls int
	totalsCalls int
}

func (f *fakeSource) SampleUtilization(context.Context) (incusapi.UtilizationSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sampleCalls++
	return f.sample, f.sampleErr
}
func (f *fakeSource) HostTotals(context.Context) (incusapi.HostTotals, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.totalsCalls++
	return f.totals, f.totalsErr
}
func (f *fakeSource) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sampleCalls, f.totalsCalls
}

// fixtureUsage emits a typed synthetic container contribution, not a runtime
// identity. Extra inventory/history cases shape the members explicitly.
func fixtureUsage(cpu, ram, disk int64, guests int) incusapi.UtilizationSample {
	sample := incusapi.UtilizationSample{CPUUsedMillis: cpu, RAMUsedMiB: ram, DiskUsedMiB: disk, Guests: guests}
	sample.GuestCPU = make([]incusapi.GuestCPUObservation, guests)
	for i := range sample.GuestCPU {
		member := &sample.GuestCPU[i]
		member.Name = fmt.Sprintf("fixture-%d", i)
		member.CreatedAt = time.Unix(1, 0)
		member.StartedAt = time.Unix(2, 0)
		member.PID = int64(i + 3)
		// Give every member a positive raw counter, even with zero whole milliseconds.
		member.CPUUsedNanos = int64(time.Millisecond)*(cpu/int64(guests)) + 1
		if i == 0 {
			member.CPUUsedNanos += int64(time.Millisecond) * (cpu % int64(guests))
		}
	}
	return sample
}

type recorded struct {
	host          string
	at            time.Time
	cpuUsedMillis int64
	ramUsedMiB    int64
	diskUsedMiB   int64
	source        string
}

type fakeRecorder struct {
	mu      sync.Mutex
	samples []recorded
	err     error
}

func (f *fakeRecorder) RecordUsageSample(_ context.Context, host string, at time.Time, cpuUsedMillis, ramUsedMiB, diskUsedMiB int64, source string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.samples = append(f.samples, recorded{host, at, cpuUsedMillis, ramUsedMiB, diskUsedMiB, source})
	return nil
}
func (f *fakeRecorder) recorded() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.samples...)
}

// Supply the same explicit start to all hosts in direct fixture sweeps.
func (c *Collector) collect(ctx context.Context, at time.Time) {
	c.collectWithClock(ctx, func() time.Time { return at })
}

var errFixture = errors.New("private fixture failure")

func TestCollectorRebaselinesOnReportedGuestCountChange(t *testing.T) {
	start := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	source := &fakeSource{}
	recorder := &fakeRecorder{}
	collector, err := New(map[string]Source{"host-a": source}, recorder, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i, input := range []struct {
		guests int
		cpu    int64
	}{
		{1, 60_000}, {2, 361_000}, {2, 362_000},
		{1, 363_000}, {1, 364_000},
	} {
		source.sample = fixtureUsage(input.cpu, 0, 0, input.guests)
		collector.collect(context.Background(), start.Add(time.Duration(i)*time.Minute))
	}
	rows := recorder.recorded()
	if len(rows) != 2 || rows[0].cpuUsedMillis != 16 || rows[1].cpuUsedMillis != 16 ||
		!rows[0].at.Equal(start.Add(2*time.Minute)) || !rows[1].at.Equal(start.Add(4*time.Minute)) {
		t.Fatalf("expected a gap on both count changes, then rates of 16 millicores: %+v", rows)
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	source := &fakeSource{}
	recorder := &fakeRecorder{}
	if _, err := New(nil, recorder, Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("empty sources accepted")
	}
	if _, err := New(map[string]Source{"a": source}, nil, Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("nil recorder accepted")
	}
	if _, err := New(map[string]Source{"": source}, recorder, Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("empty host id accepted")
	}
	if _, err := New(map[string]Source{"bad\x01host": source}, recorder, Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("control-char host id accepted")
	}
	if _, err := New(map[string]Source{"a": nil}, recorder, Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("nil source accepted")
	}
	if _, err := New(map[string]Source{"a": source}, recorder, Options{Interval: MinInterval - time.Nanosecond}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("sub-minimum interval accepted")
	}
	collector, err := New(map[string]Source{"a": source}, recorder, Options{})
	if err != nil || collector.interval != DefaultInterval {
		t.Fatalf("defaults not applied: %v %v", collector, err)
	}
}

func TestCollectorFirstReadingBaselinesThenDerivesRate(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)}
	source := &fakeSource{sample: fixtureUsage(1000, 512, 256, 1), totals: incusapi.HostTotals{CPUMillisTotal: 8000}}
	recorder := &fakeRecorder{}
	collector, err := New(map[string]Source{"host-a": source}, recorder, Options{Interval: time.Minute, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	collector.collect(context.Background(), clock.now())
	if got := recorder.recorded(); len(got) != 0 {
		t.Fatalf("first reading recorded a sample %+v", got)
	}
	clock.advance(time.Minute)
	source.mu.Lock()
	source.sample = fixtureUsage(61000, 768, 300, 1)
	source.mu.Unlock()
	collector.collect(context.Background(), clock.now())
	got := recorder.recorded()
	if len(got) != 1 {
		t.Fatalf("expected one derived sample, got %+v", got)
	}
	sample := got[0]
	if sample.host != "host-a" || sample.at != clock.now() || sample.source != GuestStateSourceV2 {
		t.Fatalf("sample identity wrong %+v", sample)
	}
	if sample.cpuUsedMillis != 1000 {
		t.Fatalf("cpu rate = %d, want 1000 millis/s over one minute", sample.cpuUsedMillis)
	}
	if sample.ramUsedMiB != 768 || sample.diskUsedMiB != 300 {
		t.Fatalf("guest-state values not recorded %+v", sample)
	}
	if sampleCalls, totalsCalls := source.calls(); sampleCalls != 2 || totalsCalls != 2 {
		t.Fatalf("unexpected read pattern %d/%d", sampleCalls, totalsCalls)
	}
}

func TestCollectorCounterResetRebaselinesWithoutNegativeSample(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)}
	source := &fakeSource{sample: fixtureUsage(90000, 0, 0, 1)}
	recorder := &fakeRecorder{}
	collector, err := New(map[string]Source{"host-a": source}, recorder, Options{Interval: time.Minute, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	collector.collect(context.Background(), clock.now())
	clock.advance(time.Minute)
	source.mu.Lock()
	source.sample = fixtureUsage(5000, 0, 0, 1)
	source.mu.Unlock()
	collector.collect(context.Background(), clock.now())
	if got := recorder.recorded(); len(got) != 0 {
		t.Fatalf("counter reset recorded a sample %+v", got)
	}
	clock.advance(30 * time.Second)
	source.mu.Lock()
	source.sample = fixtureUsage(35000, 0, 0, 1)
	source.mu.Unlock()
	collector.collect(context.Background(), clock.now())
	got := recorder.recorded()
	if len(got) != 1 || got[0].cpuUsedMillis != 1000 {
		t.Fatalf("post-reset rate wrong %+v", got)
	}
}

func TestCollectorFailingHostNeverBlocksOthers(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)}
	failing := &fakeSource{sampleErr: errFixture}
	healthy := &fakeSource{sample: fixtureUsage(1000, 64, 32, 1)}
	totalsFailing := &fakeSource{totalsErr: errFixture}
	recorder := &fakeRecorder{}
	collector, err := New(map[string]Source{"failing": failing, "healthy": healthy, "no-totals": totalsFailing}, recorder, Options{Interval: time.Minute, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	collector.collect(context.Background(), clock.now())
	if _, totalsCalls := failing.calls(); totalsCalls != 0 {
		t.Fatal("failing host wasted a totals witness read")
	}
	clock.advance(time.Minute)
	healthy.mu.Lock()
	healthy.sample = fixtureUsage(3000, 128, 64, 1)
	healthy.mu.Unlock()
	totalsFailing.mu.Lock()
	totalsFailing.sample = fixtureUsage(7000, 0, 0, 1)
	totalsFailing.mu.Unlock()
	collector.collect(context.Background(), clock.now())
	got := recorder.recorded()
	if len(got) != 1 || got[0].host != "healthy" || got[0].cpuUsedMillis != 33 {
		t.Fatalf("healthy host sample missing or wrong %+v", got)
	}
	if calls, _ := failing.calls(); calls != 1 {
		t.Fatalf("backed-off failing host kept sampling: %d calls", calls)
	}
	if _, totalsCalls := totalsFailing.calls(); totalsCalls != 1 {
		t.Fatalf("backed-off totals-less host kept sampling: %d calls", totalsCalls)
	}
	// One more sweep while the failing hosts sit inside their backoff window.
	collector.collect(context.Background(), clock.now())
	if got := recorder.recorded(); len(got) != 1 {
		t.Fatalf("backed-off hosts recorded anyway %+v", got)
	}
	// Past the two-interval backoff the failing hosts are retried, and the
	// unchanged cumulative reading records an honest zero-rate sample.
	clock.advance(time.Minute)
	collector.collect(context.Background(), clock.now())
	if calls, _ := failing.calls(); calls != 2 {
		t.Fatalf("failing host was not retried after backoff: %d calls", calls)
	}
	got = recorder.recorded()
	if len(got) != 2 || got[1].host != "healthy" || got[1].cpuUsedMillis != 0 {
		t.Fatalf("post-backoff sweep wrong %+v", got)
	}
}

func TestCollectorRecordFailureBacksOffHostOnly(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)}
	source := &fakeSource{sample: fixtureUsage(1000, 0, 0, 1)}
	recorder := &fakeRecorder{err: errFixture}
	collector, err := New(map[string]Source{"host-a": source}, recorder, Options{Interval: time.Minute, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	collector.collect(context.Background(), clock.now())
	clock.advance(time.Minute)
	collector.collect(context.Background(), clock.now())
	if got := recorder.recorded(); len(got) != 0 {
		t.Fatalf("samples recorded despite failing store %+v", got)
	}
	recorder.mu.Lock()
	recorder.err = nil
	recorder.mu.Unlock()
	clock.advance(2 * time.Minute)
	source.mu.Lock()
	source.sample = fixtureUsage(61000, 0, 0, 1)
	source.mu.Unlock()
	collector.collect(context.Background(), clock.now())
	recovered := recorder.recorded()
	if len(recovered) != 1 {
		t.Fatalf("host never recovered after store healed: %+v", recovered)
	}
	// The last successful read was at the failed record; the healed store
	// therefore sees a rate diluted across the two-minute read gap.
	if recovered[0].cpuUsedMillis != 500 {
		t.Fatalf("rate should be diluted across the two-minute gap, got %+v", recovered)
	}
}

// Cancel while the first complete source sweep is returning, before Run can
// start its periodic timer. Polling every 5ms against a 10ms ticker can miss
// this boundary under scheduler load and accidentally test two sweeps.
type baselineCancelSource struct {
	*fakeSource
	cancel context.CancelFunc
}

func (s *baselineCancelSource) HostTotals(ctx context.Context) (incusapi.HostTotals, error) {
	totals, err := s.fakeSource.HostTotals(ctx)
	s.cancel()
	return totals, err
}

func TestRunStopsWithoutBaseliningCanceledWitness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &baselineCancelSource{fakeSource: &fakeSource{sample: fixtureUsage(1000, 0, 0, 1)}, cancel: cancel}
	recorder := &fakeRecorder{}
	collector, err := New(map[string]Source{"host-a": source}, recorder, Options{Interval: MinInterval})
	if err != nil {
		t.Fatal(err)
	}
	collector.interval = 10 * time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); collector.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collector did not stop")
	}
	calls, totals := source.calls()
	if calls != 1 || totals != 1 || collector.states["host-a"].baseline != nil {
		t.Fatal("canceled initial sweep should not establish a baseline", calls, totals)
	}
	time.Sleep(50 * time.Millisecond)
	if later, _ := source.calls(); later != calls {
		t.Fatal("collector kept sampling after stop")
	}
	if got := recorder.recorded(); len(got) != 0 {
		t.Fatalf("baseline-only run recorded samples %+v", got)
	}
}

func TestCollectorDoesNotRecordIncusSamplesWithoutGuestContinuity(t *testing.T) {
	fake := testsupport.NewFakeIncus(t, "metrics-incus")
	ctx := context.Background()
	spec := incusapi.InstanceSpec{Name: "runner-1", Type: "container", Profiles: []string{"runner-base"}, ImageFingerprint: strings.Repeat("a", 64), CPUMillis: 2000, RAMMiB: 2048, UserData: "#cloud-config\nmetrics-fixture", ControllerID: "controller-1", PoolID: "pool-1", ReservationKey: strings.Repeat("b", 64)}
	created, err := fake.Client.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := fake.Client.Wait(ctx, created); err != nil {
		t.Fatal(err)
	}
	fake.SetHostResources(8, 32<<30, [][2]int64{{1 << 40, 1 << 30}})
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	policy := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	clock := &fakeClock{at: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	collector, err := New(map[string]Source{"host-a": fake.Client}, store, Options{Interval: time.Minute, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	fake.SetGuestUsage("runner-1", 1000*int64(time.Millisecond), 2<<30, 3<<30)
	collector.collect(ctx, clock.now())
	clock.advance(time.Minute)
	fake.SetGuestUsage("runner-1", 61000*int64(time.Millisecond), 3<<30, 4<<30)
	collector.collect(ctx, clock.now())
	history, err := store.UsageHistory(ctx, "host-a", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("native rows without runtime continuity yielded durable samples: %+v", history)
	}
	if fake.ResourceReads.Load() == 0 || fake.StateReads.Load() == 0 {
		t.Fatal("collector did not exercise totals or state reads")
	}
}

func TestCollectorRecordsMatchingGuestStateIntoAdmissionStore(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	policy := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: at}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	source := &fakeSource{}
	collector, err := New(map[string]Source{"host-a": source}, store, Options{})
	if err != nil {
		t.Fatal(err)
	}
	source.sample = fixtureUsage(1000, 2048, 3072, 1)
	collector.collect(ctx, at)
	source.sample = fixtureUsage(61000, 3072, 4096, 1)
	at = at.Add(time.Minute)
	collector.collect(ctx, at)
	history, err := store.UsageHistory(ctx, "host-a", time.Time{}, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("expected one stored sample, got %+v (err=%v)", history, err)
	}
	sample := history[0]
	if sample.Source != GuestStateSourceV2 || sample.CPUUsedMillis != 1000 || sample.RAMUsedMiB != 3072 || sample.DiskUsedMiB != 4096 || !sample.SampledAt.Equal(at) {
		t.Fatalf("stored sample mismatch %+v", sample)
	}
}

// A one-sweep state failure must not turn a returning cumulative counter
// into recorded utilization or expose partial RAM/disk as known usage.
func TestCollectorDoesNotRecordOrBridgePartialGuestState(t *testing.T) {
	start := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	source := &fakeSource{}
	recorder := &fakeRecorder{}
	c, err := New(map[string]Source{"fixture": source}, recorder, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i, sample := range []incusapi.UtilizationSample{
		fixtureUsage(360000, 512, 50, 2),
		{CPUUsedMillis: 61000, RAMUsedMiB: 256, DiskUsedMiB: 25, Guests: 1,
			Errors: []incusapi.GuestUtilizationError{{Guest: "omitted", Err: errFixture}}},
		fixtureUsage(362000, 512, 50, 2),
		fixtureUsage(363000, 640, 55, 2),
	} {
		source.sample = sample
		// Call the host boundary directly so the regression is independent
		// of the retry backoff policy and simulates consecutive minute polls.
		c.collectHost(t.Context(), "fixture", source, start.Add(time.Duration(i)*time.Minute))
		if i < 3 && len(recorder.recorded()) != 0 {
			t.Fatalf("recorded an incomplete or unbracketed sample at poll %d", i)
		}
	}
	got := recorder.recorded()
	if len(got) != 1 || got[0].cpuUsedMillis != 16 || got[0].ramUsedMiB != 640 || got[0].diskUsedMiB != 55 || got[0].source != GuestStateSourceV2 {
		t.Fatalf("unexpected recovered history: %+v", got)
	}
}

func TestCollectorFailedUtilizationOrTotalsReadFencesBaseline(t *testing.T) {
	for _, which := range []string{"utilization", "totals", "rising subtotal", "invalid memory", "invalid disk"} {
		t.Run(which, func(t *testing.T) {
			start := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
			source, recorder := &fakeSource{}, &fakeRecorder{}
			collector, err := New(map[string]Source{"fixture": source}, recorder, Options{})
			if err != nil {
				t.Fatal(err)
			}
			source.sample = fixtureUsage(100000, 0, 0, 1)
			collector.collectHost(t.Context(), "fixture", source, start)

			source.sample = fixtureUsage(101000, 0, 0, 1)
			switch which {
			case "utilization":
				source.sampleErr = errFixture
			case "totals":
				source.totalsErr = errFixture
			case "rising subtotal":
				source.sample.Errors = []incusapi.GuestUtilizationError{{Guest: "omitted", Err: errFixture}}
			case "invalid memory":
				source.sample.RAMUsedMiB = -1
			case "invalid disk":
				source.sample.DiskUsedMiB = -1
			}
			collector.collectHost(t.Context(), "fixture", source, start.Add(time.Minute))
			if collector.states["fixture"].baseline != nil {
				t.Fatal("failed observation kept a CPU baseline")
			}

			source.sampleErr, source.totalsErr = nil, nil
			source.sample = fixtureUsage(500000, 0, 0, 1)
			collector.collectHost(t.Context(), "fixture", source, start.Add(2*time.Minute))
			if got := recorder.recorded(); len(got) != 0 {
				t.Fatalf("an unknown interval became a row: %+v", got)
			}
			source.sample = fixtureUsage(560000, 0, 0, 1)
			collector.collectHost(t.Context(), "fixture", source, start.Add(3*time.Minute))
			if got := recorder.recorded(); len(got) != 1 || got[0].cpuUsedMillis != 1000 || got[0].source != GuestStateSourceV2 {
				t.Fatalf("new consecutive observations failed: %+v", got)
			}
		})
	}
}

func TestDelayedTickerUsesActualHostSampleStart(t *testing.T) {
	start := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	clock := &fakeClock{at: start}
	source, recorder := &fakeSource{}, &fakeRecorder{}
	collector, err := New(map[string]Source{"fixture": source}, recorder, Options{Interval: MinInterval, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	source.sample = fixtureUsage(1_000, 1, 1, 1)
	collector.collect(t.Context(), start)
	for _, step := range []struct {
		tick, advance time.Duration
		cpu           int64
	}{{15 * time.Second, 20 * time.Second, 21_000}, {30 * time.Second, 10 * time.Second, 31_000}} {
		clock.advance(step.advance)
		source.sample = fixtureUsage(step.cpu, 1, 1, 1)
		ticks := make(chan time.Time, 1)
		ticks <- start.Add(step.tick)
		close(ticks)
		collector.runTicks(t.Context(), ticks)
	}
	rows := recorder.recorded()
	if len(rows) != 2 || !rows[0].at.Equal(start.Add(20*time.Second)) || !rows[1].at.Equal(start.Add(30*time.Second)) ||
		rows[0].cpuUsedMillis != 1000 || rows[1].cpuUsedMillis != 1000 {
		t.Fatalf("want the actual start and 1000 millicores for both samples, got %+v", rows)
	}
}

func TestCollectorDoesNotPersistReplayedWallTimeAfterRollback(t *testing.T) {
	start := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	source, recorder := &fakeSource{}, &fakeRecorder{}
	collector, err := New(map[string]Source{"fixture": source}, recorder, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i, seconds := range []int{0, 60, 15, 30, 60, 120, 180} {
		source.sample = fixtureUsage(int64(1000+i*60_000), 1, 1, 1)
		collector.collectHost(t.Context(), "fixture", source, start.Add(time.Duration(seconds)*time.Second))
	}
	rows := recorder.recorded()
	if len(rows) != 2 || !rows[0].at.Equal(start.Add(time.Minute)) || !rows[1].at.Equal(start.Add(3*time.Minute)) ||
		rows[0].cpuUsedMillis != 1000 || rows[1].cpuUsedMillis != 1000 {
		t.Fatalf("want no replayed or bridged rows after a wall-clock rollback, got %+v", rows)
	}
}
