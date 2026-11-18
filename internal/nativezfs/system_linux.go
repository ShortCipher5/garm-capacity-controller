//go:build linux

package nativezfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The installed executable and its libraries are part of the trusted host.
// Every ancestor must reject non-root replacement; the opened inode is pinned
// by hash across command invocations. No shell, PATH lookup or caller argv.
type systemCommands struct{ binary, helper *os.File }

func openBinary(path, digest string) (*os.File, error) {
	if !birthID(digest) || securePath(path, false) != nil {
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
	if b.Len()+len(p) > 8192 {
		return 0, ErrUnknown
	}
	return b.Buffer.Write(p)
}
func (c systemCommands) run(ctx context.Context, args []string, namespace *os.File) ([]byte, error) {
	if c.binary == nil || c.helper == nil || namespace == nil || !validCommand(args) {
		return nil, ErrRefused
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/proc/self/fd/5", args...)
	command.ExtraFiles = []*os.File{c.binary, namespace, c.helper}
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	command.Dir = "/"
	command.WaitDelay = 2 * time.Second
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if command.Run() != nil || ctx.Err() != nil {
		return nil, ErrUnknown
	}
	return output.Bytes(), nil
}

type fileJournal struct{ directory, id string }

func (j fileJournal) consume(r Record) error {
	if !birthID(j.id) || j.id != r.CapsuleID {
		return ErrRefused
	}
	// mkdir is the persistent no-retry latch, even if writing/fsync later fails.
	if os.Mkdir(j.directory, 0700) != nil {
		return ErrUnknown
	}
	if syncDirectory(filepath.Dir(j.directory)) != nil {
		return ErrUnknown
	}
	return j.write("attempt.json", r)
}
func (j fileJournal) complete(r Record) error { return j.write("complete.json", r) }
func (j fileJournal) write(name string, r Record) error {
	f, e := os.OpenFile(filepath.Join(j.directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrUnknown
	}
	_, e = f.Write(recordBytes(r))
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil || ce != nil {
		return ErrUnknown
	}
	return syncDirectory(j.directory)
}
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
