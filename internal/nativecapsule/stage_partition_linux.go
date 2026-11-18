//go:build linux

package nativecapsule

import (
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

// placeDaemon uses kernel self targeting (0), never a sampled numeric PID. All
// pre-move allocations remain covered by the finite persistent bootstrap budget.
// Neither that budget nor its historical charges are reclaimed on this move.
func placeDaemon(b bootSpec) error {
	if os.Getpid() != 1 || os.Geteuid() != 0 || !validBoot(b) {
		return ErrCapsule
	}
	base := "/capsule-" + b.BirthID
	raw, e := os.ReadFile("/proc/self/cgroup")
	if e != nil || string(raw) != "0::"+base+"/bootstrap/payload\n" {
		return ErrCapsule
	}
	fd, e := unix.Open("/sys/fs/cgroup"+base+"/init.scope", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return ErrCapsule
	}
	defer unix.Close(fd)
	if _, e = partitionInode(fd); e != nil {
		return ErrCapsule
	}
	// Host-root max files appear unmapped in this user namespace, so ownership is
	// checked independently by the host's retained partition capability, not here.
	check := func() error {
		for file, want := range partitionFiles(b.Partition.Daemon) {
			got, e := cgRead(fd, file)
			if e != nil || got != want {
				return ErrCapsule
			}
		}
		return nil
	}
	if check() != nil || cgWrite(fd, "cgroup.procs", "0") != nil {
		return ErrCapsule
	}
	raw, e = os.ReadFile("/proc/self/cgroup")
	if e != nil || strings.TrimSpace(string(raw)) != "0::"+base+"/init.scope" || check() != nil {
		return ErrCapsule
	}
	return nil
}
