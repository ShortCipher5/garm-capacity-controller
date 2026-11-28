package observerview

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"sort"
	"sync"
	"time"
)

const hostInterval = 30 * time.Second
const staleAfter = 90 * time.Second

type HeadroomSource interface {
	ReadHeadroom(context.Context) (sshinventory.Headroom, error)
}
type HeadroomRow struct {
	Host                string                 `json:"host"`
	LastObservedAt      *time.Time             `json:"last_observed_at,omitempty"`
	RunnerStorageStatus string                 `json:"runner_storage_status"`
	Status              string                 `json:"status"`
	Sample              *sshinventory.Headroom `json:"sample,omitempty"`
}
type headroomTarget struct {
	host   string
	source HeadroomSource
}
type HeadroomCache struct {
	targets []headroomTarget
	mu      sync.RWMutex
	rows    []HeadroomRow
}

func NewHeadroomCache(src map[string]HeadroomSource) (*HeadroomCache, error) {
	if len(src) > 8 {
		return nil, errors.New("too many whole-host sources")
	}
	c := &HeadroomCache{}
	for id, s := range src {
		if !hostID.MatchString(id) || s == nil {
			return nil, errors.New("invalid whole-host source")
		}
		c.targets = append(c.targets, headroomTarget{id, s})
	}
	sort.Slice(c.targets, func(i, j int) bool { return c.targets[i].host < c.targets[j].host })
	for _, t := range c.targets {
		c.rows = append(c.rows, HeadroomRow{Host: t.host, Status: "pending", RunnerStorageStatus: "unavailable_unbound_pool"})
	}
	return c, nil
}

// All sources are fixed at startup; only this worker can connect to them.
// Browsers cannot trigger SSH. No overlapping passes, no unbounded history.
func (c *HeadroomCache) Run(ctx context.Context) {
	if len(c.targets) == 0 {
		return
	}
	for ctx.Err() == nil {
		c.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(hostInterval):
		}
	}
}
func (c *HeadroomCache) poll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, t := range c.targets {
		wg.Go(func() {
			data, e := t.source.ReadHeadroom(ctx)
			c.mu.Lock()
			defer c.mu.Unlock()
			r := &c.rows[i]
			if e != nil || ctx.Err() != nil || data.ObservedAt.IsZero() {
				r.Status = "unavailable"
				return
			}
			r.Sample = &data
			r.LastObservedAt = &data.ObservedAt
			r.Status = "observed"
			if data.CPUIdleBasisPoints == nil || data.AvailableRAMMiB == nil || data.RootAvailableMiB == nil || data.LogicalCPUs == nil || data.RAMMiB == nil || data.RootDiskMiB == nil {
				r.Status = "partial"
			}
		})
	}
	wg.Wait()
}
func (c *HeadroomCache) snapshot(now time.Time) []HeadroomRow {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	rows := append([]HeadroomRow(nil), c.rows...)
	for i, r := range rows {
		if r.Status != "unavailable" && r.Sample != nil && now.Sub(r.Sample.ObservedAt) > staleAfter {
			rows[i].Status = "stale"
		}
		if rows[i].Status == "stale" || rows[i].Status == "unavailable" {
			rows[i].Sample = nil
		}
	}
	return rows
}
