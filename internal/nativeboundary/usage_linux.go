//go:build linux

package nativeboundary

import (
	"context"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Usage is a current whole-domain kernel accounting observation, not an
// attributable guest residual, reclamation proof or capacity release grant.
type Usage struct {
	memory, tasks    int64
	namespace, inode uint64
	at               time.Time
}

func (u Usage) MemoryBytes() int64     { return u.memory }
func (u Usage) Tasks() int64           { return u.tasks }
func (u Usage) NamespaceInode() uint64 { return u.namespace }
func (u Usage) RootInode() uint64      { return u.inode }
func (u Usage) ObservedAt() time.Time  { return u.at }
func (u Usage) Valid() bool {
	return u.memory >= 0 && u.tasks >= 0 && u.namespace != 0 && u.inode != 0 && !u.at.IsZero() && time.Since(u.at) >= 0 && time.Since(u.at) <= 5*time.Second
}
func counter(raw []byte) (int64, error) {
	s := strings.TrimSuffix(string(raw), "\n")
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n < 0 || strconv.FormatInt(n, 10) != s {
		return 0, ErrBoundary
	}
	return n, nil
}
func readCounterAt(fd int, name string) (int64, error) {
	n, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return 0, e
	}
	f := os.NewFile(uintptr(n), name)
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 129))
	if e != nil || len(raw) > 128 {
		return 0, ErrBoundary
	}
	return counter(raw)
}
func ObserveUsage(ctx context.Context, expected Observation) (Usage, error) {
	if ctx == nil || ctx.Err() != nil || !expected.Valid() {
		return Usage{}, ErrBoundary
	}
	began := time.Now()
	before, e := Observe(expected.Limits())
	if e != nil || before.RootInode() != expected.RootInode() || before.NamespaceInode() != expected.NamespaceInode() {
		return Usage{}, ErrBoundary
	}
	fd, e := unix.Open("/sys/fs/cgroup", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return Usage{}, ErrBoundary
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &st) != nil || st.Ino != expected.RootInode() || unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return Usage{}, ErrBoundary
	}
	memory, e := readCounterAt(fd, "memory.current")
	if e != nil {
		return Usage{}, ErrBoundary
	}
	swap, e := readCounterAt(fd, "memory.swap.current")
	if e != nil || swap != 0 {
		return Usage{}, ErrBoundary
	}
	tasks, e := readCounterAt(fd, "pids.current")
	if e != nil {
		return Usage{}, ErrBoundary
	}
	after, e := Observe(expected.Limits())
	if e != nil || after.RootInode() != before.RootInode() || after.NamespaceInode() != before.NamespaceInode() || time.Since(began) > 5*time.Second || ctx.Err() != nil {
		return Usage{}, ErrBoundary
	}
	return Usage{memory, tasks, before.NamespaceInode(), before.RootInode(), began}, nil
}
