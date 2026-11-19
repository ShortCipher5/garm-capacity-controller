package brokerinput

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"sort"
	"sync"
	"time"
)

// AdvisoryReader has no grant cycle, writer, or Run method. It exposes only
// authenticated observations and a non-binding projection.
type AdvisoryReader struct{ reader *planReader }

func NewAdvisoryReader(store *admission.SQLiteStore, source *garmapi.DemandClient) (*AdvisoryReader, error) {
	if store == nil || source == nil {
		return nil, capacitybroker.ErrSnapshot
	}
	return &AdvisoryReader{reader: &planReader{store: store, source: source, opt: capacitybroker.Options{MaxAge: time.Minute, MaxAssignments: 1000}, pressure: true}}, nil
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
		for _, v := range rows {
			if v.Source != "guest-state" || v.SampledAt.After(now) || now.Sub(v.SampledAt) > pressureAge {
				continue
			}
			if status.SampledAt.Before(v.SampledAt) {
				status.SampledAt = v.SampledAt
			}
			if status.State == "missing_or_stale_not_headroom" {
				status.State = "partial_sample_not_headroom"
			}
			if pressureHigh(v, state.Config.Hosts[id].Budget) {
				status.State = "high_partial_pressure_suppressed"
				blocked[id] = true
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, blocked, nil
}
func pressureHigh(v admission.UsageSample, b admission.Resources) bool {
	// ceil(90% of budget), calculated without multiplication overflow.
	high := func(used, limit int64) bool { return limit > 0 && used >= limit-limit/10 }
	return high(v.CPUUsedMillis, b.CPU) || high(v.RAMUsedMiB, b.RAM) || high(v.DiskUsedMiB, b.Disk)
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
	return Stock(snapshot, policy, observed, opt)
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
	return startAdvisoryLoop(ctx, reader.ReadPlan, interval), nil
}
func startAdvisoryLoop(ctx context.Context, read func(context.Context) (ReadPlan, error), interval time.Duration) *AdvisoryLoop {
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
			poll, stop := context.WithTimeout(ctx, 12*time.Second)
			result, err := read(poll)
			stop()
			s.mu.Lock()
			s.plan = nil
			s.failure = unavailable(PlanFailureReason(err), capacitybroker.ErrSnapshot)
			if err == nil && ctx.Err() == nil && result.ExpiresAt.After(time.Now()) {
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
	if !s.plan.ExpiresAt.After(time.Now()) {
		return ReadPlan{}, unavailable("snapshot_changed_or_expired", capacitybroker.ErrSnapshot)
	}
	return cloneReadPlan(*s.plan), nil
}
func cloneReadPlan(v ReadPlan) ReadPlan {
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
