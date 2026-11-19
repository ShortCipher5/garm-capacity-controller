//go:build linux

// Package nativecache observes global kernel ARC statistics. It deliberately
// exposes no enforced-limit or capacity-release capability: ARC targets are soft.
package nativecache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var ErrUnavailable = errors.New("fresh kernel ARC observation unavailable")

const Unqualified = "arc_target_not_enforced_memory_limit"

type Snapshot struct {
	State              string    `json:"state"`
	ObservedAt         time.Time `json:"observed_at"`
	KernelBootID       string    `json:"kernel_boot_id"`
	ModuleVersion      string    `json:"module_version"`
	ConfiguredMaxBytes uint64    `json:"configured_max_bytes"`
	TargetMinBytes     uint64    `json:"target_min_bytes"`
	TargetMaxBytes     uint64    `json:"target_max_bytes"`
	TargetBytes        uint64    `json:"target_bytes"`
	SizeBytes          uint64    `json:"size_bytes"`
}
type Observation struct {
	sample Snapshot
	began  time.Time
}

func (o Observation) Fresh() bool {
	age := time.Since(o.began)
	return !o.began.IsZero() && age >= 0 && age <= 5*time.Second && o.sample.State == Unqualified
}
func (o Observation) Snapshot() Snapshot {
	if !o.Fresh() {
		return Snapshot{}
	}
	return o.sample
}

// Observe reads only fixed actual procfs/sysfs files; no request can choose paths.
// Stats are a bounded sample, not an atomic global-memory inventory. Boot/module/
// tunable brackets reject observed identity/config drift but are not enforcement.
func Observe(ctx context.Context) (Observation, error) { return observe(ctx, kernelRead) }
func observe(ctx context.Context, read func(string, int64) ([]byte, error)) (Observation, error) {
	if ctx == nil || ctx.Err() != nil {
		return Observation{}, ErrUnavailable
	}
	began := time.Now()
	paths := []string{"/proc/sys/kernel/random/boot_id", "/sys/module/zfs/version", "/sys/module/zfs/parameters/zfs_arc_max"}
	vals := make([]string, 3)
	for i, p := range paths {
		raw, e := read(p, 4096)
		if e != nil {
			return Observation{}, ErrUnavailable
		}
		vals[i] = strings.TrimSpace(string(raw))
	}
	if !bootID(vals[0]) || !version(vals[1]) {
		return Observation{}, ErrUnavailable
	}
	configured, e := decimal(vals[2])
	if e != nil {
		return Observation{}, ErrUnavailable
	}
	raw, e := read("/proc/spl/kstat/zfs/arcstats", 1<<20)
	if e != nil {
		return Observation{}, ErrUnavailable
	}
	fields, e := parseStats(raw)
	if e != nil {
		return Observation{}, ErrUnavailable
	}
	for i, p := range paths {
		raw, e := read(p, 4096)
		if e != nil || strings.TrimSpace(string(raw)) != vals[i] {
			return Observation{}, ErrUnavailable
		}
	}
	if ctx.Err() != nil || time.Since(began) > 5*time.Second {
		return Observation{}, ErrUnavailable
	}
	out := Snapshot{State: Unqualified, ObservedAt: began.UTC(), KernelBootID: vals[0], ModuleVersion: vals[1], ConfiguredMaxBytes: configured, TargetMinBytes: fields["c_min"], TargetMaxBytes: fields["c_max"], TargetBytes: fields["c"], SizeBytes: fields["size"]}
	if out.TargetMinBytes == 0 || out.TargetMaxBytes < out.TargetMinBytes || out.TargetBytes < out.TargetMinBytes || out.TargetBytes > out.TargetMaxBytes {
		return Observation{}, ErrUnavailable
	}
	return Observation{sample: out, began: began}, nil
}
func decimal(s string) (uint64, error) {
	n, e := strconv.ParseUint(s, 10, 64)
	if e != nil || strconv.FormatUint(n, 10) != s {
		return 0, ErrUnavailable
	}
	return n, nil
}
func bootID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func version(s string) bool {
	if s == "2.2.6" {
		return true
	}
	if !strings.HasPrefix(s, "2.2.6-") || len(s) > 96 || len(s) == 6 {
		return false
	}
	for _, c := range s[6:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '+') {
			return false
		}
	}
	return true
}
func parseStats(raw []byte) (map[string]uint64, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return nil, ErrUnavailable
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 6 || len(lines) > 4096 || strings.Join(strings.Fields(lines[1]), " ") != "name type data" {
		return nil, ErrUnavailable
	}
	// The first kstat line has seven numeric/hex metadata columns, not data.
	header := strings.Fields(lines[0])
	if len(header) != 7 {
		return nil, ErrUnavailable
	}
	for _, v := range header {
		if _, e := strconv.ParseUint(v, 0, 64); e != nil {
			return nil, ErrUnavailable
		}
	}
	out := map[string]uint64{}
	for _, line := range lines[2:] {
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "4" || len(f[0]) > 128 {
			return nil, ErrUnavailable
		}
		if _, ok := out[f[0]]; ok {
			return nil, ErrUnavailable
		}
		n, e := decimal(f[2])
		if e != nil {
			return nil, e
		}
		out[f[0]] = n
	}
	for _, k := range []string{"c", "c_min", "c_max", "size"} {
		if _, ok := out[k]; !ok {
			return nil, ErrUnavailable
		}
	}
	return out, nil
}
func kernelRead(path string, limit int64) ([]byte, error) {
	// Component-by-component O_NOFOLLOW prevents symlink redirection. Verify the
	// actual final filesystem, not a regular file masquerading as proc/sys data.
	fd, e := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if i == len(parts)-1 {
			flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		}
		next, err := unix.Openat(fd, p, flags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var fs unix.Statfs_t
	if e = unix.Fstatfs(fd, &fs); e != nil {
		return nil, e
	}
	want := int64(unix.PROC_SUPER_MAGIC)
	if strings.HasPrefix(path, "/sys/") {
		want = unix.SYSFS_MAGIC
	}
	if int64(fs.Type) != want {
		return nil, ErrUnavailable
	}
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, ErrUnavailable
	}
	raw, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: bounded kernel read", ErrUnavailable)
	}
	return raw, nil
}
