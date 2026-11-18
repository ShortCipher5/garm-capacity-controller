//go:build linux

package nativezfs

import (
	"context"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// initializeRuntime acts only before zoned delegation. The fresh filesystem root
// otherwise belongs to unmapped host UID0 and is unwritable by capsule root.
// A failure leaves the exclusive placeholder and any mounted filesystem in place;
// no deferred cleanup, lazy unmount or deletion can obscure an unknown outcome.
func initializeRuntime(ctx context.Context, r Record, journal string, ns *os.File, cmd commands) error {
	if ctx.Err() != nil || r.MappedUID == 0 || r.MappedGID == 0 || !number(r.RuntimeGUID) || securePath(journal, true) != nil || strings.ContainsAny(journal, " \\\t\r\n") {
		return ErrUnknown
	}
	source := r.Dataset + "/runtime"
	path := filepath.Join(journal, "runtime-mount")
	props, e := read(ctx, cmd, source, ns)
	if e != nil || !runtimeProperties(props, r.RuntimeGUID) {
		return ErrUnknown
	}
	if os.Mkdir(path, 0700) != nil || syncDirectory(journal) != nil {
		return ErrUnknown
	}
	before, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return ErrUnknown
	}
	defer unix.Close(before)
	var original unix.Statx_t
	if unix.Statx(before, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID|unix.STATX_INO, &original) != nil || original.Mask&unix.STATX_MNT_ID == 0 {
		return ErrUnknown
	}
	raw, e := mountInfo()
	if e != nil || !noMount(raw, path) {
		return ErrUnknown
	}
	// This matches the legacy-filesystem mount(2) path in OpenZFS2.2.6 mount_zfs.c.
	if ctx.Err() != nil || unix.Mount(source, path, "zfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "") != nil {
		return ErrUnknown
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return ErrUnknown
	}
	f := os.NewFile(uintptr(fd), path)
	// Close even on error, but NEVER unmount uncertain state as cleanup.
	var mounted unix.Statx_t
	var st unix.Stat_t
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID|unix.STATX_INO, &mounted) != nil || mounted.Mask&unix.STATX_MNT_ID == 0 || mounted.Mnt_id == original.Mnt_id || unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Gid != 0 {
		f.Close()
		return ErrUnknown
	}
	raw, e = mountInfo()
	if e != nil || !exactMount(raw, path, source, mounted.Mnt_id) {
		f.Close()
		return ErrUnknown
	}
	entries, e := f.Readdirnames(1)
	if e != io.EOF || len(entries) != 0 {
		f.Close()
		return ErrUnknown
	}
	props, e = read(ctx, cmd, source, ns)
	if e != nil || !runtimeProperties(props, r.RuntimeGUID) {
		f.Close()
		return ErrUnknown
	}
	if ctx.Err() != nil || unix.Fchown(fd, int(r.MappedUID), int(r.MappedGID)) != nil || unix.Fchmod(fd, 0700) != nil || f.Sync() != nil || unix.Fstat(fd, &st) != nil || st.Uid != r.MappedUID || st.Gid != r.MappedGID || st.Mode&07777 != 0700 {
		f.Close()
		return ErrUnknown
	}
	if f.Close() != nil {
		return ErrUnknown
	}
	raw, e = mountInfo()
	if e != nil || !exactMount(raw, path, source, mounted.Mnt_id) || ctx.Err() != nil {
		return ErrUnknown
	}
	// Normal exact unmount is part of successful initialization, never a rollback.
	if unix.Unmount(path, 0) != nil {
		return ErrUnknown
	}
	raw, e = mountInfo()
	if e != nil || !noMount(raw, path) {
		return ErrUnknown
	}
	var after unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID|unix.STATX_INO, &after) != nil || after.Mnt_id != original.Mnt_id || after.Ino != original.Ino {
		return ErrUnknown
	}
	props, e = read(ctx, cmd, source, ns)
	if e != nil || !runtimeProperties(props, r.RuntimeGUID) {
		return ErrUnknown
	}
	return nil
}
func runtimeProperties(p map[string]string, guid string) bool {
	return p["guid"] == guid && p["type"] == "filesystem" && p["quota"] == "0" && p["zoned"] == "off" && p["mountpoint"] == "legacy" && p["canmount"] == "noauto"
}
func mountInfo() ([]byte, error) {
	f, e := os.Open("/proc/thread-self/mountinfo")
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if e != nil || len(b) > 1<<20 {
		return nil, ErrUnknown
	}
	return b, nil
}
func noMount(raw []byte, path string) bool { return matchMount(raw, path, "", 0, false) }
func exactMount(raw []byte, path, source string, id uint64) bool {
	return matchMount(raw, path, source, id, true)
}
func matchMount(raw []byte, path, source string, id uint64, want bool) bool {
	if len(raw) == 0 || len(raw) > 1<<20 || strings.ContainsAny(path, " \\\t\r\n") {
		return false
	}
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		parts := strings.Split(line, " - ")
		if len(parts) != 2 {
			return false
		}
		left := strings.Fields(parts[0])
		right := strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 {
			return false
		}
		mountpoint := left[4]
		if strings.HasPrefix(mountpoint, path+"/") {
			return false
		}
		if mountpoint != path {
			continue
		}
		if !want || found || left[0] != strconv.FormatUint(id, 10) || left[3] != "/" || right[0] != "zfs" || right[1] != source || !mountFlags(left[5]) {
			return false
		}
		found = true
	}
	return found == want
}

func mountFlags(v string) bool {
	m := map[string]bool{}
	for _, o := range strings.Split(v, ",") {
		m[o] = true
	}
	return m["rw"] && m["nodev"] && m["nosuid"] && m["noexec"] && !m["ro"]
}
