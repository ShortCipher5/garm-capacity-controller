//go:build linux

package nativepreflight

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecache"
	"golang.org/x/sys/unix"
)

// MemoryPlan is a configuration arithmetic report, never a capacity capability.
// Reserve includes the OS, host services, ZFS/ARC and other uncontained usage.
// A configured reserve neither constrains that usage nor authorizes execution.
type MemoryPlan struct {
	State                       string                `json:"state"`
	ObservedAt                  time.Time             `json:"observed_at"`
	HostTotalBytes              uint64                `json:"host_total_bytes"`
	ConfiguredReserveBytes      uint64                `json:"configured_reserve_bytes"`
	PlannedCapsuleCeilingsBytes uint64                `json:"planned_capsule_ceilings_bytes"`
	UnassignedBytes             uint64                `json:"unassigned_bytes"`
	FitsPhysicalTotal           bool                  `json:"fits_physical_total"`
	ActivationAuthorized        bool                  `json:"activation_authorized"`
	ReleaseAuthorized           bool                  `json:"release_authorized"`
	ARC                         *nativecache.Snapshot `json:"arc,omitempty"`
	Warnings                    []string              `json:"warnings"`
}

func InspectMemoryPlan(ctx context.Context, reserve, capsules uint64) (MemoryPlan, error) {
	if ctx == nil || ctx.Err() != nil {
		return MemoryPlan{}, fmt.Errorf("memory plan cancelled")
	}
	began := time.Now()
	total, e := hostMemoryTotal()
	if e != nil {
		return MemoryPlan{}, e
	}
	r, e := evaluateMemoryPlan(total, reserve, capsules)
	if e != nil {
		return MemoryPlan{}, e
	}
	if o, err := nativecache.Observe(ctx); err == nil && o.Fresh() {
		s := o.Snapshot()
		r.ARC = &s
		if s.SizeBytes > reserve {
			r.Warnings = append(r.Warnings, "Observed ARC size alone exceeds the configured reserve; other host memory is additional.")
		}
	} else {
		r.Warnings = append(r.Warnings, "Fresh ARC diagnostics unavailable; no cache bound may be inferred.")
	}
	after, e := hostMemoryTotal()
	if e != nil || after != total || ctx.Err() != nil || time.Since(began) > 5*time.Second {
		return MemoryPlan{}, fmt.Errorf("memory plan observation changed or expired")
	}
	r.ObservedAt = began.UTC()
	return r, nil
}
func evaluateMemoryPlan(total, reserve, capsules uint64) (MemoryPlan, error) {
	if total == 0 || reserve == 0 || capsules == 0 || total > math.MaxInt64 || reserve > math.MaxInt64 || capsules > math.MaxInt64 {
		return MemoryPlan{}, fmt.Errorf("positive signed-range physical total, reserve and aggregate capsule ceilings required")
	}
	r := MemoryPlan{State: "blocked_unbounded_host_memory", HostTotalBytes: total, ConfiguredReserveBytes: reserve, PlannedCapsuleCeilingsBytes: capsules, Warnings: []string{"Configured reserve is an operator planning allowance, not an enforced host-global memory bound.", "ARC targets omit other ZFS/kernel allocations and may be exceeded. No automatic admission, release or reserve reduction is authorized."}}
	if reserve <= total && capsules <= total-reserve {
		r.FitsPhysicalTotal = true
		r.UnassignedBytes = total - reserve - capsules
	} else {
		r.State = "blocked_physical_overcommit"
		r.Warnings = append(r.Warnings, "Configured reserve plus aggregate capsule ceilings exceeds actual physical memory.")
	}
	return r, nil
}
func hostMemoryTotal() (uint64, error) {
	fd, e := unix.Open("/proc/meminfo", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return 0, e
	}
	f := os.NewFile(uintptr(fd), "/proc/meminfo")
	defer f.Close()
	var fs unix.Statfs_t
	var st unix.Stat_t
	if unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.PROC_SUPER_MAGIC || unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return 0, fmt.Errorf("unexpected memory diagnostic source")
	}
	raw, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(raw) > 65536 {
		return 0, fmt.Errorf("memory diagnostic read failed")
	}
	return parseMemoryTotal(string(raw))
}
func parseMemoryTotal(raw string) (uint64, error) {
	var result uint64
	count := 0
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "MemTotal:" {
			continue
		}
		count++
		if len(f) != 3 || f[2] != "kB" {
			return 0, fmt.Errorf("invalid physical memory units")
		}
		n, e := strconv.ParseUint(f[1], 10, 64)
		if e != nil || strconv.FormatUint(n, 10) != f[1] || n == 0 || n > math.MaxInt64/1024 {
			return 0, fmt.Errorf("invalid physical memory total")
		}
		result = n * 1024
	}
	if count != 1 {
		return 0, fmt.Errorf("missing or duplicate physical memory total")
	}
	return result, nil
}
