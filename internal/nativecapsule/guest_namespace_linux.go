//go:build linux

package nativecapsule

import (
	"context"
	"golang.org/x/sys/unix"
	"time"
)

// PIDNamespaceTermination is termination of the captured namespace's PID1
// threadgroup and its descendant PID namespace tasks. It does not cover work
// delegated to external services, kernel references, physical memory or disk.
type PIDNamespaceTermination struct {
	namespace uint64
	observed  time.Time
}

func (o PIDNamespaceTermination) Terminated() bool       { return o.namespace != 0 && !o.observed.IsZero() }
func (o PIDNamespaceTermination) ObservedAt() time.Time  { return o.observed }
func (o PIDNamespaceTermination) NamespaceInode() uint64 { return o.namespace }
func pidfdTerminated(fd int) bool {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, e := unix.Poll(poll, 0)
	return e == nil && n == 1 && poll[0].Revents&unix.POLLIN != 0 && poll[0].Revents & ^int16(unix.POLLIN|unix.POLLHUP) == 0
}
func (g GuestKernel) ObservePIDNamespaceTermination(ctx context.Context) (PIDNamespaceTermination, error) {
	if g.state == nil {
		return PIDNamespaceTermination{}, ErrCapsule
	}
	s := g.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.owner.CheckScope(ctx) != nil {
		return PIDNamespaceTermination{}, ErrCapsule
	}
	var before, after unix.Stat_t
	kind, e := unix.IoctlRetInt(s.pidns, unix.NS_GET_NSTYPE)
	if e != nil || kind != unix.CLONE_NEWPID || unix.Fstat(s.pidns, &before) != nil || before.Ino == 0 || !pidfdTerminated(s.pidfd) {
		return PIDNamespaceTermination{}, ErrCapsule
	}
	if unix.Fstat(s.pidns, &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || s.owner.CheckScope(ctx) != nil || !pidfdTerminated(s.pidfd) {
		return PIDNamespaceTermination{}, ErrCapsule
	}
	return PIDNamespaceTermination{before.Ino, time.Now().UTC()}, nil
}
