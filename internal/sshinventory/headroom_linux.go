package sshinventory

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"strconv"
	"strings"
	"time"
)

// Headroom describes the pinned SSH target's OS and root filesystem. It is
// deliberately not an input type to admission or scaling.
type Headroom struct {
	ObservedAt         time.Time `json:"observed_at"`
	CPUIdleBasisPoints *uint64   `json:"cpu_idle_basis_points,omitempty"`
	CPUWindowMillis    int64     `json:"cpu_window_ms"`
	LogicalCPUs        *int64    `json:"logical_cpus,omitempty"`
	RAMMiB             *int64    `json:"ram_mib,omitempty"`
	AvailableRAMMiB    *int64    `json:"available_ram_mib,omitempty"`
	RootDiskMiB        *int64    `json:"root_disk_mib,omitempty"`
	RootAvailableMiB   *int64    `json:"root_available_mib,omitempty"`
}

func (c *Client) ReadHeadroom(ctx context.Context) (Headroom, error) {
	data, e := c.runFixed(ctx, hostinventory.HeadroomCommand, "")
	if e != nil {
		return Headroom{}, e
	}
	return parseHeadroom(data, time.Now().UTC())
}
func parseHeadroom(data []byte, now time.Time) (Headroom, error) {
	parts := strings.SplitN(string(data), "\n", 5)
	if len(parts) != 5 || parts[0] != hostinventory.HeadroomCommand {
		return Headroom{}, ErrInventory
	}
	ms, e := strconv.ParseInt(parts[1], 10, 64)
	if e != nil || ms <= 0 {
		return Headroom{}, ErrInventory
	}
	t := time.UnixMilli(ms).UTC()
	if t.After(now.Add(time.Second)) {
		return Headroom{}, ErrInventory
	}
	w, e := strconv.ParseInt(parts[2], 10, 64)
	if e != nil || w < 0 || w > 15000 {
		return Headroom{}, ErrInventory
	}
	var idle *uint64
	if parts[3] != "unknown" {
		n, e := strconv.ParseUint(parts[3], 10, 16)
		if e != nil || n > 10000 || w < 1000 || w > 5000 {
			return Headroom{}, ErrInventory
		}
		idle = &n
	}
	v, e := parse([]byte(parts[4]))
	if e != nil {
		return Headroom{}, e
	}
	return Headroom{ObservedAt: t, CPUIdleBasisPoints: idle, CPUWindowMillis: w, LogicalCPUs: v.LogicalCPUs, RAMMiB: v.RAMMiB, AvailableRAMMiB: v.AvailableRAMMiB, RootDiskMiB: v.RootDiskMiB, RootAvailableMiB: v.RootAvailableMiB}, nil
}
