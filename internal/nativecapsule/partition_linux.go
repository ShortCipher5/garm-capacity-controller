//go:build linux

package nativecapsule

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"golang.org/x/sys/unix"
)

type birthPartition struct {
	mu     sync.Mutex
	root   int
	groups map[string]int
	inodes map[string]uint64
	spec   capsulepeer.PartitionSpec
	failed bool
	closed bool
}

func cgOpen(parent int, name string, flags int) (int, error) {
	return unix.Openat(parent, name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}
func cgRead(parent int, name string) (string, error) {
	fd, e := cgOpen(parent, name, unix.O_RDONLY)
	if e != nil {
		return "", e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(b) > 4096 {
		return "", ErrCapsule
	}
	return strings.TrimSpace(string(b)), nil
}
func cgWrite(parent int, name, value string) error {
	fd, e := cgOpen(parent, name, unix.O_WRONLY)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	n, e := unix.Write(fd, []byte(value))
	if e != nil || n != len(value) {
		return ErrCapsule
	}
	return nil
}
func partitionFiles(v nativeboundary.Limits) map[string]string {
	return map[string]string{"memory.max": strconv.FormatInt(v.MemoryBytes, 10), "memory.swap.max": "0", "memory.oom.group": "0", "cpu.max": fmt.Sprintf("%d 100000", v.CPUMilli*100), "pids.max": strconv.FormatInt(v.Tasks, 10)}
}
func partitionLimits(fd int, v nativeboundary.Limits) error {
	for name, want := range partitionFiles(v) {
		var st unix.Stat_t
		got, e := cgRead(fd, name)
		if e != nil || got != want || unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Uid != 0 || st.Gid != 0 || st.Mode&0022 != 0 {
			return ErrCapsule
		}
	}
	return nil
}
func partitionInode(fd int) (uint64, error) {
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &st) != nil || unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return 0, ErrCapsule
	}
	return st.Ino, nil
}

// preparePartition runs before the first native process exists. There is no
// PID-based migration and hence no PID-reuse window or migration of preexisting
// daemon memory charges. Failures retain created groups and the consumed intent.
func preparePartition(directory, birth string, spec capsulepeer.PartitionSpec, parent nativeboundary.Observation) (*birthPartition, error) {
	if !hash(birth) || !parent.Valid() || spec.Validate(parent.Limits()) != nil || os.Geteuid() != 0 {
		return nil, ErrCapsule
	}
	now, e := nativeboundary.Observe(parent.Limits())
	if e != nil || now.RootInode() != parent.RootInode() || now.NamespaceInode() != parent.NamespaceInode() {
		return nil, ErrCapsule
	}
	raw, _ := json.Marshal(spec)
	f, e := os.OpenFile(filepath.Join(directory, "partition-intent.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, ErrCapsule
	}
	n, e := f.Write(raw)
	se := f.Sync()
	ce := f.Close()
	if e != nil || n != len(raw) || se != nil || ce != nil || syncDir(directory) != nil {
		return nil, ErrCapsule
	}
	path := "/sys/fs/cgroup/capsule-" + birth
	if os.Mkdir(path, 0755) != nil {
		return nil, ErrCapsule
	}
	root, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrCapsule
	}
	p := &birthPartition{root: root, groups: map[string]int{}, inodes: map[string]uint64{}, spec: spec}
	success := false
	defer func() {
		if !success {
			p.close()
		}
	}()
	ino, e := partitionInode(root)
	if e != nil {
		return nil, ErrCapsule
	}
	p.inodes[""] = ino
	procs, e := cgRead(root, "cgroup.procs")
	if e != nil || procs != "" || cgWrite(root, "cgroup.subtree_control", "+cpu +memory +pids") != nil {
		return nil, ErrCapsule
	}
	for name, v := range map[string]nativeboundary.Limits{"bootstrap": spec.Bootstrap, "init.scope": spec.Daemon, "monitors": spec.Monitors, "guests": spec.Guests} {
		if unix.Mkdirat(root, name, 0755) != nil {
			return nil, ErrCapsule
		}
		fd, e := cgOpen(root, name, unix.O_RDONLY|unix.O_DIRECTORY)
		if e != nil {
			return nil, ErrCapsule
		}
		p.groups[name] = fd
		ino, e := partitionInode(fd)
		if e != nil {
			return nil, ErrCapsule
		}
		p.inodes[name] = ino
		for file, value := range partitionFiles(v) {
			if cgWrite(fd, file, value) != nil {
				return nil, ErrCapsule
			}
		}
		if partitionLimits(fd, v) != nil {
			return nil, ErrCapsule
		}
	}
	success = true
	return p, nil
}
func (p *birthPartition) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.failed = true
	for _, fd := range p.groups {
		unix.Close(fd)
	}
	unix.Close(p.root)
}
func (p *birthPartition) recheck(ctx context.Context) error {
	if p == nil || ctx == nil || ctx.Err() != nil {
		return ErrCapsule
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed {
		return ErrCapsule
	}
	fail := func() error { p.failed = true; return ErrCapsule }
	ino, e := partitionInode(p.root)
	if e != nil || ino != p.inodes[""] {
		return fail()
	}
	for name, v := range map[string]nativeboundary.Limits{"bootstrap": p.spec.Bootstrap, "init.scope": p.spec.Daemon, "monitors": p.spec.Monitors, "guests": p.spec.Guests} {
		fd := p.groups[name]
		ino, e := partitionInode(fd)
		var st unix.Stat_t
		if e != nil || ino != p.inodes[name] || unix.Fstatat(p.root, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Ino != ino || partitionLimits(fd, v) != nil {
			return fail()
		}
	}
	if ctx.Err() != nil {
		return fail()
	}
	return nil
}

// No aggregate max file is delegated. Ancestor and sibling writes are denied by
// host-root ownership outside the mapped user namespace; the actual root mount
// remains limited to the independently managed finite owner domain.
func (p *birthPartition) delegate(uid, gid uint32) error {
	if p == nil || uid == 0 || gid == 0 {
		return ErrCapsule
	}
	for _, name := range []string{"monitors", "guests"} {
		fd := p.groups[name]
		if unix.Fchown(fd, int(uid), int(gid)) != nil {
			return ErrCapsule
		}
		for _, file := range []string{"cgroup.procs", "cgroup.threads", "cgroup.subtree_control"} {
			if unix.Fchownat(fd, file, int(uid), int(gid), unix.AT_SYMLINK_NOFOLLOW) != nil {
				return ErrCapsule
			}
		}
	}
	for _, fd := range []int{p.root, p.groups["init.scope"]} {
		for _, file := range []string{"cgroup.procs", "cgroup.threads"} {
			if unix.Fchownat(fd, file, int(uid), int(gid), unix.AT_SYMLINK_NOFOLLOW) != nil {
				return ErrCapsule
			}
		}
	}
	return nil
}
