//go:build linux

package nativeimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Configuration struct{ Source, Destination, Mkfs, MkfsSHA256, Helper, HelperSHA256 string }
type Result struct {
	ManifestSHA256, ImageSHA256, ImagePath string
	BootQualified                          bool
}
type buildSpec struct {
	Epoch, SizeMiB int64
	UUID           string
}

// Build creates only new files in Destination. It never mounts, edits an input,
// installs a package, contacts a host or enables a service. Two independent image
// builds must be byte-identical or the attempt fails and remains for diagnosis.
func Build(ctx context.Context, c Configuration, m Manifest) (Result, error) {
	if os.Geteuid() != 0 || ctx == nil || ctx.Err() != nil || m.Validate() != nil || !absolute(c.Source) || !absolute(c.Destination) || safeDir(c.Source) != nil || safeDir(filepath.Dir(c.Destination)) != nil {
		return Result{}, ErrImage
	}
	if inspect(c.Source, m) != nil {
		return Result{}, ErrImage
	}
	mkfs, e := pin(c.Mkfs, c.MkfsSHA256)
	if e != nil {
		return Result{}, e
	}
	defer mkfs.Close()
	helper, e := pin(c.Helper, c.HelperSHA256)
	if e != nil {
		return Result{}, e
	}
	defer helper.Close()
	// Permanent exclusive directory: failures are never adopted as completed builds.
	if os.Mkdir(c.Destination, 0700) != nil {
		return Result{}, ErrImage
	}
	if syncDir(filepath.Dir(c.Destination)) != nil {
		return Result{}, ErrImage
	}
	raw, _ := json.Marshal(m)
	if writeNew(filepath.Join(c.Destination, "manifest.json"), raw, 0400) != nil {
		return Result{}, ErrImage
	}
	root := filepath.Join(c.Destination, "rootfs")
	if os.Mkdir(root, 0700) != nil {
		return Result{}, ErrImage
	}
	if materialize(c.Source, root, m) != nil {
		return Result{}, ErrImage
	}
	rootFD, e := os.Open(root)
	if e != nil {
		return Result{}, ErrImage
	}
	defer rootFD.Close()
	digest := m.Digest()
	uuid := digest[:8] + "-" + digest[8:12] + "-" + digest[12:16] + "-" + digest[16:20] + "-" + digest[20:32]
	spec := buildSpec{m.Epoch, m.SizeMiB, uuid}
	sr, _ := json.Marshal(spec)
	specPath := filepath.Join(c.Destination, "mkfs.json")
	if writeNew(specPath, sr, 0400) != nil {
		return Result{}, ErrImage
	}
	specFD, e := os.Open(specPath)
	if e != nil {
		return Result{}, ErrImage
	}
	defer specFD.Close()
	hashes := []string{}
	for _, name := range []string{"runtime.raw", "reproducibility.raw"} {
		image, e := os.OpenFile(filepath.Join(c.Destination, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return Result{}, ErrImage
		}
		if image.Truncate(m.SizeMiB<<20) != nil {
			image.Close()
			return Result{}, ErrImage
		}
		specFD.Seek(0, 0)
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		cmd := exec.CommandContext(runCtx, "/proc/self/fd/4", "--mkfs-exec")
		cmd.ExtraFiles = []*os.File{mkfs, helper, image, rootFD, specFD}
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
		e = cmd.Run()
		cancel()
		if e != nil || image.Sync() != nil {
			image.Close()
			return Result{}, ErrImage
		}
		image.Seek(0, 0)
		h := sha256.New()
		_, e = io.Copy(h, image)
		image.Close()
		if e != nil {
			return Result{}, ErrImage
		}
		hashes = append(hashes, hex.EncodeToString(h.Sum(nil)))
	}
	if hashes[0] != hashes[1] {
		return Result{}, ErrImage
	}
	result := Result{digest, hashes[0], filepath.Join(c.Destination, "runtime.raw"), false}
	receipt, _ := json.Marshal(result)
	if writeNew(filepath.Join(c.Destination, "complete.json"), receipt, 0400) != nil || syncDir(c.Destination) != nil {
		return Result{}, ErrImage
	}
	return result, nil
}
func absolute(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
}
func safeDir(p string) error {
	for {
		st, e := os.Lstat(p)
		if e != nil || !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return ErrImage
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 {
			return ErrImage
		}
		if p == "/" {
			return nil
		}
		p = filepath.Dir(p)
	}
}
func pin(path, want string) (*os.File, error) {
	if !absolute(path) || !hash(want) || safeDir(filepath.Dir(path)) != nil {
		return nil, ErrImage
	}
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrImage
	}
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 || st.Size() > 128<<20 || st.Sys().(*syscall.Stat_t).Uid != 0 {
		f.Close()
		return nil, ErrImage
	}
	h := sha256.New()
	_, e = io.Copy(h, f)
	if e != nil || hex.EncodeToString(h.Sum(nil)) != want {
		f.Close()
		return nil, ErrImage
	}
	f.Seek(0, 0)
	return f, nil
}
func inspect(root string, m Manifest) error {
	mounts, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return ErrImage
	}
	for _, line := range strings.Split(string(mounts), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 {
			p := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\").Replace(f[4])
			if p == root || strings.HasPrefix(p, root+"/") {
				return ErrImage
			}
		}
	}
	seen := map[string]bool{}
	elfFiles := map[string]bool{}
	dependencies := map[string][]dependency{}
	versions := map[string]elfVersions{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return ErrImage
		}
		if p == root {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil || !relative(rel) {
			return ErrImage
		}
		st, e := os.Lstat(p)
		if e != nil {
			return ErrImage
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 {
			return ErrImage
		}
		if d.IsDir() {
			if st.Mode().Perm()&0022 != 0 {
				return ErrImage
			}
			for f := range m.Files {
				if strings.HasPrefix(f, rel+"/") {
					return nil
				}
			}
			for f := range m.Links {
				if strings.HasPrefix(f, rel+"/") {
					return nil
				}
			}
			for _, v := range emptyDirs {
				if v == rel || strings.HasPrefix(v, rel+"/") {
					return nil
				}
			}
			return ErrImage
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, e := os.Readlink(p)
			if e != nil || m.Links[rel] != target {
				return ErrImage
			}
			seen[rel] = true
			return nil
		}
		expected, ok := m.Files[rel]
		if !ok || !st.Mode().IsRegular() || st.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || st.Mode().Perm()&0022 != 0 || st.Size() > 512<<20 {
			return ErrImage
		}
		rawstat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || rawstat.Nlink != 1 {
			return ErrImage
		}
		n, e := unix.Listxattr(p, nil)
		if e != nil || n != 0 {
			return ErrImage
		}
		f, e := os.OpenFile(p, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if e != nil {
			return ErrImage
		}
		defer f.Close()
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
			return ErrImage
		}
		magic := make([]byte, 4)
		f.ReadAt(magic, 0)
		if bytes.Equal(magic, []byte{0x7f, 'E', 'L', 'F'}) {
			if e = m.CheckELF(rel, f); e != nil {
				return e
			}
			elfFiles[rel] = true
			versions[rel], e = readVersions(f)
			if e != nil {
				return e
			}
			dependencies[rel], e = m.dependencies(f)
			if e != nil {
				return e
			}
		} else {
			for _, r := range required {
				if r == rel {
					return ErrImage
				}
			}
		}
		seen[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	if !elfFiles[m.LXCLibrary] || !dependencyKinds(m, elfFiles, dependencies) || !versionsSatisfied(m, versions) {
		return ErrImage
	}
	if len(seen) != len(m.Files)+len(m.Links) {
		return ErrImage
	}
	return nil
}
func materialize(source, root string, m Manifest) error {
	epoch := time.Unix(m.Epoch, 0)
	for _, p := range m.paths() {
		target := filepath.Join(root, p)
		if os.MkdirAll(filepath.Dir(target), 0755) != nil {
			return ErrImage
		}
		src, e := os.OpenFile(filepath.Join(source, p), os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if e != nil {
			return ErrImage
		}
		dst, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			src.Close()
			return ErrImage
		}
		h := sha256.New()
		_, e = io.Copy(io.MultiWriter(dst, h), src)
		src.Close()
		se := dst.Sync()
		ce := dst.Close()
		if e != nil || se != nil || ce != nil || hex.EncodeToString(h.Sum(nil)) != m.Files[p].SHA256 || os.Chmod(target, os.FileMode(m.Files[p].Mode)) != nil || os.Chtimes(target, epoch, epoch) != nil {
			return ErrImage
		}
	}
	links := make([]string, 0, len(m.Links))
	for p := range m.Links {
		links = append(links, p)
	}
	sort.Strings(links)
	for _, p := range links {
		target := m.Links[p]
		dest := filepath.Join(root, p)
		if os.MkdirAll(filepath.Dir(dest), 0755) != nil || os.Symlink(target, dest) != nil {
			return ErrImage
		}
		ts := []unix.Timespec{unix.NsecToTimespec(epoch.UnixNano()), unix.NsecToTimespec(epoch.UnixNano())}
		if unix.UtimesNanoAt(unix.AT_FDCWD, dest, ts, unix.AT_SYMLINK_NOFOLLOW) != nil {
			return ErrImage
		}
	}
	for _, p := range emptyDirs {
		if os.MkdirAll(filepath.Join(root, p), 0755) != nil {
			return ErrImage
		}
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if os.Chmod(p, 0755) != nil || os.Chtimes(p, epoch, epoch) != nil {
				return ErrImage
			}
		}
		return nil
	})
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func writeNew(p string, b []byte, mode os.FileMode) error {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return e
	}
	n, e := f.Write(b)
	se := f.Sync()
	ce := f.Close()
	if e != nil || n != len(b) || se != nil || ce != nil {
		return ErrImage
	}
	return syncDir(filepath.Dir(p))
}

// ExecMkfs is a dedicated helper mode with only fixed descriptors. No arbitrary
// path/argv, mount, shell, network or environment is accepted.
func ExecMkfs() error {
	if len(os.Args) != 2 || os.Args[1] != "--mkfs-exec" {
		return ErrImage
	}
	for _, fd := range []int{3, 5, 7} {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
			return ErrImage
		}
	}
	var root unix.Stat_t
	if unix.Fstat(6, &root) != nil || root.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrImage
	}
	raw, e := io.ReadAll(io.LimitReader(os.NewFile(7, "spec"), 4097))
	if e != nil || len(raw) > 4096 {
		return ErrImage
	}
	var s buildSpec
	if json.Unmarshal(raw, &s) != nil {
		return ErrImage
	}
	canonical, _ := json.Marshal(s)
	if !bytes.Equal(raw, canonical) || s.Epoch < 1 || s.Epoch > 2147483647 || s.SizeMiB < 64 || s.SizeMiB > 16384 || len(s.UUID) != 36 {
		return ErrImage
	}
	uuid := strings.ReplaceAll(s.UUID, "-", "")
	if len(uuid) != 32 {
		return ErrImage
	}
	if _, e = hex.DecodeString(uuid); e != nil {
		return ErrImage
	}
	var image unix.Stat_t
	if unix.Fstat(5, &image) != nil || image.Size != s.SizeMiB<<20 {
		return ErrImage
	}
	if unix.CloseRange(7, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) != nil {
		return ErrImage
	}
	unix.CloseOnExec(4)
	epoch := strconv.FormatInt(s.Epoch, 10)
	args := []string{"mke2fs", "-q", "-F", "-t", "ext4", "-b", "4096", "-I", "256", "-N", "32768", "-m", "0", "-U", s.UUID, "-L", "garm-native", "-O", "none,extent,filetype,sparse_super,large_file,huge_file,dir_nlink,extra_isize", "-E", "root_owner=0:0,lazy_itable_init=0,hash_seed=" + s.UUID, "-d", "/proc/self/fd/6", "/proc/self/fd/5", strconv.FormatInt(s.SizeMiB*256, 10)}
	return syscall.Exec("/proc/self/fd/3", args, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "MKE2FS_CONFIG=/dev/null", "E2FSPROGS_FAKE_TIME=" + epoch, "SOURCE_DATE_EPOCH=" + epoch})
}
