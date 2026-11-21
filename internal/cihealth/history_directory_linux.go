package cihealth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// HistoryDirectoryReview pins an already existing directory under the
// dedicated service UID. These values are not discovered or authored here.
type HistoryDirectoryReview struct {
	Directory     string
	Device, Inode uint64
}

func (c HistoryDirectoryReview) Valid() bool {
	_, ok := historyDirectoryComponents(c.Directory)
	return ok && c.Device != 0 && c.Inode != 0
}

func historyDirectoryComponents(dir string) ([]string, bool) {
	if len(dir) > 1024 || !utf8.ValidString(dir) || strings.TrimSpace(dir) != dir || !filepath.IsAbs(dir) ||
		dir == "/" || filepath.Clean(dir) != dir || strings.ContainsFunc(dir, unicode.IsControl) {
		return nil, false
	}
	parts := strings.Split(dir[1:], "/")
	if len(parts) > 32 {
		return nil, false
	}
	for _, name := range parts {
		if len(name) == 0 || len(name) > 255 {
			return nil, false
		}
	}
	return parts, true
}

// OpenReviewedHistoryDirectory walks held directory descriptors from /, refusing symlinks and
// group/other-writable or non-deployment-owned ancestors (even sticky ones).
// The final physical identity and mode must match the independent review.
// No code can prove that root or another process with the same UID is absent:
// dedicating that UID's storage, including trustworthy rename/fsync behavior,
// is still the operator's responsibility.
func OpenReviewedHistoryDirectory(ctx context.Context, c HistoryDirectoryReview) (*os.Root, error) {
	parts, valid := historyDirectoryComponents(c.Directory)
	if !valid || c.Device == 0 || c.Inode == 0 || ctx == nil || ctx.Err() != nil {
		return nil, ErrHistory
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, ErrHistory
	}
	success := false
	defer func() {
		if !success {
			_ = root.Close()
		}
	}()
	uid := uint32(os.Geteuid())
	allowed := func(info os.FileInfo, final bool) bool {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return false
		}
		if final {
			return stat.Uid == uid && info.Mode().Perm() == 0700 && uint64(stat.Dev) == c.Device && stat.Ino == c.Inode
		}
		return (stat.Uid == 0 || stat.Uid == uid) && info.Mode().Perm()&0022 == 0
	}
	if info, err := root.Stat("."); err != nil || !allowed(info, false) {
		return nil, ErrHistory
	}
	for i, name := range parts {
		if ctx.Err() != nil {
			return nil, ErrHistory
		}
		final := i == len(parts)-1
		before, err := root.Lstat(name)
		if err != nil || !allowed(before, final) {
			return nil, ErrHistory
		}
		child, err := root.OpenRoot(name)
		if err != nil {
			return nil, ErrHistory
		}
		after, err := child.Stat(".")
		if err != nil || !allowed(after, final) || !os.SameFile(before, after) {
			_ = child.Close()
			return nil, ErrHistory
		}
		_ = root.Close()
		root = child
	}
	if ctx.Err() != nil {
		return nil, ErrHistory
	}
	success = true
	return root, nil
}
