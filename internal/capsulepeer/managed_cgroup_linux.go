//go:build linux

package capsulepeer

import (
	"fmt"
	"golang.org/x/sys/unix"
	"strings"
)

// managedMount checks the actual bounded owner-private hierarchy, never the host
// hierarchy. Its root inode and inherited namespace were independently observed
// before the clone; only exact fresh capsule children receive mapped write access.
func (p *Peer) managedMount() bool {
	path := fmt.Sprintf("/proc/%d/root/sys/fs/cgroup", p.pid)
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Stat(path, &st) != nil || unix.Statfs(path, &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC || st.Ino != p.managedRoot || st.Uid != 0 || st.Gid != 0 || st.Mode&0022 != 0 {
		return false
	}
	for _, name := range []string{"memory.max", "memory.swap.max", "cpu.max", "pids.max", "cgroup.procs", "cgroup.threads", "cgroup.subtree_control"} {
		var f unix.Stat_t
		if unix.Lstat(path+"/"+name, &f) != nil || f.Mode&unix.S_IFMT != unix.S_IFREG || f.Uid != 0 || f.Gid != 0 || f.Mode&0022 != 0 {
			return false
		}
	}
	raw, e := bounded(fmt.Sprintf("/proc/%d/mountinfo", p.pid), 1<<20)
	return e == nil && managedCgroupMount(string(raw))
}
func managedCgroupMount(raw string) bool {
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
		if sep < 6 || sep+3 >= len(f) || f[3] != "/" || f[sep+1] != "cgroup2" || !cgOption(f[5], "rw") || !cgOption(f[sep+3], "nsdelegate") {
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
func cgOption(raw, want string) bool {
	for _, v := range strings.Split(raw, ",") {
		if v == want {
			return true
		}
	}
	return false
}

// CompleteNativePlacement only acknowledges the fixed self-placement performed
// by the pinned PID1 stage. It never writes a numeric PID or accepts a caller
// cgroup path, and only succeeds after the same process executes the pinned ELF.
func (p *Peer) CompleteNativePlacement(nativeSHA string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.managedRoot == 0 {
		return false
	}
	target := "0::/capsule-" + p.birth + "/init.scope\n"
	old := "0::/capsule-" + p.birth + "/bootstrap/payload\n"
	if p.cgroup != old && p.cgroup != target {
		return false
	}
	if !p.executableAt(nativeSHA, target) {
		return false
	}
	p.cgroup = target
	return true
}
