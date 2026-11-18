//go:build linux

package nativedevice

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

type pinMount struct{ Namespace, Device, Directory uint64 }

// hostPinMount rejects a private-only bpffs. The same directory must be retained
// by PID1's independent mount namespace at the same fixed host path. Closing the
// owner namespace therefore cannot destroy the last bpffs reference.
func hostPinMount(root string) (pinMount, error) {
	if !strings.HasPrefix(root, "/sys/fs/bpf/") || filepath.Clean(root) != root {
		return pinMount{}, ErrDevice
	}
	host := "/proc/1/root" + root
	var a, b, n unix.Stat_t
	var af, bf unix.Statfs_t
	if unix.Stat(root, &a) != nil || unix.Stat(host, &b) != nil || unix.Stat("/proc/1/ns/mnt", &n) != nil || unix.Statfs(root, &af) != nil || unix.Statfs(host, &bf) != nil || af.Type != unix.BPF_FS_MAGIC || bf.Type != unix.BPF_FS_MAGIC || a.Dev != b.Dev || a.Ino != b.Ino || a.Uid != 0 || b.Uid != 0 || a.Mode&unix.S_IFMT != unix.S_IFDIR || a.Mode&0022 != 0 {
		return pinMount{}, ErrDevice
	}
	raw, e := os.ReadFile("/proc/1/mountinfo")
	if e != nil || !hostBPFMount(raw, root) {
		return pinMount{}, ErrDevice
	}
	return pinMount{n.Ino, uint64(a.Dev), a.Ino}, nil
}
func hostBPFMount(raw []byte, target string) bool {
	deepest := ""
	valid := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 10 {
			return false
		}
		sep := -1
		for i, v := range fields {
			if v == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || sep+3 >= len(fields) {
			return false
		}
		point := unescape(fields[4])
		if point != "/" && target != point && !strings.HasPrefix(target, point+"/") {
			continue
		}
		if len(point) > len(deepest) {
			deepest = point
			valid = fields[sep+1] == "bpf" && fields[3] == "/" && (point == "/sys/fs/bpf" || strings.HasPrefix(point, "/sys/fs/bpf/"))
		}
	}
	return valid
}
func unescape(s string) string {
	return strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\").Replace(s)
}
