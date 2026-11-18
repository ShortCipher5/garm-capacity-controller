//go:build linux

package nativezfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

var artifactDirectories = []string{"ids", "state", "zfs", "egress", "devices", "images"}

type artifactContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r artifactContextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func mountArtifact(ctx context.Context, c ArtifactConfiguration, r artifactRecord) (*artifactState, int64, error) {
	path := ArtifactMount
	if securePath(filepath.Dir(path), true) != nil || os.Mkdir(path, 0700) != nil || syncDirectory(filepath.Dir(path)) != nil {
		return nil, 0, ErrUnknown
	}
	before, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, 0, ErrUnknown
	}
	defer unix.Close(before)
	var original unix.Statx_t
	if unix.Statx(before, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID|unix.STATX_INO, &original) != nil || original.Mask&unix.STATX_MNT_ID == 0 {
		return nil, 0, ErrUnknown
	}
	raw, e := mountInfo()
	if e != nil || !noMount(raw, path) || ctx.Err() != nil {
		return nil, 0, ErrUnknown
	}
	if unix.Mount(r.Dataset, path, "zfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "") != nil {
		return nil, 0, ErrUnknown
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, 0, ErrUnknown
	}
	root := os.NewFile(uintptr(fd), path)
	ok := false
	defer func() {
		if !ok {
			root.Close()
		}
	}()
	var mounted unix.Statx_t
	var st unix.Stat_t
	if unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID|unix.STATX_INO, &mounted) != nil || mounted.Mask&unix.STATX_MNT_ID == 0 || mounted.Mnt_id == original.Mnt_id || unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Gid != 0 {
		return nil, 0, ErrUnknown
	}
	raw, e = mountInfo()
	if e != nil || !exactMount(raw, path, r.Dataset, mounted.Mnt_id) {
		return nil, 0, ErrUnknown
	}
	names, e := root.Readdirnames(1)
	if e != io.EOF || len(names) != 0 || unix.Fchmod(fd, 0700) != nil {
		return nil, 0, ErrUnknown
	}
	for _, name := range artifactDirectories {
		if unix.Mkdirat(fd, name, 0700) != nil {
			return nil, 0, ErrUnknown
		}
	}
	imagePath := filepath.Join(path, "images/runtime.raw")
	size, e := copyArtifactImage(ctx, c.Image, c.ImageSHA256, imagePath, c.QuotaBytes)
	if e != nil {
		return nil, 0, e
	}
	if syncDirectory(filepath.Join(path, "images")) != nil || root.Sync() != nil {
		return nil, 0, ErrUnknown
	}
	var image unix.Stat_t
	if unix.Lstat(imagePath, &image) != nil {
		return nil, 0, ErrUnknown
	}
	state := &artifactState{root: root, mountID: mounted.Mnt_id, image: image}
	if checkArtifactMount(state, r) != nil {
		return nil, 0, ErrUnknown
	}
	ok = true
	return state, size, nil
}
func copyArtifactImage(ctx context.Context, source, digest, target string, quota int64) (int64, error) {
	if securePath(source, false) != nil || !birthID(digest) || quota <= 0 {
		return 0, ErrRefused
	}
	fd, e := unix.Open(source, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return 0, ErrRefused
	}
	src := os.NewFile(uintptr(fd), source)
	defer src.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0022 != 0 || st.Mode&07000 != 0 || st.Size <= 0 || st.Size > 16<<30 || st.Size >= quota {
		return 0, ErrRefused
	}
	out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return 0, ErrUnknown
	}
	defer out.Close()

	n, e := copyArtifactBytes(ctx, src, out, st.Size, digest)
	if e != nil || out.Chmod(0444) != nil || out.Sync() != nil {
		return 0, ErrUnknown
	}

	return n, nil
}
func checkArtifactMount(s *artifactState, r artifactRecord) error {
	if s == nil || s.root == nil {
		return ErrRefused
	}
	path := ArtifactMount
	var held, actual unix.Statx_t
	if unix.Statx(int(s.root.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID|unix.STATX_INO, &held) != nil || unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID|unix.STATX_INO, &actual) != nil || held.Mask&unix.STATX_MNT_ID == 0 || actual.Mask&unix.STATX_MNT_ID == 0 || held.Mnt_id != s.mountID || actual.Mnt_id != s.mountID || held.Ino != actual.Ino {
		return ErrUnknown
	}
	raw, e := mountInfo()
	if e != nil || !exactMount(raw, path, r.Dataset, s.mountID) {
		return ErrUnknown
	}
	for _, name := range append([]string{""}, artifactDirectories...) {
		p := filepath.Join(path, name)
		if securePath(p, true) != nil || !artifactPathMount(p, s.mountID) {
			return ErrUnknown
		}
		st, e := os.Stat(p)
		if e != nil || st.Mode().Perm() != 0700 {
			return ErrUnknown
		}
	}
	var st unix.Stat_t
	if !artifactPathMount(filepath.Join(path, "images/runtime.raw"), s.mountID) || unix.Lstat(filepath.Join(path, "images/runtime.raw"), &st) != nil || st.Uid != 0 || st.Gid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0444 || st.Dev != s.image.Dev || st.Ino != s.image.Ino || st.Size != s.image.Size || st.Mtim != s.image.Mtim || st.Ctim != s.image.Ctim {
		return ErrUnknown
	}
	return nil
}

func copyArtifactBytes(ctx context.Context, src io.Reader, out io.Writer, size int64, digest string) (int64, error) {
	if ctx == nil || src == nil || out == nil || size <= 0 || size > 16<<30 || !birthID(digest) {
		return 0, ErrRefused
	}
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, hash), io.LimitReader(artifactContextReader{ctx, src}, size+1))
	if e != nil || n != size || hex.EncodeToString(hash.Sum(nil)) != digest || ctx.Err() != nil {
		return 0, ErrUnknown
	}
	return n, nil
}

func artifactPathMount(path string, id uint64) bool {
	var st unix.Statx_t
	return id != 0 && unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &st) == nil && st.Mask&unix.STATX_MNT_ID != 0 && st.Mnt_id == id
}
