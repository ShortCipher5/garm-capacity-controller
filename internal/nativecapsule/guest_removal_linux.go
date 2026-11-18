//go:build linux

package nativecapsule

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"strings"
	"time"
)

// GuestCgroupRemoval describes only the exact retained cgroup kernel object.
// It does NOT establish absence of tasks migrated elsewhere or release authority.
type GuestCgroupRemoval struct {
	observed time.Time
	removed  bool
}

func (o GuestCgroupRemoval) Removed() bool         { return o.removed && !o.observed.IsZero() }
func (o GuestCgroupRemoval) ObservedAt() time.Time { return o.observed }
func removedHeldCgroup(fd int, original string) bool {
	if !strings.HasPrefix(original, "/sys/fs/cgroup/capsule-") || strings.Contains(original, " (deleted)") {
		return false
	}
	// Trigger ordinary kernfs revalidation, but NEVER infer removal from this
	// pathname lookup: replacement/rename also makes that lookup fail or differ.
	probe, e := openCgroup(strings.TrimPrefix(original, "/sys/fs/cgroup/"))
	if e == nil {
		same := sameFD(probe, fd)
		unix.Close(probe)
		if same {
			return false
		}
	} else if !errors.Is(e, unix.ENOENT) {
		return false
	}
	link, e := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if e != nil || link != original+" (deleted)" {
		return false
	}
	// Linux kernfs can invalidate a dentry on rename without destroying its
	// node. Its held directory still resolves cgroup.events while that node is
	// active. Require BOTH unlink marker and inactive built-in events interface.
	_, e = readCgroupAt(fd, "cgroup.events")
	if !errors.Is(e, unix.ENOENT) && !errors.Is(e, unix.ENODEV) {
		return false
	}
	eventError := e
	after, e := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	return e == nil && removedCgroupEvidence(original, link, eventError, after)
}
func (g GuestKernel) ObserveCgroupRemoval(ctx context.Context) (GuestCgroupRemoval, error) {
	if g.state == nil {
		return GuestCgroupRemoval{}, ErrCapsule
	}
	s := g.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.owner.CheckScope(ctx) != nil {
		return GuestCgroupRemoval{}, ErrCapsule
	}
	poll := []unix.PollFd{{Fd: int32(s.pidfd), Events: unix.POLLIN}}
	n, e := unix.Poll(poll, 0)
	if e != nil || n != 1 || poll[0].Revents&unix.POLLIN == 0 || poll[0].Revents & ^int16(unix.POLLIN|unix.POLLHUP) != 0 {
		return GuestCgroupRemoval{}, ErrCapsule
	}
	var before, after unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(s.cgroup, &before) != nil || unix.Fstatfs(s.cgroup, &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC || !removedHeldCgroup(s.cgroup, s.originalCgroupPath) {
		return GuestCgroupRemoval{}, ErrCapsule
	}
	if unix.Fstat(s.cgroup, &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || s.owner.CheckScope(ctx) != nil {
		return GuestCgroupRemoval{}, ErrCapsule
	}
	return GuestCgroupRemoval{time.Now().UTC(), true}, nil
}

func removedCgroupEvidence(original, link string, eventError error, after string) bool {
	return strings.HasPrefix(original, "/sys/fs/cgroup/capsule-") && !strings.Contains(original, " (deleted)") && link == original+" (deleted)" && after == link && (errors.Is(eventError, unix.ENOENT) || errors.Is(eventError, unix.ENODEV))
}
