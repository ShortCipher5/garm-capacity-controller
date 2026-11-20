package adminview

import (
	"sort"
	"strconv"
	"time"
)

const (
	maxRunnerDiskHealthPools = 8
	maxRunnerDiskHealthAge   = 5 * time.Minute
)

// RunnerDiskHealthReading is a display-only sample for an explicitly matched
// ledger host and runner storage pool. The source must already have verified
// the runner route and actual backing filesystem; a host-root reading, pool
// name or configured volume size alone cannot supply this input.
type RunnerDiskHealthReading struct {
	StoragePool     string
	ObservedAt      time.Time
	ExpiresAt       time.Time
	TotalBytes      int64
	AvailableBytes  int64
	TotalInodes     int64
	AvailableInodes int64
}

// RunnerDiskHealthSource supplies an immutable in-memory snapshot keyed by
// exact ledger host ID. Do not perform probe, network or filesystem I/O here.
// The application does not set a source by default.
type RunnerDiskHealthSource interface {
	SnapshotRunnerDiskHealth() map[string][]RunnerDiskHealthReading
}

// SetRunnerDiskHealthSource connects an optional display source before serving
// requests. It cannot configure thresholds, enable a brake or grant authority.
func (h *Handler) SetRunnerDiskHealthSource(source RunnerDiskHealthSource) {
	h.runnerDiskHealth = source
}

type runnerDiskHealthView struct {
	StoragePool     string    `json:"storage_pool"`
	State           string    `json:"state"`
	ObservedAt      time.Time `json:"observed_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	TotalBytes      string    `json:"total_bytes,omitempty"`
	AvailableBytes  string    `json:"available_bytes,omitempty"`
	TotalInodes     string    `json:"total_inodes,omitempty"`
	AvailableInodes string    `json:"available_inodes,omitempty"`
}

func projectRunnerDiskHealth(readings []RunnerDiskHealthReading, now time.Time) []runnerDiskHealthView {
	if len(readings) == 0 || len(readings) > maxRunnerDiskHealthPools || now.IsZero() {
		return nil
	}
	rows := make([]runnerDiskHealthView, 0, len(readings))
	seen := map[string]bool{}
	for _, r := range readings {
		ageLimit := r.ExpiresAt.Sub(r.ObservedAt)
		if !runnerDiskPoolName(r.StoragePool) || seen[r.StoragePool] || !runnerDiskWireTime(r.ObservedAt) || !runnerDiskWireTime(r.ExpiresAt) || r.ObservedAt.After(now) ||
			ageLimit <= 0 || ageLimit > maxRunnerDiskHealthAge ||
			r.TotalBytes <= 0 || r.AvailableBytes < 0 || r.AvailableBytes > r.TotalBytes ||
			r.TotalInodes <= 0 || r.AvailableInodes < 0 || r.AvailableInodes > r.TotalInodes {
			return nil
		}
		seen[r.StoragePool] = true
		row := runnerDiskHealthView{StoragePool: r.StoragePool, State: "stale", ObservedAt: r.ObservedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC()}
		if now.Before(r.ExpiresAt) {
			row.State = "current"
			// Decimal strings retain integer precision in JavaScript.
			row.TotalBytes = strconv.FormatInt(r.TotalBytes, 10)
			row.AvailableBytes = strconv.FormatInt(r.AvailableBytes, 10)
			row.TotalInodes = strconv.FormatInt(r.TotalInodes, 10)
			row.AvailableInodes = strconv.FormatInt(r.AvailableInodes, 10)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].StoragePool < rows[j].StoragePool })
	return rows
}

// These values are optional display data, so a timestamp that time.Time
// cannot marshal must never take down the rest of an authenticated overview.
func runnerDiskWireTime(t time.Time) bool {
	year := t.UTC().Year()
	return !t.IsZero() && year >= 1 && year <= 9999
}

func runnerDiskPoolName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r == '-' && i > 0)) {
			return false
		}
	}
	return true
}

func (h *Handler) attachRunnerDiskHealth(view *Overview) {
	if h.runnerDiskHealth == nil || len(view.Hosts) == 0 {
		return
	}
	readings := h.runnerDiskHealth.SnapshotRunnerDiskHealth()
	now := time.Now().UTC()
	for i := range view.Hosts {
		host := &view.Hosts[i]
		host.RunnerDiskHealth = projectRunnerDiskHealth(readings[host.ID], now)
	}
}
