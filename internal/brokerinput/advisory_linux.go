package brokerinput

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostmetrics"
)

// AdvisoryReader has no grant cycle, writer, or Run method. It exposes only
// authenticated observations and a non-binding projection.
type AdvisoryReader struct{ reader *planReader }

func NewAdvisoryReader(store *admission.SQLiteStore, source *garmapi.DemandClient) (*AdvisoryReader, error) {
	return newAdvisoryReader(store, source, false)
}

// NewAdvisoryReaderWithStockMaximumPreview reuses the existing read-only
// observation and pressure veto. It adds no writer or independent poller.
func NewAdvisoryReaderWithStockMaximumPreview(store *admission.SQLiteStore, source *garmapi.DemandClient) (*AdvisoryReader, error) {
	return newAdvisoryReader(store, source, true)
}

// NewAdvisoryReaderWithStockPreviews explicitly opts into independent read-only comparisons.
func NewAdvisoryReaderWithStockPreviews(store *admission.SQLiteStore, source *garmapi.DemandClient, maximum, idle bool) (*AdvisoryReader, error) {
	r, err := newAdvisoryReader(store, source, maximum)
	if err == nil {
		r.reader.stockIdlePreview = idle
	}
	return r, err
}

// NewAdvisoryReaderWithAllocatorProposal opts the bounded advisory tick into
// the additional observe-only cross-repository allocator proposal. The
// proposal reuses the same plan, lifecycle and cache; it adds no writer,
// poller or grant lifecycle.
func NewAdvisoryReaderWithAllocatorProposal(store *admission.SQLiteStore, source *garmapi.DemandClient, maximum, idle bool) (*AdvisoryReader, error) {
	r, err := NewAdvisoryReaderWithStockPreviews(store, source, maximum, idle)
	if err == nil {
		r.reader.allocatorProposal = true
	}
	return r, err
}
func newAdvisoryReader(store *admission.SQLiteStore, source *garmapi.DemandClient, preview bool) (*AdvisoryReader, error) {
	if store == nil || source == nil {
		return nil, capacitybroker.ErrSnapshot
	}
	return &AdvisoryReader{reader: &planReader{store: store, source: source, opt: capacitybroker.Options{MaxAge: time.Minute, MaxAssignments: 1000}, pressure: true, stockMaximumPreview: preview}}, nil
}
func (r *AdvisoryReader) ReadPlan(ctx context.Context) (ReadPlan, error) {
	if r == nil || r.reader == nil || ctx == nil || ctx.Err() != nil {
		return ReadPlan{}, capacitybroker.ErrSnapshot
	}
	return r.reader.read(ctx)
}

// PressureStatus reports partial guest monitoring against configured budgets,
// never physical host totals or evidence of free capacity.
type PressureStatus struct {
	Host      string    `json:"host"`
	State     string    `json:"state"`
	SampledAt time.Time `json:"sampled_at,omitempty"`
}

const pressureAge = 2 * time.Minute

func (p *planReader) readPressure(ctx context.Context, state admission.BrokerGrantState, now time.Time) ([]PressureStatus, map[string]bool, error) {
	statuses := []PressureStatus{}
	// Bound both database work and returned diagnostics. Large installations must
	// be scoped rather than silently skipping participating hosts.
	if len(state.Config.Hosts) > 64 {
		return nil, nil, capacitybroker.ErrSnapshot
	}
	ids := make([]string, 0, len(state.Config.Hosts))
	for id := range state.Config.Hosts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	blocked := map[string]bool{}
	for _, id := range ids {
		rows, err := p.store.UsageHistory(ctx, id, now.Add(-pressureAge), 1000)
		if err != nil {
			return nil, nil, err
		}
		status := PressureStatus{Host: id, State: "missing_or_stale_not_headroom"}
		// Oldest-first history may truncate newer data. Refuse to characterize it
		// as low: suppress new proposals if the bounded observation window overflows.
		if len(rows) == 1000 {
			status.State = "window_overflow_suppressed"
			blocked[id] = true
		}
		var latestHigh time.Time
		for _, v := range rows {
			if v.SampledAt.After(now) || !v.SampledAt.Add(pressureAge).After(now) {
				continue
			}
			budget := state.Config.Hosts[id].Budget
			var high bool
			switch v.Source {
			case hostmetrics.GuestStateSource:
				high = pressureHigh(v, budget)
			case hostmetrics.GuestStateSourceV2:
				// V2 disk counters can include shared devices. Use only high CPU
				// or RAM to veto advice. Low partial readings must not qualify
				// or renew the existing stock preview's freshness gate.
				if !cpuOrRAMPressureHigh(v, budget) {
					continue
				}
				high = true
			default:
				continue
			}
			if status.SampledAt.Before(v.SampledAt) {
				status.SampledAt = v.SampledAt
			}
			if status.State == "missing_or_stale_not_headroom" {
				status.State = "partial_sample_not_headroom"
			}
			if high && v.SampledAt.After(latestHigh) {
				latestHigh = v.SampledAt
			}
		}
		if !latestHigh.IsZero() {
			status.State = "high_partial_pressure_suppressed"
			// A newer low sample does not cancel an otherwise fresh veto or
			// extend its life. Report the high sample that still supports it.
			status.SampledAt = latestHigh
			blocked[id] = true
		}
		statuses = append(statuses, status)
	}
	return statuses, blocked, nil
}

// Bound the complete captured projection by its earliest expiring high veto.
// At or beyond this deadline a new poll may yield a different plan; this helper
// does not implicitly lift the veto or return unchecked placements.
func pressureVetoExpiry(expiry time.Time, statuses []PressureStatus) time.Time {
	for _, status := range statuses {
		if status.State == "high_partial_pressure_suppressed" {
			deadline := status.SampledAt.Add(pressureAge)
			if deadline.Before(expiry) {
				expiry = deadline
			}
		}
	}
	return expiry
}

func pressureHigh(v admission.UsageSample, b admission.Resources) bool {
	return cpuOrRAMPressureHigh(v, b) || atPressureThreshold(v.DiskUsedMiB, b.Disk)
}

func cpuOrRAMPressureHigh(v admission.UsageSample, b admission.Resources) bool {
	return atPressureThreshold(v.CPUUsedMillis, b.CPU) || atPressureThreshold(v.RAMUsedMiB, b.RAM)
}

func atPressureThreshold(used, limit int64) bool {
	// ceil(90% of budget), calculated without multiplication overflow.
	return limit > 0 && used >= limit-limit/10
}

// advisoryStock avoids vetoed hosts only for NEW placements, after canonical
// charges and provisional supply have been mapped. It cannot enable any host.
func advisoryStock(snapshot capacitybroker.Snapshot, policy admission.BrokerPolicy, observed garmapi.DemandObservation, opt capacitybroker.Options, blocked map[string]bool) (capacitybroker.JobPlan, error) {
	hosts := make(map[string]capacitybroker.Host, len(snapshot.Hosts))
	for id, h := range snapshot.Hosts {
		if blocked[id] {
			h.AcceptNew = false
		}
		hosts[id] = h
	}
	snapshot.Hosts = hosts
	return stock(snapshot, policy, observed, opt, true)
}

// AdvisoryLoop refreshes a cache of read-only projections. It contains no
// allocation callback or writer. Failed/expired observations are unavailable,
// never replaced by a fabricated empty plan.
type AdvisoryLoop struct {
	mu      sync.Mutex
	plan    *ReadPlan
	failure error
	cancel  context.CancelFunc
	done    chan struct{}
}

func StartAdvisoryLoop(ctx context.Context, reader *AdvisoryReader, interval time.Duration) (*AdvisoryLoop, error) {
	if ctx == nil || ctx.Err() != nil || reader == nil || interval < 15*time.Second || interval > time.Minute {
		return nil, capacitybroker.ErrSnapshot
	}
	return startAdvisoryLoop(ctx, reader.ReadPlan, interval, 12*time.Second), nil
}
func startAdvisoryLoop(ctx context.Context, read func(context.Context) (ReadPlan, error), interval, pollTimeout time.Duration) *AdvisoryLoop {
	ctx, cancel := context.WithCancel(ctx)
	s := &AdvisoryLoop{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer func() {
			s.mu.Lock()
			s.plan = nil
			s.failure = unavailable("preview_stopped", capacitybroker.ErrSnapshot)
			s.mu.Unlock()
		}()
		delay := interval
		for {
			if ctx.Err() != nil {
				return
			}
			poll, stop := context.WithTimeout(ctx, pollTimeout)
			result, err := read(poll)
			s.mu.Lock()
			// A late success cannot replace unavailable or older cached advice.
			// Check before stopping the child; stop itself cancels that context.
			now := time.Now()
			if deadline, _ := poll.Deadline(); err == nil && (poll.Err() != nil || !now.Before(deadline)) {
				err = unavailable("observation_unavailable", capacitybroker.ErrSnapshot)
			}
			stop()
			s.plan = nil
			s.failure = unavailable(PlanFailureReason(err), capacitybroker.ErrSnapshot)
			if err == nil && ctx.Err() == nil && result.ExpiresAt.After(now) {
				copy := cloneReadPlan(result)
				s.plan = &copy
				s.failure = nil
			}
			s.mu.Unlock()
			if err != nil {
				delay *= 2
				if delay > time.Minute {
					delay = time.Minute
				}
			} else {
				delay = interval
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return s
}
func (s *AdvisoryLoop) Close() {
	if s != nil {
		s.cancel()
		<-s.done
	}
}
func (s *AdvisoryLoop) ReadPlan(ctx context.Context) (ReadPlan, error) {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ReadPlan{}, capacitybroker.ErrSnapshot
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil {
		if s.failure != nil {
			return ReadPlan{}, s.failure
		}
		return ReadPlan{}, unavailable("awaiting_observation", capacitybroker.ErrSnapshot)
	}
	now := time.Now()
	if !s.plan.ExpiresAt.After(now) {
		return ReadPlan{}, unavailable("snapshot_changed_or_expired", capacitybroker.ErrSnapshot)
	}
	result := cloneReadPlan(*s.plan)
	result.StockMaximumPreview = stockMaximumAtRead(result.StockMaximumPreview, now)
	result.StockMaximumBatchPreview = stockMaximumAtRead(result.StockMaximumBatchPreview, now)
	if result.StockIdleDecisionPreview != nil && !result.StockIdleDecisionPreview.ExpiresAt.After(now) {
		result.StockIdleDecisionPreview = nil
	}
	if result.StockIdlePreview != nil && !result.StockIdlePreview.ExpiresAt.After(now) {
		result.StockIdlePreview = nil
	}
	return result, nil
}

// A maximum section may expire before the base plan. Preserve its own deadline
// so clients can distinguish expiry from an omitted (unconfigured) section,
// while stripping every expired proposal and its host evidence from the wire.
func stockMaximumAtRead(p *StockMaximumPreview, now time.Time) *StockMaximumPreview {
	if p == nil || p.ExpiresAt.After(now) {
		return p
	}
	return &StockMaximumPreview{State: "unavailable", ExpiresAt: p.ExpiresAt, Proposals: []StockMaximumProposal{}}
}

func cloneReadPlan(v ReadPlan) ReadPlan {
	if v.StockIdleDecisionPreview != nil {
		copy := *v.StockIdleDecisionPreview
		v.StockIdleDecisionPreview = &copy
	}
	if v.StockIdlePreview != nil {
		copy := *v.StockIdlePreview
		copy.Proposals = append([]StockIdleProposal{}, copy.Proposals...)
		v.StockIdlePreview = &copy
	}
	if v.Allocator != nil {
		copy := cloneAllocatorProposal(*v.Allocator)
		v.Allocator = &copy
	}
	v.StockMaximumPreview = cloneStockMaximumPreview(v.StockMaximumPreview)
	v.StockMaximumBatchPreview = cloneStockMaximumPreview(v.StockMaximumBatchPreview)
	v.Pressure = append([]PressureStatus(nil), v.Pressure...)
	v.Pools = append([]ReadPlanPool(nil), v.Pools...)
	for i := range v.Pools {
		if v.Pools[i].Supply != nil {
			s := *v.Pools[i].Supply
			v.Pools[i].Supply = &s
		}
	}
	return v
}

func cloneAllocatorProposal(v AllocatorProposal) AllocatorProposal {
	v.Awards = append([]AllocatorAward(nil), v.Awards...)
	v.Decisions = append([]AllocatorPoolDecision(nil), v.Decisions...)
	v.Risks = append([]string(nil), v.Risks...)
	if v.Held.Repositories != nil {
		repositories := make(map[string]admission.Resources, len(v.Held.Repositories))
		for id, used := range v.Held.Repositories {
			repositories[id] = used
		}
		v.Held.Repositories = repositories
	}
	if v.Held.Pools != nil {
		pools := make(map[string]admission.Resources, len(v.Held.Pools))
		for id, used := range v.Held.Pools {
			pools[id] = used
		}
		v.Held.Pools = pools
	}
	return v
}

func cloneStockMaximumPreview(preview *StockMaximumPreview) *StockMaximumPreview {
	if preview != nil {
		copy := *preview
		copy.Proposals = append([]StockMaximumProposal{}, copy.Proposals...)
		for i := range copy.Proposals {
			if copy.Proposals[i].ObservedMinIdle != nil {
				idle := *copy.Proposals[i].ObservedMinIdle
				copy.Proposals[i].ObservedMinIdle = &idle
			}
		}
		return &copy
	}
	return nil
}
