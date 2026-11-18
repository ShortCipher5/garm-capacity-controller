//go:build linux

package nativedevice

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Record struct {
	ID, ProgramSHA256, PinPath string
	ProgramID, LinkID          uint32
	CgroupID, NamespaceID      uint64
	ZFSMajor, ZFSMinor         uint32
}
type state struct {
	mu       sync.Mutex
	failed   bool
	record   Record
	group    *os.File
	limits   nativeboundary.Observation
	pinMount pinMount
}

// Boundary has shared state across value copies; there is no record constructor,
// detach, unpin, retry or recovery method. The pin outlives the creating process.
type Boundary struct{ state *state }

func (b Boundary) Record() Record {
	if b.state == nil {
		return Record{}
	}
	return b.state.record
}
func Install(ctx context.Context, limits nativeboundary.Observation, registry, pinRoot string) (Boundary, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || !limits.Valid() || secure(registry) != nil || secure(pinRoot) != nil {
		return Boundary{}, ErrDevice
	}
	fresh, e := nativeboundary.Observe(limits.Limits())
	if e != nil || fresh.RootInode() != limits.RootInode() || fresh.NamespaceInode() != limits.NamespaceInode() {
		return Boundary{}, ErrDevice
	}
	hostMount, e := hostPinMount(pinRoot)
	if e != nil {
		return Boundary{}, ErrDevice
	}
	var bpffs unix.Statfs_t
	if unix.Statfs(pinRoot, &bpffs) != nil || bpffs.Type != unix.BPF_FS_MAGIC {
		return Boundary{}, ErrDevice
	}
	// Install only in the empty independently managed parent before capsule birth.
	procs, e := os.ReadFile("/sys/fs/cgroup/cgroup.procs")
	if e != nil || strings.TrimSpace(string(procs)) != strconv.Itoa(os.Getpid()) {
		return Boundary{}, ErrDevice
	}
	entries, e := os.ReadDir("/sys/fs/cgroup")
	if e != nil {
		return Boundary{}, ErrDevice
	}
	for _, d := range entries {
		if d.IsDir() {
			return Boundary{}, ErrDevice
		}
	}
	cg, e := os.Open("/sys/fs/cgroup")
	if e != nil {
		return Boundary{}, ErrDevice
	}
	ok := false
	defer func() {
		if !ok {
			cg.Close()
		}
	}()
	var stat unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(int(cg.Fd()), &stat) != nil || stat.Ino != limits.RootInode() || unix.Fstatfs(int(cg.Fd()), &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return Boundary{}, ErrDevice
	}
	major, minor, e := zfsDevice()
	if e != nil {
		return Boundary{}, e
	}
	raw, e := program(major, minor)
	if e != nil {
		return Boundary{}, e
	}
	random := make([]byte, 32)
	if _, e = rand.Read(random); e != nil {
		return Boundary{}, ErrDevice
	}
	id := hex.EncodeToString(random)
	rec := Record{ID: id, ProgramSHA256: digest(raw), PinPath: filepath.Join(pinRoot, "devices-"+id), CgroupID: stat.Ino, NamespaceID: limits.NamespaceInode(), ZFSMajor: major, ZFSMinor: minor}
	dir := filepath.Join(registry, id)
	if os.Mkdir(dir, 0700) != nil || syncDir(registry) != nil {
		return Boundary{}, ErrDevice
	}
	intent, _ := json.Marshal(rec)
	if writeNew(filepath.Join(dir, "attempt.json"), intent) != nil {
		return Boundary{}, ErrDevice
	}
	prog, e := load(raw)
	if e != nil {
		return Boundary{}, ErrDevice
	}
	defer unix.Close(prog)
	pi, e := info(prog, 16)
	if e != nil || binary.LittleEndian.Uint32(pi) != unix.BPF_PROG_TYPE_CGROUP_DEVICE {
		return Boundary{}, ErrDevice
	}
	rec.ProgramID = binary.LittleEndian.Uint32(pi[4:])
	if rec.ProgramID == 0 {
		return Boundary{}, ErrDevice
	}
	lf, e := link(prog, int(cg.Fd()))
	if e != nil {
		return Boundary{}, ErrDevice
	}
	defer unix.Close(lf)
	li, e := info(lf, 32)
	if e != nil {
		return Boundary{}, ErrDevice
	}
	rec.LinkID = binary.LittleEndian.Uint32(li[4:])
	if rec.LinkID == 0 || !matchesLink(li, rec.ProgramID, rec.LinkID, rec.CgroupID) {
		return Boundary{}, ErrDevice
	}
	// The durable kernel pin is established before construction may continue.
	// Failure before pin may detach this filter, but no capsule has been launched.
	if pin(lf, rec.PinPath) != nil || os.Chmod(rec.PinPath, 0600) != nil {
		return Boundary{}, ErrDevice
	}
	b := Boundary{&state{record: rec, group: cg, limits: limits, pinMount: hostMount}}
	if b.Recheck(ctx) != nil {
		return Boundary{}, ErrDevice
	}
	complete, _ := json.Marshal(rec)
	if writeNew(filepath.Join(dir, "complete.json"), complete) != nil {
		return Boundary{}, ErrDevice
	}
	ok = true
	return b, nil
}
func (b Boundary) Recheck(ctx context.Context) error {
	if b.state == nil || ctx == nil || ctx.Err() != nil {
		return ErrDevice
	}
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return ErrDevice
	}
	e := s.recheck()
	if e != nil {
		s.failed = true
	}
	return e
}
func (s *state) recheck() error {
	rec := s.record
	actualMount, e := hostPinMount(filepath.Dir(rec.PinPath))
	if e != nil || actualMount != s.pinMount {
		return ErrDevice
	}
	fresh, e := nativeboundary.Observe(s.limits.Limits())
	if e != nil || fresh.NamespaceInode() != rec.NamespaceID || fresh.RootInode() != rec.CgroupID {
		return ErrDevice
	}
	major, minor, e := zfsDevice()
	if e != nil || major != rec.ZFSMajor || minor != rec.ZFSMinor {
		return ErrDevice
	}
	var st unix.Stat_t
	if unix.Fstat(int(s.group.Fd()), &st) != nil || st.Ino != rec.CgroupID {
		return ErrDevice
	}
	if secure(filepath.Dir(rec.PinPath)) != nil {
		return ErrDevice
	}
	var fs unix.Statfs_t
	if unix.Statfs(filepath.Dir(rec.PinPath), &fs) != nil || fs.Type != unix.BPF_FS_MAGIC {
		return ErrDevice
	}
	fd, e := get(rec.PinPath)
	if e != nil {
		return ErrDevice
	}
	defer unix.Close(fd)
	li, e := info(fd, 32)
	if e != nil || !matchesLink(li, rec.ProgramID, rec.LinkID, rec.CgroupID) || !effective(int(s.group.Fd()), rec.ProgramID) {
		return ErrDevice
	}
	return nil
}
func zfsDevice() (uint32, uint32, error) {
	st, e := os.Lstat("/dev/zfs")
	if e != nil || st.Mode()&os.ModeCharDevice == 0 || st.Mode()&os.ModeSymlink != 0 {
		return 0, 0, ErrDevice
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok || raw.Uid != 0 {
		return 0, 0, ErrDevice
	}
	major, minor := unix.Major(uint64(raw.Rdev)), unix.Minor(uint64(raw.Rdev))
	identity, e := os.ReadFile("/sys/class/misc/zfs/dev")
	if e != nil || string(identity) != fmt.Sprintf("%d:%d\n", major, minor) {
		return 0, 0, ErrDevice
	}
	return major, minor, nil
}
func secure(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\x00\n\r") {
		return ErrDevice
	}
	for {
		st, e := os.Lstat(p)
		if e != nil || !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return ErrDevice
		}
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != 0 {
			return ErrDevice
		}
		if p == "/" {
			return nil
		}
		p = filepath.Dir(p)
	}
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func writeNew(p string, b []byte) error {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	n, e := f.Write(b)
	se := f.Sync()
	ce := f.Close()
	if e != nil || n != len(b) || se != nil || ce != nil {
		return ErrDevice
	}
	return syncDir(filepath.Dir(p))
}
