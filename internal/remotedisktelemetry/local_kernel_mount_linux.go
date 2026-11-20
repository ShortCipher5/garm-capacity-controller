package remotedisktelemetry

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

const (
	localBootIDPath     = "/proc/sys/kernel/random/boot_id"
	localMountNSPath    = "/proc/thread-self/ns/mnt"
	localMountInfoPath  = "/proc/thread-self/mountinfo"
	localMountInfoLimit = 1 << 20
)

func readLocalProc(path string) ([]byte, bool) {
	var limit int64
	switch path {
	case localBootIDPath:
		limit = 37
	case localMountInfoPath:
		limit = localMountInfoLimit
	default:
		return nil, false
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	file := os.NewFile(uintptr(fd), "kernel-proc-observation")
	defer file.Close()
	var fs unix.Statfs_t
	var st unix.Stat_t
	if unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.PROC_SUPER_MAGIC ||
		unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	return raw, err == nil && len(raw) != 0 && int64(len(raw)) <= limit
}

type localMountNamespace struct{ device, inode uint64 }

func openLocalMountNS() (*os.File, localMountNamespace, bool) {
	// Intentionally follow this fixed kernel namespace magic link; its result
	// must actually be an NSFS namespace handle of mount type.
	fd, err := unix.Open(localMountNSPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, localMountNamespace{}, false
	}
	file := os.NewFile(uintptr(fd), "kernel-current-mount-namespace")
	var fs unix.Statfs_t
	var st unix.Stat_t
	if unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.NSFS_MAGIC ||
		unix.Fstat(fd, &st) != nil || st.Ino == 0 {
		file.Close()
		return nil, localMountNamespace{}, false
	}
	typ, err := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if err != nil || typ != unix.CLONE_NEWNS {
		file.Close()
		return nil, localMountNamespace{}, false
	}
	return file, localMountNamespace{device: uint64(st.Dev), inode: st.Ino}, true
}

type localHeldMount struct{ ordinary, unique uint64 }

func readLocalHeldMount(fd int, review localReview) (localHeldMount, bool) {
	const flags = unix.AT_EMPTY_PATH | unix.AT_SYMLINK_NOFOLLOW | unix.AT_STATX_DONT_SYNC
	const common = unix.STATX_TYPE | unix.STATX_INO
	var ordinary, unique unix.Statx_t
	// The kernel reports one stx_mnt_id meaning per request. The ordinary
	// mountinfo ID and the unique ID are different notions, never compared
	// numerically to each other.
	if unix.Statx(fd, "", flags, common|unix.STATX_MNT_ID, &ordinary) != nil ||
		unix.Statx(fd, "", flags, common|unix.STATX_MNT_ID_UNIQUE, &unique) != nil {
		return localHeldMount{}, false
	}
	return localHeldMountFacts(review, ordinary, unique)
}

func localHeldMountFacts(review localReview, ordinary, unique unix.Statx_t) (localHeldMount, bool) {
	const common = unix.STATX_TYPE | unix.STATX_INO
	if ordinary.Mask&(common|unix.STATX_MNT_ID|unix.STATX_MNT_ID_UNIQUE) != common|unix.STATX_MNT_ID ||
		unique.Mask&(common|unix.STATX_MNT_ID|unix.STATX_MNT_ID_UNIQUE) != common|unix.STATX_MNT_ID_UNIQUE ||
		ordinary.Mnt_id == 0 || unique.Mnt_id == 0 || unique.Mnt_id != review.scope.MountID {
		return localHeldMount{}, false
	}
	for _, found := range [...]unix.Statx_t{ordinary, unique} {
		if found.Mode&unix.S_IFMT != unix.S_IFDIR || found.Ino != review.inode ||
			found.Dev_major != review.scope.DeviceMajor || found.Dev_minor != review.scope.DeviceMinor {
			return localHeldMount{}, false
		}
	}
	return localHeldMount{ordinary: ordinary.Mnt_id, unique: unique.Mnt_id}, true
}

func localMountInfoCrosswalk(raw []byte, ordinary uint64, review localReview) bool {
	if len(raw) == 0 || len(raw) > localMountInfoLimit || raw[len(raw)-1] != '\n' || ordinary == 0 {
		return false
	}
	id := strconv.FormatUint(ordinary, 10)
	device := strconv.FormatUint(uint64(review.scope.DeviceMajor), 10) + ":" + strconv.FormatUint(uint64(review.scope.DeviceMinor), 10)
	seen := false
	for line := range bytes.SplitSeq(raw[:len(raw)-1], []byte("\n")) {
		fields := bytes.Fields(line)
		if len(fields) < 10 {
			return false
		}
		if string(fields[0]) != id {
			// IDs must be exact decimal; ambiguous spellings cannot shadow a
			// matching entry. Other mount rows do not supply any proof.
			n, err := strconv.ParseUint(string(fields[0]), 10, 64)
			if err != nil || n == 0 || string(fields[0]) != strconv.FormatUint(n, 10) {
				return false
			}
			continue
		}
		if seen || string(fields[2]) != device {
			return false
		}
		separator := -1
		for i := 6; i < len(fields); i++ {
			if bytes.Equal(fields[i], []byte("-")) {
				separator = i
				break
			}
		}
		if separator < 0 || len(fields) != separator+4 || string(fields[separator+1]) != "ext4" {
			return false
		}
		seen = true
	}
	return seen
}

func currentLocalDirectoryMount(ctx context.Context, dir *os.File, review localReview) bool {
	if ctx == nil || dir == nil || !review.scope.Valid() || review.inode == 0 || !localLeaseCurrent(ctx) {
		return false
	}
	control, err := dir.SyscallConn()
	if err != nil {
		return false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	beforeNS, namespace, ok := openLocalMountNS()
	if !ok {
		return false
	}
	defer beforeNS.Close() // pin the actual namespace lifetime across the crosswalk
	if namespace.inode != review.scope.MountNamespace {
		return false
	}
	boot, ok := readLocalProc(localBootIDPath)
	if !ok || string(boot) != review.scope.BootID+"\n" {
		return false
	}
	matched := false
	err = control.Control(func(fd uintptr) {
		before, ok := readLocalHeldMount(int(fd), review)
		if !ok || !localLeaseCurrent(ctx) {
			return
		}
		info, ok := readLocalProc(localMountInfoPath)
		if !ok || !localMountInfoCrosswalk(info, before.ordinary, review) {
			return
		}
		after, ok := readLocalHeldMount(int(fd), review)
		if !ok || before != after || !localLeaseCurrent(ctx) {
			return
		}
		info, ok = readLocalProc(localMountInfoPath)
		matched = ok && localMountInfoCrosswalk(info, after.ordinary, review) && localLeaseCurrent(ctx)
	})
	if err != nil || !matched || !localLeaseCurrent(ctx) {
		return false
	}
	afterNS, finalNS, ok := openLocalMountNS()
	if !ok {
		return false
	}
	defer afterNS.Close()
	finalBoot, ok := readLocalProc(localBootIDPath)
	return finalNS == namespace && ok && bytes.Equal(finalBoot, boot) && localLeaseCurrent(ctx)
}
