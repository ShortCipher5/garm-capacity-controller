//go:build linux

package nativeegress

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func openBinary(path, digest string) (*os.File, error) {
	if !birth(digest) || securePath(path, false) != nil {
		return nil, ErrRefused
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrRefused
	}
	f := os.NewFile(uintptr(fd), path)
	bad := func() (*os.File, error) { f.Close(); return nil, ErrRefused }
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return bad()
	}
	// Reject privilege-changing capability metadata, including unreadable xattrs.
	size, e := unix.Fgetxattr(fd, "security.capability", nil)
	if e == nil && size > 0 || e != nil && e != unix.ENODATA && e != unix.ENOTSUP {
		return bad()
	}
	hash := sha256.New()
	if _, e = io.Copy(hash, f); e != nil || hex.EncodeToString(hash.Sum(nil)) != digest {
		return bad()
	}
	if _, e = f.Seek(0, 0); e != nil {
		return bad()
	}
	return f, nil
}
func securePath(path string, dir bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrRefused
	}
	current := "/"
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := -1; i < len(parts); i++ {
		if i >= 0 && parts[i] != "" {
			current = filepath.Join(current, parts[i])
		}
		s, e := os.Lstat(current)
		if e != nil || s.Mode()&os.ModeSymlink != 0 || s.Mode().Perm()&0022 != 0 {
			return ErrRefused
		}
		stat, ok := s.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return ErrRefused
		}
		if i < len(parts)-1 || dir {
			if !s.IsDir() {
				return ErrRefused
			}
		} else if !s.Mode().IsRegular() {
			return ErrRefused
		}
	}
	return nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1048576 {
		return 0, ErrUnknown
	}
	return b.Buffer.Write(p)
}
