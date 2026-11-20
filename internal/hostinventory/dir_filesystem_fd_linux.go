package hostinventory

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// DirFilesystemFDBinding is supplied independently by the reviewed collector,
// from its actual boot/namespace and known Incus pool mapping. In particular,
// the exact ext4 type must come from reviewed mount metadata, not a statfs
// magic number shared with other ext filesystems. This type itself proves
// neither provenance nor that a descriptor came from that scope.
type DirFilesystemFDBinding struct {
	HostFingerprint string
	StoragePool     string
	BootID          string
	MountNamespace  uint64
	FilesystemType  string
}

const dirFilesystemStatxMask = unix.STATX_TYPE | unix.STATX_INO | unix.STATX_MNT_ID_UNIQUE

func (b DirFilesystemFDBinding) matches(expected DirFilesystemScope) bool {
	return b.HostFingerprint == expected.HostFingerprint && b.StoragePool == expected.StoragePool &&
		b.BootID == expected.BootID && b.MountNamespace == expected.MountNamespace && b.FilesystemType == "ext4"
}

func dirFilesystemStatxMatches(st unix.Statx_t, expected DirFilesystemScope) bool {
	return st.Mask&dirFilesystemStatxMask == dirFilesystemStatxMask && st.Mode&unix.S_IFMT == unix.S_IFDIR &&
		st.Ino != 0 && st.Mnt_id == expected.MountID && st.Dev_major == expected.DeviceMajor && st.Dev_minor == expected.DeviceMinor
}

func dirFilesystemFromDescriptorStats(expected DirFilesystemScope, binding DirFilesystemFDBinding,
	before, after unix.Statx_t, fs unix.Statfs_t, at time.Time) (DirFilesystemSample, bool) {
	if !binding.matches(expected) || !dirFilesystemStatxMatches(before, expected) ||
		!dirFilesystemStatxMatches(after, expected) || before.Ino != after.Ino || fs.Type != unix.EXT4_SUPER_MAGIC {
		return DirFilesystemSample{}, false
	}
	observed := DirFilesystemScope{
		HostFingerprint: binding.HostFingerprint, StoragePool: binding.StoragePool,
		BootID: binding.BootID, MountNamespace: binding.MountNamespace,
		MountID: after.Mnt_id, DeviceMajor: after.Dev_major, DeviceMinor: after.Dev_minor,
	}
	return NormalizeDirFilesystem(expected, observed, binding.FilesystemType, at, &DirFilesystemCounters{
		FragmentBytes: int64(fs.Frsize), TotalBlocks: fs.Blocks, FreeBlocks: fs.Bfree,
		AvailableBlocks: fs.Bavail, TotalInodes: fs.Files, FreeInodes: fs.Ffree,
	})
}

func dirFilesystemContextCurrent(ctx context.Context, now time.Time) bool {
	if ctx == nil || ctx.Err() != nil || now.IsZero() {
		return false
	}
	deadline, bounded := ctx.Deadline()
	return !bounded || now.Before(deadline)
}

// Shared read sequence. The real caller holds the same descriptor throughout;
// these private functions let focused tests verify elapsed time without I/O.
func readDirFilesystemFD(ctx context.Context, expected DirFilesystemScope, binding DirFilesystemFDBinding,
	statx func(*unix.Statx_t) error, statfs func(*unix.Statfs_t) error, now func() time.Time) (DirFilesystemSample, bool) {
	var before, after unix.Statx_t
	var fs unix.Statfs_t
	if !dirFilesystemContextCurrent(ctx, now()) || statx(&before) != nil || !dirFilesystemStatxMatches(before, expected) {
		return DirFilesystemSample{}, false
	}
	// Stamp before requesting counters: slow syscall return or a descheduled
	// reader cannot make older counters appear newly sampled.
	at := now()
	if !dirFilesystemContextCurrent(ctx, at) || statfs(&fs) != nil || fs.Type != unix.EXT4_SUPER_MAGIC ||
		ctx.Err() != nil || statx(&after) != nil || !dirFilesystemContextCurrent(ctx, now()) {
		return DirFilesystemSample{}, false
	}
	return dirFilesystemFromDescriptorStats(expected, binding, before, after, fs, at)
}

// SampleDirFilesystemFD reads only an already independently authorized and
// retained local directory descriptor. It neither opens a path nor verifies
// the caller's boot/namespace binding, its connection to Incus, or the remote
// route. Linux kernels without unique mount IDs fail closed. Context checks
// reject canceled results; they cannot interrupt an in-flight kernel call.
// The caller retains ownership of the descriptor and must keep its separately
// reviewed scope current for this sample.
func SampleDirFilesystemFD(ctx context.Context, dir *os.File, expected DirFilesystemScope, binding DirFilesystemFDBinding) (DirFilesystemSample, bool) {
	if !dirFilesystemContextCurrent(ctx, time.Now()) || dir == nil || *dir == (os.File{}) ||
		!validDirFilesystemScope(expected) || !binding.matches(expected) {
		return DirFilesystemSample{}, false
	}
	control, err := dir.SyscallConn()
	if err != nil {
		return DirFilesystemSample{}, false
	}
	var out DirFilesystemSample
	var ok bool
	err = control.Control(func(fd uintptr) {
		const flags = unix.AT_EMPTY_PATH | unix.AT_SYMLINK_NOFOLLOW | unix.AT_STATX_DONT_SYNC
		// Control retains this exact kernel descriptor through all three
		// reads, even if the file is concurrently being closed.
		out, ok = readDirFilesystemFD(ctx, expected, binding, func(st *unix.Statx_t) error {
			return unix.Statx(int(fd), "", flags, dirFilesystemStatxMask, st)
		}, func(st *unix.Statfs_t) error {
			return unix.Fstatfs(int(fd), st)
		}, time.Now)
	})
	if err != nil || !ok || !dirFilesystemContextCurrent(ctx, time.Now()) {
		return DirFilesystemSample{}, false
	}
	return out, true
}
