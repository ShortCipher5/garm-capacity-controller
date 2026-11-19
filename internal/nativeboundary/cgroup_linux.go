//go:build linux

// Package nativeboundary observes an actual delegated host-domain boundary.
// It has no operator-boolean constructor and does not enable provisioning.
package nativeboundary

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var ErrBoundary = errors.New("enforced private native-domain boundary unavailable")

type Limits struct{ MemoryBytes, CPUMilli, Tasks int64 }
type Observation struct {
	limits               Limits
	namespace, rootInode uint64
	at                   time.Time
}

func (o Observation) Valid() bool {
	return o.namespace != 0 && o.rootInode != 0 && o.limits.MemoryBytes > 0 && o.limits.CPUMilli > 0 && o.limits.Tasks > 0 && !o.at.IsZero()
}
func (o Observation) Limits() Limits         { return o.limits }
func (o Observation) NamespaceInode() uint64 { return o.namespace }
func (o Observation) RootInode() uint64      { return o.rootInode }

// Observe requires an actual cgroup namespace different from host PID1 and a
// private cgroup2 mount with nsdelegate. Systemd's ProtectControlGroups property
// alone is insufficient because unsupported kernels can take fallback paths.
// The owner command must independently verify its system service/MainPID identity.
func Observe(want Limits) (Observation, error) {
	if os.Geteuid() != 0 || want.MemoryBytes <= 0 || want.CPUMilli <= 0 || want.Tasks <= 0 {
		return Observation{}, ErrBoundary
	}
	self, e := namespace("/proc/self/ns/cgroup")
	if e != nil {
		return Observation{}, e
	}
	host, e := namespace("/proc/1/ns/cgroup")
	if e != nil || self == host {
		return Observation{}, ErrBoundary
	}
	raw, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil || !privateMount(string(raw)) {
		return Observation{}, ErrBoundary
	}
	var fs unix.Statfs_t
	if unix.Statfs("/sys/fs/cgroup", &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return Observation{}, ErrBoundary
	}
	st, e := os.Stat("/sys/fs/cgroup")
	if e != nil {
		return Observation{}, ErrBoundary
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != 0 {
		return Observation{}, ErrBoundary
	}
	files := map[string]string{}
	for _, name := range []string{"memory.max", "memory.swap.max", "memory.oom.group", "cpu.max", "pids.max"} {
		b, e := os.ReadFile("/sys/fs/cgroup/" + name)
		if e != nil || len(b) > 128 {
			return Observation{}, ErrBoundary
		}
		files[name] = strings.TrimSpace(string(b))
	}
	actual, e := parseLimits(files, want)
	if e != nil {
		return Observation{}, e
	}
	// A changed namespace while sampling cannot create an observation.
	after, e := namespace("/proc/self/ns/cgroup")
	if e != nil || after != self {
		return Observation{}, ErrBoundary
	}
	return Observation{actual, self, sys.Ino, time.Now().UTC()}, nil
}
func namespace(path string) (uint64, error) {
	st, e := os.Stat(path)
	if e != nil {
		return 0, ErrBoundary
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Ino == 0 {
		return 0, ErrBoundary
	}
	return s.Ino, nil
}
func privateMount(raw string) bool {
	count := 0
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[4] != "/sys/fs/cgroup" {
			continue
		}
		count++
		sep := -1
		for i, v := range f {
			if v == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || sep+3 >= len(f) || f[3] != "/" || f[sep+1] != "cgroup2" || !option(f[5], "rw") || !option(f[sep+3], "nsdelegate") {
			return false
		}
		for _, v := range f[6:sep] {
			if strings.HasPrefix(v, "shared:") || strings.HasPrefix(v, "master:") {
				return false
			}
		}
	}
	return count == 1
}
func option(s, want string) bool {
	for _, v := range strings.Split(s, ",") {
		if v == want {
			return true
		}
	}
	return false
}
func positive(s string) (int64, error) {
	v, e := strconv.ParseInt(s, 10, 64)
	if e != nil || v <= 0 || strconv.FormatInt(v, 10) != s {
		return 0, ErrBoundary
	}
	return v, nil
}
func parseLimits(files map[string]string, want Limits) (Limits, error) {
	m, e := positive(files["memory.max"])
	if e != nil || m > want.MemoryBytes || files["memory.swap.max"] != "0" || files["memory.oom.group"] != "0" {
		return Limits{}, ErrBoundary
	}
	p, e := positive(files["pids.max"])
	if e != nil || p > want.Tasks {
		return Limits{}, ErrBoundary
	}
	cpu := strings.Fields(files["cpu.max"])
	if len(cpu) != 2 {
		return Limits{}, ErrBoundary
	}
	q, e := positive(cpu[0])
	if e != nil || q > (1<<63-1)/1000 {
		return Limits{}, ErrBoundary
	}
	period, e := positive(cpu[1])
	if e != nil || period < 1000 || period > 1000000 {
		return Limits{}, ErrBoundary
	}
	// Round upward: never under-report a fractional CPU quota.
	milli := q * 1000 / period
	if q*1000%period != 0 {
		milli++
	}
	if milli <= 0 || milli > want.CPUMilli {
		return Limits{}, ErrBoundary
	}
	if want.MemoryBytes <= 0 || want.CPUMilli <= 0 || want.Tasks <= 0 {
		return Limits{}, fmt.Errorf("%w: invalid budget", ErrBoundary)
	}
	return Limits{m, milli, p}, nil
}
