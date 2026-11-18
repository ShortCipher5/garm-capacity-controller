//go:build linux

package nativecapsule

import (
	"context"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GuestKernel retains actual kernel identities across STOP/DELETE. It cannot be
// reconstructed from a UUID, PID, path or persisted JSON. Copies share closure.
type GuestKernel struct{ state *guestKernel }
type guestKernel struct {
	originalCgroupPath   string
	mu                   sync.Mutex
	owner                *Running
	pidfd, pidns, cgroup int
	closed               bool
	pid                  int
}
type GuestExit struct {
	observed      time.Time
	exited, empty bool
}

func (o GuestExit) PID1Exited() bool      { return !o.observed.IsZero() && o.exited }
func (o GuestExit) CgroupEmpty() bool     { return !o.observed.IsZero() && o.empty }
func (o GuestExit) ObservedAt() time.Time { return o.observed }
func (g GuestKernel) Close() {
	if g.state == nil {
		return
	}
	s := g.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		unix.Close(s.pidfd)
		unix.Close(s.pidns)
		unix.Close(s.cgroup)
	}
}
func guestRead(path string, max int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrCapsule
	}
	return b, nil
}
func namespacePIDs(raw []byte) ([]int, error) {
	var result []int
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			if result != nil {
				return nil, ErrCapsule
			}
			for _, v := range strings.Fields(strings.TrimPrefix(line, "NSpid:")) {
				n, e := strconv.Atoi(v)
				if e != nil || n <= 0 {
					return nil, ErrCapsule
				}
				result = append(result, n)
			}
		}
	}
	if len(result) == 0 || len(result) > 32 {
		return nil, ErrCapsule
	}
	return result, nil
}
func guestPIDMatch(parent, child []int, reported, host int) bool {
	return reported > 1 && len(parent) > 0 && len(child) == len(parent)+1 && child[0] == host && child[len(parent)-1] == reported && child[len(child)-1] == 1
}
func guestCgroup(raw []byte, birth string) (string, error) {
	s := string(raw)
	if !strings.HasPrefix(s, "0::/") || !strings.HasSuffix(s, "\n") || strings.Count(s, "\n") != 1 {
		return "", ErrCapsule
	}
	p := strings.TrimSuffix(strings.TrimPrefix(s, "0::"), "\n")
	prefix := "/capsule-" + birth + "/"
	if !strings.HasPrefix(p, prefix) || filepath.Clean(p) != p || strings.Contains(p, "..") {
		return "", ErrCapsule
	}
	return strings.TrimPrefix(p, "/"), nil
}
func sameFD(a, b int) bool {
	var x, y unix.Stat_t
	return unix.Fstat(a, &x) == nil && unix.Fstat(b, &y) == nil && x.Dev == y.Dev && x.Ino == y.Ino
}
func openCgroup(path string) (int, error) {
	root, e := unix.Open("/sys/fs/cgroup", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return -1, e
	}
	defer unix.Close(root)
	return unix.Openat2(root, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
}

// ObserveGuestPID resolves Incus's namespace-relative PID through real procfs.
// The caller must bracket this with authenticated UUID/state reads. No mutation.
func (r *Running) ObserveGuestPID(ctx context.Context, reported int, name string, expected nativeboundary.Limits) (GuestKernel, error) {
	if !validGuestLimits(expected) || !guestName(name) || reported <= 1 || r.CheckScope(ctx) != nil {
		return GuestKernel{}, ErrCapsule
	}
	parent, e := r.staged.peer.PIDNamespace()
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	defer parent.Close()
	raw, e := guestRead(fmt.Sprintf("/proc/%d/status", r.staged.peer.PID()), 65536)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	pp, e := namespacePIDs(raw)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	proc, e := os.Open("/proc")
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	entries, e := proc.ReadDir(100001)
	proc.Close()
	if e == io.EOF {
		e = nil
	}
	if e != nil || len(entries) > 100000 {
		return GuestKernel{}, ErrCapsule
	}
	var result GuestKernel
	for _, entry := range entries {
		if ctx.Err() != nil {
			result.Close()
			return GuestKernel{}, ErrCapsule
		}
		pid, e := strconv.Atoi(entry.Name())
		if e != nil || pid <= 0 {
			continue
		}
		raw, e := guestRead("/proc/"+entry.Name()+"/status", 65536)
		if e != nil {
			continue
		}
		pids, e := namespacePIDs(raw)
		if e != nil || !guestPIDMatch(pp, pids, reported, pid) {
			continue
		}
		g, e := r.observeGuestCandidate(pid, reported, pp, int(parent.Fd()), name, expected)
		if e != nil {
			continue
		}
		if result.state != nil {
			g.Close()
			result.Close()
			return GuestKernel{}, ErrCapsule
		}
		result = g
	}
	if result.state == nil || r.CheckScope(ctx) != nil {
		result.Close()
		return GuestKernel{}, ErrCapsule
	}
	return result, nil
}
func (r *Running) observeGuestCandidate(pid, reported int, pp []int, parent int, name string, expected nativeboundary.Limits) (GuestKernel, error) {
	pfd, e := unix.PidfdOpen(pid, 0)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	s := &guestKernel{owner: r, pid: pid, pidfd: pfd, pidns: -1, cgroup: -1}
	g := GuestKernel{s}
	good := false
	defer func() {
		if !good {
			g.Close()
		}
	}()
	base := fmt.Sprintf("/proc/%d", pid)
	s.pidns, e = unix.Open(base+"/ns/pid", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	kind, e := unix.IoctlRetInt(s.pidns, unix.NS_GET_NSTYPE)
	if e != nil || kind != unix.CLONE_NEWPID {
		return GuestKernel{}, ErrCapsule
	}
	p, e := unix.IoctlRetInt(s.pidns, unix.NS_GET_PARENT)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	equal := sameFD(p, parent)
	unix.Close(p)
	if !equal {
		return GuestKernel{}, ErrCapsule
	}
	raw, e := guestRead(base+"/cgroup", 4096)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	path, e := guestCgroup(raw, r.BirthID())
	if e != nil || !exactGuestPath(path, r.BirthID(), name) {
		return GuestKernel{}, ErrCapsule
	}
	limitPath := strings.TrimSuffix(path, "/payload")
	limitFD, e := openCgroup(limitPath)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	defer unix.Close(limitFD)
	if readGuestLimits(limitFD, expected) != nil {
		return GuestKernel{}, ErrCapsule
	}
	s.cgroup, e = openCgroup(path)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	var fs unix.Statfs_t
	if unix.Fstatfs(s.cgroup, &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return GuestKernel{}, ErrCapsule
	}
	// Re-open and compare all mutable proc links after holding the pidfd.
	raw2, e := guestRead(base+"/cgroup", 4096)
	if e != nil || string(raw2) != string(raw) {
		return GuestKernel{}, ErrCapsule
	}
	status, e := guestRead(base+"/status", 65536)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	ps, e := namespacePIDs(status)
	if e != nil || !guestPIDMatch(pp, ps, reported, pid) {
		return GuestKernel{}, ErrCapsule
	}
	fd, e := unix.Open(base+"/ns/pid", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	equal = sameFD(fd, s.pidns)
	unix.Close(fd)
	if !equal || !pidLive(pfd) {
		return GuestKernel{}, ErrCapsule
	}
	fd, e = openCgroup(path)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	equal = sameFD(fd, s.cgroup)
	unix.Close(fd)
	if !equal {
		return GuestKernel{}, ErrCapsule
	}
	procs, e := readCgroupAt(s.cgroup, "cgroup.procs")
	if e != nil || !guestHasPID(procs, pid) || !pidLive(pfd) {
		return GuestKernel{}, ErrCapsule
	}
	link, e := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", s.cgroup))
	if e != nil || link != "/sys/fs/cgroup/"+path || strings.Contains(link, " (deleted)") {
		return GuestKernel{}, ErrCapsule
	}
	limitAgain, e := openCgroup(limitPath)
	if e != nil {
		return GuestKernel{}, ErrCapsule
	}
	sameLimit := sameFD(limitFD, limitAgain)
	unix.Close(limitAgain)
	if !sameLimit || readGuestLimits(limitFD, expected) != nil {
		return GuestKernel{}, ErrCapsule
	}
	s.originalCgroupPath = link
	good = true
	return g, nil
}
func readCgroupAt(fd int, name string) ([]byte, error) {
	n, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(n), name)
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(b) > 4096 {
		return nil, ErrCapsule
	}
	return b, nil
}
func cgroupUnpopulated(raw []byte) bool {
	seen := false
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return false
		}
		if f[0] == "populated" {
			if seen || f[1] != "0" {
				return false
			}
			seen = true
		}
	}
	return seen
}

// ObserveExit proves only the retained PID namespace init and cgroup object's
// current terminal/empty status. It does not prove deletion, prevent migration
// of other tasks, prove escaped tasks absent, or authorize capacity release.
func (g GuestKernel) ObserveExit(ctx context.Context) (GuestExit, error) {
	if g.state == nil {
		return GuestExit{}, ErrCapsule
	}
	s := g.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.owner.CheckScope(ctx) != nil {
		return GuestExit{}, ErrCapsule
	}
	poll := []unix.PollFd{{Fd: int32(s.pidfd), Events: unix.POLLIN}}
	n, e := unix.Poll(poll, 0)
	if e != nil || n != 1 || poll[0].Revents&unix.POLLIN == 0 || poll[0].Revents & ^int16(unix.POLLIN|unix.POLLHUP) != 0 {
		return GuestExit{}, ErrCapsule
	}
	var before, after unix.Stat_t
	if unix.Fstat(s.cgroup, &before) != nil {
		return GuestExit{}, ErrCapsule
	}
	raw, e := readCgroupAt(s.cgroup, "cgroup.events")
	if e != nil || !cgroupUnpopulated(raw) {
		return GuestExit{}, ErrCapsule
	}
	if unix.Fstat(s.cgroup, &after) != nil || before.Ino != after.Ino || before.Dev != after.Dev || before.Nlink != after.Nlink || s.owner.CheckScope(ctx) != nil {
		return GuestExit{}, ErrCapsule
	}
	return GuestExit{time.Now().UTC(), true, true}, nil
}

func guestHasPID(raw []byte, pid int) bool {
	for _, s := range strings.Fields(string(raw)) {
		if s == strconv.Itoa(pid) {
			return true
		}
	}
	return false
}

func guestName(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' && i > 0) {
			return false
		}
	}
	return true
}
func exactGuestPath(path, birth, name string) bool {
	return guestName(name) && path == "capsule-"+birth+"/guests/"+name+"/payload"
}
