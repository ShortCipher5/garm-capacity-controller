//go:build linux

package nativeassets

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const diskHeadroom int64 = 1 << 30
const metadataAllowance int64 = 1 << 20
const inodeHeadroom uint64 = 64

// enoughSpace is an observation, not a reservation against unrelated writers.
// Bavail deliberately excludes root-reserved blocks. Unknown accounting fails
// closed; metadata allowance covers bounded files and allocation rounding.
func enoughSpace(s unix.Statfs_t, remaining int64) error {
	if remaining < 0 || remaining > 17<<30 || s.Frsize <= 0 || s.Frsize > 65536 || s.Bavail > s.Blocks || s.Ffree < inodeHeadroom+16 || s.Flags&unix.ST_RDONLY != 0 {
		return ErrAssets
	}
	required := remaining + diskHeadroom + metadataAllowance
	blocks := uint64((required + s.Frsize - 1) / s.Frsize)
	if s.Bavail < blocks {
		return ErrAssets
	}
	return nil
}
func spaceAt(fd int, remaining int64) error {
	var s unix.Statfs_t
	if unix.Fstatfs(fd, &s) != nil {
		return ErrAssets
	}
	return enoughSpace(s, remaining)
}

// Serialize cooperating helper writers across IDs. Upload has its own lock
// covering reception as well. Neither lock excludes arbitrary host processes.
func prepareLock(parent string, uid uint32) (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(parent, "staging.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if e != nil {
		return nil, ErrAssets
	}
	fail := func() (*os.File, error) { f.Close(); return nil, ErrAssets }
	st, e := f.Stat()
	if e != nil || st.Mode() != 0600 {
		return fail()
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || meta.Uid != uid || meta.Nlink != 1 {
		return fail()
	}
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil || syncDir(parent) != nil {
		return fail()
	}
	return f, nil
}
