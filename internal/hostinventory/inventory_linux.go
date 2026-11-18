// Package hostinventory is the fixed, unprivileged server-side inventory command.
// It never accepts paths or shell commands from an SSH client.
package hostinventory

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Values are Linux OS observations, not evidence of exclusive physical ownership.
type Values struct{ CPU, RAM, AvailableRAM, Disk, AvailableDisk *uint64 }

func readBounded(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(b) > 65536 {
		return nil, fmt.Errorf("inventory unavailable")
	}
	return b, nil
}

// CPUs parses the Linux online CPU list, rejecting overlapping or malformed ranges.
func CPUs(raw string) *uint64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 65536 {
		return nil
	}
	var total, last uint64
	first := true
	for _, part := range strings.Split(raw, ",") {
		ends := strings.Split(part, "-")
		if len(ends) > 2 {
			return nil
		}
		a, e := strconv.ParseUint(ends[0], 10, 32)
		if e != nil {
			return nil
		}
		b := a
		if len(ends) == 2 {
			b, e = strconv.ParseUint(ends[1], 10, 32)
			if e != nil {
				return nil
			}
		}
		if b < a || (!first && a <= last) || b > 1000000 {
			return nil
		}
		total += b - a + 1
		last = b
		first = false
	}
	if total == 0 || total > 1000000 {
		return nil
	}
	return &total
}
func memory(raw []byte) (*uint64, *uint64) {
	var total, available *uint64
	seen := map[string]bool{}
	scan := bufio.NewScanner(strings.NewReader(string(raw)))
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		if key != "MemTotal:" && key != "MemAvailable:" {
			continue
		}
		if seen[key] || len(fields) != 3 || fields[2] != "kB" {
			return nil, nil
		}
		seen[key] = true
		n, e := strconv.ParseUint(fields[1], 10, 64)
		if e != nil || n > 1<<50 {
			return nil, nil
		}
		if key == "MemTotal:" {
			total = &n
		} else {
			available = &n
		}
	}
	if scan.Err() != nil || (total != nil && *total < 1024) || (total != nil && available != nil && *available > *total) {
		return nil, nil
	}
	return total, available
}
func diskKib(blocks uint64, size int64) *uint64 {
	if size <= 0 || blocks > ^uint64(0)/uint64(size) {
		return nil
	}
	n := blocks * uint64(size) / 1024
	if n > 1<<50 {
		return nil
	}
	return &n
}
func Collect() Values {
	var v Values
	if raw, e := readBounded("/sys/devices/system/cpu/online"); e == nil {
		v.CPU = CPUs(string(raw))
	}
	if raw, e := readBounded("/proc/meminfo"); e == nil {
		v.RAM, v.AvailableRAM = memory(raw)
	}
	var fs syscall.Statfs_t
	if syscall.Statfs("/", &fs) == nil {
		v.Disk = diskKib(fs.Blocks, fs.Bsize)
		v.AvailableDisk = diskKib(fs.Bavail, fs.Bsize)
		if v.Disk != nil && v.AvailableDisk != nil && *v.AvailableDisk > *v.Disk {
			v.Disk = nil
			v.AvailableDisk = nil
		}
	}
	return v
}
func value(n *uint64) string {
	if n == nil {
		return "unknown"
	}
	return strconv.FormatUint(*n, 10)
}
func Write(w io.Writer, v Values) error {
	_, e := fmt.Fprintf(w, "garm_inventory_v1\ncpu_online=%s\nram_kib=%s\navailable_ram_kib=%s\nroot_disk_kib=%s\nroot_available_kib=%s\n", value(v.CPU), value(v.RAM), value(v.AvailableRAM), value(v.Disk), value(v.AvailableDisk))
	return e
}
