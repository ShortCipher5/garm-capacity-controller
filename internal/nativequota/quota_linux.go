//go:build linux

// Package nativequota reads an actual local filesystem quota through a pinned
// root-owned executable. A quota observation alone is NOT an instance binding,
// allocation permission, start permission or release proof.
package nativequota

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var ErrQuota = errors.New("local enforced quota unavailable; retain charge and do not start")

type Configuration struct{ Binary, BinarySHA256, Helper, HelperSHA256 string }
type Reader struct{ config Configuration }
type Observation struct {
	dataset, guid string
	bytes         int64
	at            time.Time
}

func (o Observation) Dataset() string       { return o.dataset }
func (o Observation) GUID() string          { return o.guid }
func (o Observation) Bytes() int64          { return o.bytes }
func (o Observation) ObservedAt() time.Time { return o.at }
func (o Observation) Valid() bool {
	return validDataset(o.dataset) && o.guid != "" && o.bytes > 0 && !o.at.IsZero()
}
func New(c Configuration) (*Reader, error) {
	if os.Geteuid() != 0 {
		return nil, ErrQuota
	}
	for _, pair := range [][2]string{{c.Binary, c.BinarySHA256}, {c.Helper, c.HelperSHA256}} {
		f, e := pinned(pair[0], pair[1])
		if e != nil {
			return nil, e
		}
		f.Close()
	}
	return &Reader{c}, nil
}

// Read accepts an exact dataset already selected by the private native adapter.
// Only a filesystem's quota (including descendants/snapshots), never refquota,
// is accepted. Missing/unlimited/rounded values are not enforcement evidence.
func (r *Reader) Read(ctx context.Context, dataset string, expectedBytes int64) (Observation, error) {
	if expectedBytes <= 0 {
		return Observation{}, ErrQuota
	}
	raw, e := r.readRaw(ctx, dataset)
	if e != nil {
		return Observation{}, e
	}
	return parse(raw, dataset, expectedBytes, time.Now().UTC())
}

// FilesystemIdentity verifies identity, not an enforced positive quota. A zero
// quota here means no per-filesystem limit; callers must separately verify its
// immutable quota ancestor. It cannot be used as a StoppedQuota or allocation.
type FilesystemIdentity struct {
	dataset, guid string
	quota         int64
}

func (v FilesystemIdentity) Dataset() string { return v.dataset }
func (v FilesystemIdentity) GUID() string    { return v.guid }
func (v FilesystemIdentity) Quota() int64    { return v.quota }
func (r *Reader) ReadFilesystem(ctx context.Context, dataset string) (FilesystemIdentity, error) {
	raw, e := r.readRaw(ctx, dataset)
	if e != nil {
		return FilesystemIdentity{}, e
	}
	return parseFilesystem(raw, dataset)
}
func parseFilesystem(raw []byte, dataset string) (FilesystemIdentity, error) {
	if !validDataset(dataset) || len(raw) > 4096 {
		return FilesystemIdentity{}, ErrQuota
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		return FilesystemIdentity{}, ErrQuota
	}
	values := map[string]string{}
	for _, line := range lines {
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[0] != dataset || values[f[1]] != "" {
			return FilesystemIdentity{}, ErrQuota
		}
		values[f[1]] = f[2]
	}
	guid, e := strconv.ParseUint(values["guid"], 10, 64)
	if e != nil || guid == 0 || strconv.FormatUint(guid, 10) != values["guid"] || values["type"] != "filesystem" {
		return FilesystemIdentity{}, ErrQuota
	}
	quota, e := strconv.ParseInt(values["quota"], 10, 64)
	if e != nil || quota < 0 || strconv.FormatInt(quota, 10) != values["quota"] {
		return FilesystemIdentity{}, ErrQuota
	}
	return FilesystemIdentity{dataset, values["guid"], quota}, nil
}

func (r *Reader) readRaw(ctx context.Context, dataset string) ([]byte, error) {
	if r == nil || os.Geteuid() != 0 || !validDataset(dataset) {
		return nil, ErrQuota
	}
	binary, e := pinned(r.config.Binary, r.config.BinarySHA256)
	if e != nil {
		return nil, e
	}
	defer binary.Close()
	helper, e := pinned(r.config.Helper, r.config.HelperSHA256)
	if e != nil {
		return nil, e
	}
	defer helper.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/proc/self/fd/4", dataset)
	cmd.ExtraFiles = []*os.File{binary, helper}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	var output limitedBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || ctx.Err() != nil {
		return nil, ErrQuota
	}
	return output.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, ErrQuota
	}
	return b.Buffer.Write(p)
}
func validDataset(s string) bool {
	if len(s) == 0 || len(s) > 255 {
		return false
	}
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
		for i, c := range p {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || ((c == '_' || c == '-' || c == '.') && i > 0)) {
				return false
			}
		}
	}
	return true
}
func parse(raw []byte, dataset string, want int64, at time.Time) (Observation, error) {
	if !validDataset(dataset) || want <= 0 || at.IsZero() || len(raw) > 4096 {
		return Observation{}, ErrQuota
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		return Observation{}, ErrQuota
	}
	values := map[string]string{}
	for _, line := range lines {
		v := strings.Split(line, "\t")
		if len(v) != 3 || v[0] != dataset || values[v[1]] != "" {
			return Observation{}, ErrQuota
		}
		values[v[1]] = v[2]
	}
	guid, e := strconv.ParseUint(values["guid"], 10, 64)
	if e != nil || guid == 0 || strconv.FormatUint(guid, 10) != values["guid"] || values["type"] != "filesystem" {
		return Observation{}, ErrQuota
	}
	quota, e := strconv.ParseInt(values["quota"], 10, 64)
	if e != nil || quota != want || strconv.FormatInt(quota, 10) != values["quota"] {
		return Observation{}, ErrQuota
	}
	return Observation{dataset, values["guid"], quota, at}, nil
}
func pinned(path, digest string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(digest) != 64 {
		return nil, ErrQuota
	}
	decoded, e := hex.DecodeString(digest)
	if e != nil || hex.EncodeToString(decoded) != digest {
		return nil, ErrQuota
	}
	// Trusted-root model: no root mutation of pinned inodes while hashing/executing.
	// Every ancestor must deny replacement by non-root users.
	current := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, part)
		st, e := os.Lstat(current)
		if e != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return nil, ErrQuota
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || sys.Uid != 0 {
			return nil, ErrQuota
		}
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrQuota
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		f.Close()
		return nil, ErrQuota
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil || hex.EncodeToString(h.Sum(nil)) != digest {
		f.Close()
		return nil, ErrQuota
	}
	if _, e = f.Seek(0, 0); e != nil {
		f.Close()
		return nil, ErrQuota
	}
	return f, nil
}

// ExecHelper runs only in the dedicated child. Fixed read-only grammars cover
// quota/root accounting, typed identity inventories and pool get/free-wait.
// No shell, property mutation, arbitrary options or fallback is accepted.
func ExecHelper() error {
	inventory := len(os.Args) == 3 && ((os.Args[1] == "--identity-inventory" && validDataset(os.Args[2])) || (os.Args[1] == "--pool-identity-inventory" && validPool(os.Args[2])))
	rootMode := len(os.Args) == 3 && os.Args[1] == "--root-accounting" && validDataset(os.Args[2])
	poolMode := len(os.Args) == 3 && (os.Args[1] == "--pool-accounting" || os.Args[1] == "--pool-wait-free") && validPool(os.Args[2])
	if os.Geteuid() != 0 || (!inventory && !poolMode && !rootMode && (len(os.Args) != 2 || !validDataset(os.Args[1]))) {
		return ErrQuota
	}
	var st unix.Stat_t
	if unix.Fstat(3, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Mode&0111 == 0 {
		return ErrQuota
	}
	if unix.CloseRange(4, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) != nil {
		return ErrQuota
	}
	if rootMode {
		return syscall.Exec("/proc/self/fd/3", []string{"zfs", "get", "-Hp", "-o", "name,property,value", rootAccountingProperties, os.Args[2]}, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"})
	}
	if poolMode {
		args := []string{"zpool", "get", "-Hp", "-o", "name,property,value", poolProperties, os.Args[2]}
		if os.Args[1] == "--pool-wait-free" {
			args = []string{"zpool", "wait", "-t", "free", os.Args[2]}
		}
		return syscall.Exec("/proc/self/fd/3", args, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"})
	}
	if inventory {
		return syscall.Exec("/proc/self/fd/3", []string{"zfs", "list", "-Hpr", "-t", "all", "-o", "name,guid,type,origin,defer_destroy,userrefs", os.Args[2]}, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"})
	}
	return syscall.Exec("/proc/self/fd/3", []string{"zfs", "get", "-Hp", "-o", "name,property,value", "guid,type,quota", os.Args[1]}, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"})
}
