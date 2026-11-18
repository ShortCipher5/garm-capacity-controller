//go:build linux

package nativecapsule

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"strconv"
	"strings"
)

func validGuestLimits(v nativeboundary.Limits) bool {
	return v.MemoryBytes > 0 && v.CPUMilli > 0 && v.CPUMilli%1000 == 0 && v.CPUMilli/1000 <= 1<<20 && v.Tasks == 4096
}
func readGuestLimits(fd int, want nativeboundary.Limits) error {
	if !validGuestLimits(want) {
		return ErrCapsule
	}
	for name, value := range map[string]string{"memory.max": strconv.FormatInt(want.MemoryBytes, 10), "memory.swap.max": "0", "pids.max": strconv.FormatInt(want.Tasks, 10)} {
		got, e := cgRead(fd, name)
		if e != nil || got != value {
			return ErrCapsule
		}
	}
	raw, e := cgRead(fd, "cpuset.cpus.effective")
	if e != nil {
		return ErrCapsule
	}
	n, e := cpuSetCount(raw)
	if e != nil || n != want.CPUMilli/1000 {
		return ErrCapsule
	}
	return nil
}

// cpuset uses the kernel's canonical ascending disjoint CPU-list format. No
// allocation proportional to a caller range is needed; oversized IDs refuse.
func cpuSetCount(raw string) (int64, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return 0, ErrCapsule
	}
	last := int64(-1)
	count := int64(0)
	for _, part := range strings.Split(raw, ",") {
		f := strings.Split(part, "-")
		if len(f) < 1 || len(f) > 2 {
			return 0, ErrCapsule
		}
		lo, e := strconv.ParseInt(f[0], 10, 64)
		if e != nil || lo < 0 || lo >= 1<<20 || strconv.FormatInt(lo, 10) != f[0] || lo <= last {
			return 0, ErrCapsule
		}
		hi := lo
		if len(f) == 2 {
			hi, e = strconv.ParseInt(f[1], 10, 64)
			if e != nil || hi <= lo || hi >= 1<<20 || strconv.FormatInt(hi, 10) != f[1] {
				return 0, ErrCapsule
			}
		}
		count += hi - lo + 1
		last = hi
	}
	return count, nil
}
