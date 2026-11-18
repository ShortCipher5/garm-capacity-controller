//go:build linux

package nativecapsule

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativelxc"
	"golang.org/x/sys/unix"
)

// LXCVersion accepts only the inventoried vendor library actually mapped by the
// retained live daemon. A version string, package name or configured checksum
// cannot mint this observation. It is not ABI or confinement qualification.
func (r *Running) LXCVersion(ctx context.Context) (string, error) {
	if r == nil || r.CheckScope(ctx) != nil {
		return "", ErrCapsule
	}
	pid := r.staged.peer.PID()
	root, e := os.Open(fmt.Sprintf("/proc/%d/root", pid))
	if e != nil {
		return "", ErrCapsule
	}
	defer root.Close()
	fd, e := unix.Openat2(int(root.Fd()), nativelxc.DebianLibrary, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if e != nil {
		return "", ErrCapsule
	}
	f := os.NewFile(uintptr(fd), "retained-native-library")
	defer f.Close()
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &st) != nil || unix.Fstatfs(fd, &fs) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0222 != 0 || st.Size != nativelxc.DebianLibrarySize || fs.Flags&unix.ST_RDONLY == 0 {
		return "", ErrCapsule
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, nativelxc.DebianLibrarySize+1))
	if e != nil || n != nativelxc.DebianLibrarySize || fmt.Sprintf("%x", h.Sum(nil)) != nativelxc.DebianLibrarySHA256 {
		return "", ErrCapsule
	}
	maps, e := os.Open(fmt.Sprintf("/proc/%d/maps", pid))
	if e != nil {
		return "", ErrCapsule
	}
	defer maps.Close()
	raw, e := io.ReadAll(io.LimitReader(maps, 4<<20+1))
	if e != nil || len(raw) > 4<<20 || !mappedLibrary(string(raw), uint64(st.Dev), st.Ino) || r.CheckScope(ctx) != nil {
		return "", ErrCapsule
	}
	return nativelxc.DebianVersion, nil
}

func mappedLibrary(raw string, dev, ino uint64) bool {
	found := false
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !strings.Contains(fields[5], "liblxc.so") {
			continue
		}
		if len(fields) != 6 || fields[5] != "/"+nativelxc.DebianLibrary {
			return false
		}
		inode, e := strconv.ParseUint(fields[4], 10, 64)
		if e != nil || inode != ino {
			return false
		}
		pair := strings.Split(fields[3], ":")
		if len(pair) != 2 {
			return false
		}
		major, e := strconv.ParseUint(pair[0], 16, 32)
		if e != nil {
			return false
		}
		minor, e := strconv.ParseUint(pair[1], 16, 32)
		if e != nil || major != uint64(unix.Major(dev)) || minor != uint64(unix.Minor(dev)) {
			return false
		}
		if len(fields[1]) != 4 || fields[1][3] != 'p' {
			return false
		}
		if fields[1][2] == 'x' {
			found = true
		}
	}
	return found
}
