//go:build linux

// Package capsulepeer authenticates a live paused bootstrap process through an
// actual Unix peer credential and kernel namespace descriptors. It does not
// attest a complete runtime image/device policy or grant native execution.
package capsulepeer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeids"
	"golang.org/x/sys/unix"
)

var ErrPeer = errors.New("fresh namespace bootstrap peer unavailable; quarantine birth")

type Peer struct {
	mu                sync.Mutex
	pid, pidfd        int
	userns, netns     int
	uid               uint32
	birth             string
	closed            bool
	ids               map[string]uint64
	gid               uint32
	count             uint64
	helperSHA, cgroup string
	fds               map[string]int
	managedRoot       uint64
}

func (p *Peer) BirthID() string {
	if p == nil {
		return ""
	}
	return p.birth
}
func (p *Peer) PID() int {
	if p == nil {
		return 0
	}
	return p.pid
}
func (p *Peer) UID() uint32 {
	if p == nil {
		return 0
	}
	return p.uid
}
func (p *Peer) GID() uint32 {
	if p == nil {
		return 0
	}
	return p.gid
}

func (p *Peer) aliveLocked() bool {
	if p.closed || p.pidfd < 0 {
		return false
	}
	v := []unix.PollFd{{Fd: int32(p.pidfd), Events: unix.POLLIN}}
	n, e := unix.Poll(v, 0)
	return e == nil && n == 0
}
func (p *Peer) Alive() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.aliveLocked()
}

// Namespace descriptors are duplicated from the held real kernel objects, never
// reconstructed from a caller PID/path. Closing a Peer never kills its process.
func (p *Peer) UserNamespace() (*os.File, error)    { return p.duplicate(true) }
func (p *Peer) NetworkNamespace() (*os.File, error) { return p.duplicate(false) }
func (p *Peer) duplicate(user bool) (*os.File, error) {
	if p == nil {
		return nil, ErrPeer
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.aliveLocked() {
		return nil, ErrPeer
	}
	fd := p.netns
	if user {
		fd = p.userns
	}
	dup, e := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if e != nil {
		return nil, ErrPeer
	}
	return os.NewFile(uintptr(dup), "held-native-namespace"), nil
}
func (p *Peer) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	unix.Close(p.pidfd)
	for _, fd := range p.fds {
		unix.Close(fd)
	}
}

// Observe consumes one reserved construction attempt and checks the actual peer
// before returning any namespace handle. Expected helper SHA is pinned by the
// runtime-image manifest, not provided by the peer. Limits must be a live opaque
// observation of the independently managed parent domain. This is bootstrap
// identity, not proof that nspawn's full mount/device policy has been qualified.
func Observe(conn *net.UnixConn, ids nativeids.Reservation, helperSHA string, limits nativeboundary.Observation) (*Peer, error) {
	return observe(conn, ids, helperSHA, limits, false)
}
func ObserveManaged(conn *net.UnixConn, ids nativeids.Reservation, helperSHA string, limits nativeboundary.Observation) (*Peer, error) {
	return observe(conn, ids, helperSHA, limits, true)
}
func observe(conn *net.UnixConn, ids nativeids.Reservation, helperSHA string, limits nativeboundary.Observation, managed bool) (*Peer, error) {
	if conn == nil || os.Geteuid() != 0 || !limits.Valid() || !digest(helperSHA) {
		return nil, ErrPeer
	}
	fresh, e := nativeboundary.Observe(limits.Limits())
	if e != nil || fresh.NamespaceInode() != limits.NamespaceInode() || fresh.RootInode() != limits.RootInode() {
		return nil, ErrPeer
	}
	id, e := ids.Claim()
	if e != nil {
		return nil, ErrPeer
	}
	raw, e := conn.SyscallConn()
	if e != nil {
		return nil, ErrPeer
	}
	var cred *unix.Ucred
	var ce error
	if raw.Control(func(fd uintptr) { cred, ce = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }) != nil || ce != nil || cred == nil || cred.Pid <= 1 || uint64(cred.Uid) != id.UIDStart || uint64(cred.Gid) != id.GIDStart {
		return nil, ErrPeer
	}
	pid := int(cred.Pid)
	pidfd, e := unix.PidfdOpen(pid, 0)
	if e != nil {
		return nil, ErrPeer
	}
	p := &Peer{pid: pid, pidfd: pidfd, userns: -1, netns: -1, uid: cred.Uid, birth: id.BirthID, gid: cred.Gid, count: id.Count, helperSHA: helperSHA, ids: map[string]uint64{}, fds: map[string]int{}}
	if managed {
		p.managedRoot = limits.RootInode()
	}
	success := false
	defer func() {
		if !success {
			p.Close()
		}
	}()
	if !p.Alive() {
		return nil, ErrPeer
	}
	root := fmt.Sprintf("/proc/%d", pid)
	uid, e := bounded(root+"/uid_map", 4096)
	if e != nil || !mapping(uid, id.UIDStart, id.Count) {
		return nil, ErrPeer
	}
	gid, e := bounded(root+"/gid_map", 4096)
	if e != nil || !mapping(gid, id.GIDStart, id.Count) {
		return nil, ErrPeer
	}
	status, e := bounded(root+"/status", 65536)
	if e != nil || !namespaceInit(status, pid) {
		return nil, ErrPeer
	}
	cg, e := bounded(root+"/cgroup", 4096)
	if e != nil || !containedCgroup(cg) || managed && string(cg) != "0::/capsule-"+p.birth+"/bootstrap/payload\n" {
		return nil, ErrPeer
	}
	// The exact cgroup is resolved inside the observed private parent hierarchy;
	// parent-relative paths from outside that hierarchy are rejected.
	p.cgroup = string(cg)
	cgPath := strings.TrimSuffix(strings.TrimPrefix(string(cg), "0::"), "\n")
	procs, e := bounded(filepath.Join("/sys/fs/cgroup", cgPath, "cgroup.procs"), 1<<20)
	if e != nil || !hasPID(procs, pid) {
		return nil, ErrPeer
	}
	for _, n := range []struct {
		name string
		kind int
	}{{"user", unix.CLONE_NEWUSER}, {"net", unix.CLONE_NEWNET}, {"pid", unix.CLONE_NEWPID}, {"mnt", unix.CLONE_NEWNS}, {"ipc", unix.CLONE_NEWIPC}, {"cgroup", unix.CLONE_NEWCGROUP}} {
		fd, e := unix.Open(root+"/ns/"+n.name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if e != nil {
			return nil, ErrPeer
		}
		var a, b unix.Stat_t
		kind, ke := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
		bad := unix.Fstat(fd, &a) != nil || unix.Stat("/proc/self/ns/"+n.name, &b) != nil || kind != n.kind || ke != nil
		if managed && n.name == "cgroup" {
			bad = bad || a.Ino != limits.NamespaceInode() || a.Ino != b.Ino
		} else {
			bad = bad || a.Ino == b.Ino
		}
		if bad {
			unix.Close(fd)
			return nil, ErrPeer
		}
		p.ids[n.name] = a.Ino
		if n.name != "user" {
			owner, oe := unix.IoctlRetInt(fd, unix.NS_GET_USERNS)
			var ownerStat, userStat unix.Stat_t
			badOwner := oe != nil
			if oe == nil {
				badOwner = unix.Fstat(owner, &ownerStat) != nil
				if managed && n.name == "cgroup" {
					badOwner = badOwner || unix.Stat("/proc/self/ns/user", &userStat) != nil
				} else {
					badOwner = badOwner || unix.Fstat(p.userns, &userStat) != nil
				}
				badOwner = badOwner || ownerStat.Ino != userStat.Ino
				unix.Close(owner)
			}
			if badOwner {
				unix.Close(fd)
				return nil, ErrPeer
			}
		}
		switch n.name {
		case "user":
			p.userns = fd
		case "net":
			p.netns = fd
		}
		p.fds[n.name] = fd
	}
	exe, e := os.Open(root + "/exe")
	if e != nil {
		return nil, ErrPeer
	}
	st, se := exe.Stat()
	h := sha256.New()
	_, he := io.Copy(h, io.LimitReader(exe, 128<<20+1))
	exe.Close()
	if se != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > 128<<20 || he != nil || hex.EncodeToString(h.Sum(nil)) != helperSHA || !p.Alive() {
		return nil, ErrPeer
	}
	// Recheck the credential-bound namespace maps after holding descriptors and
	// hashing, before exposing any capability to a fresh-dataset provisioner.
	uid, e = bounded(root+"/uid_map", 4096)
	if e != nil || !mapping(uid, id.UIDStart, id.Count) {
		return nil, ErrPeer
	}
	gid, e = bounded(root+"/gid_map", 4096)
	if e != nil || !mapping(gid, id.GIDStart, id.Count) || !p.Alive() {
		return nil, ErrPeer
	}
	if !p.Paused() {
		return nil, ErrPeer
	}
	success = true
	return p, nil
}
func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func bounded(path string, max int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrPeer
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrPeer
	}
	return b, nil
}
func mapping(b []byte, start, count uint64) bool {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		return false
	}
	f := strings.Fields(lines[0])
	if len(f) != 3 {
		return false
	}
	return f[0] == "0" && f[1] == strconv.FormatUint(start, 10) && f[2] == strconv.FormatUint(count, 10) && start > 0 && count > 0 && start <= 1<<32-1 && count <= 1<<32-1-start
}
func namespaceInit(b []byte, pid int) bool {
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			if found {
				return false
			}
			found = true
			f := strings.Fields(strings.TrimPrefix(line, "NSpid:"))
			if len(f) < 2 || f[0] != strconv.Itoa(pid) || f[len(f)-1] != "1" {
				return false
			}
		}
	}
	return found
}
func containedCgroup(b []byte) bool {
	s := string(b)
	if !strings.HasPrefix(s, "0::/") || !strings.HasSuffix(s, "\n") || strings.Count(s, "\n") != 1 {
		return false
	}
	p := strings.TrimSuffix(strings.TrimPrefix(s, "0::"), "\n")
	return p != "/" && filepath.Clean(p) == p && !strings.Contains(p, "..")
}
func hasPID(b []byte, pid int) bool {
	for _, v := range strings.Fields(string(b)) {
		if v == strconv.Itoa(pid) {
			return true
		}
	}
	return false
}

// Paused revalidates the pinned bootstrap executable, mappings, cgroup and all
// namespace identities. Alive alone means only process liveness. The pinned
// bootstrap helper must remain in its initial state until explicit provisioned
// release; no untrusted job is allowed before that handoff.
func (p *Peer) Paused() bool {
	if p == nil {
		return false
	}
	return p.Executable(p.helperSHA)
}

// Executable rechecks the held birth namespace/map/cgroup and actual executable.
// It does not attest image confinement or native API semantics.
func (p *Peer) Executable(expectedSHA string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.executableAt(expectedSHA, p.cgroup)
}
func (p *Peer) executableAt(expectedSHA, expectedCgroup string) bool {
	if !digest(expectedSHA) || !p.aliveLocked() || len(p.ids) != 6 {
		return false
	}
	root := fmt.Sprintf("/proc/%d", p.pid)
	if p.managedRoot != 0 && !p.managedMount() {
		return false
	}
	for name, ino := range p.ids {
		var st unix.Stat_t
		if unix.Stat(root+"/ns/"+name, &st) != nil || st.Ino != ino {
			return false
		}
	}
	uid, e := bounded(root+"/uid_map", 4096)
	if e != nil || !mapping(uid, uint64(p.uid), p.count) {
		return false
	}
	gid, e := bounded(root+"/gid_map", 4096)
	if e != nil || !mapping(gid, uint64(p.gid), p.count) {
		return false
	}
	cg, e := bounded(root+"/cgroup", 4096)
	if e != nil || string(cg) != expectedCgroup {
		return false
	}
	exe, e := os.Open(root + "/exe")
	if e != nil {
		return false
	}
	st, se := exe.Stat()
	h := sha256.New()
	_, he := io.Copy(h, io.LimitReader(exe, 128<<20+1))
	exe.Close()
	return se == nil && st.Mode().IsRegular() && st.Size() > 0 && st.Size() <= 128<<20 && he == nil && hex.EncodeToString(h.Sum(nil)) == expectedSHA && p.aliveLocked()
}
