//go:build linux

// Package nativeassets prepares approved runtime artifacts without installing
// system packages, executing them, or qualifying an owner boundary.
package nativeassets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const Version = "native-runtime-assets/v1"
const Parent = "/var/lib/garm-capacity/native-runtime-assets"

var ErrAssets = errors.New("runtime asset preparation unavailable; preserve partial output and inspect, never retry the same ID")
var names = []string{"runtime.raw", "systemd-nspawn", "zfs", "zpool", "ip", "nft"}

type Entry struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Manifest struct {
	Version      string           `json:"version"`
	ID           string           `json:"id"`
	Architecture string           `json:"architecture"`
	Files        map[string]Entry `json:"files"`
}
type Receipt struct {
	Version              string `json:"version"`
	ID                   string `json:"id"`
	ManifestDigest       string `json:"manifest_digest"`
	State                string `json:"state"`
	ActivationAuthorized bool   `json:"activation_authorized"`
}

func Names() []string        { return append([]string(nil), names...) }
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func hash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func Validate(m Manifest) error {
	if m.Version != Version || !hash(m.ID) || (m.Architecture != "amd64" && m.Architecture != "arm64") || len(m.Files) != len(names) {
		return ErrAssets
	}
	for _, n := range names {
		e, ok := m.Files[n]
		if !ok || !hash(e.SHA256) || e.Size < 1 {
			return ErrAssets
		}
		if n == "runtime.raw" {
			if e.Size < 64<<20 || e.Size > 16<<30 || e.Size%(1<<20) != 0 {
				return ErrAssets
			}
		} else if e.Size > 128<<20 {
			return ErrAssets
		}
	}
	return nil
}
func ManifestDigest(m Manifest) (string, error) {
	if Validate(m) != nil {
		return "", ErrAssets
	}
	b, e := json.Marshal(m)
	if e != nil {
		return "", ErrAssets
	}
	return digest(b), nil
}
func ValidateReceipt(m Manifest, r Receipt) error {
	d, e := ManifestDigest(m)
	if e != nil || r != (Receipt{Version, m.ID, d, "artifacts_prepared_not_activated", false}) {
		return ErrAssets
	}
	return nil
}
func protectedDir(p string, uid uint32) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return ErrAssets
	}
	for {
		s, e := os.Lstat(p)
		if e != nil || !s.IsDir() || s.Mode().Perm()&0022 != 0 {
			return ErrAssets
		}
		v, ok := s.Sys().(*syscall.Stat_t)
		if !ok || (v.Uid != 0 && v.Uid != uid) {
			return ErrAssets
		}
		if p == "/" {
			return nil
		}
		p = filepath.Dir(p)
	}
}
func openFile(r *os.Root, n string, uid uint32, limit int64) (*os.File, error) {
	s, e := r.Lstat(n)
	if e != nil || !s.Mode().IsRegular() {
		return nil, ErrAssets
	}
	f, e := r.OpenFile(n, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrAssets
	}
	now, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, ErrAssets
	}
	v, ok := now.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(s, now) || !now.Mode().IsRegular() || now.Mode().Perm()&0022 != 0 || now.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || v.Uid != uid || v.Nlink != 1 || now.Size() < 1 || now.Size() > limit {
		f.Close()
		return nil, ErrAssets
	}
	return f, nil
}
func readFile(r *os.Root, n string, uid uint32, limit int64) ([]byte, error) {
	f, e := openFile(r, n, uid, limit)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, ErrAssets
	}
	return b, nil
}
func decode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return ErrAssets
	}
	b, _ := json.Marshal(v)
	if !bytes.Equal(raw, b) {
		return ErrAssets
	}
	return nil
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func write(r *os.Root, n string, b []byte, mode os.FileMode) error {
	f, e := r.OpenFile(n, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	return e
}
func sourceManifest(source, want string, uid uint32) (*os.Root, Manifest, error) {
	var m Manifest
	if !hash(want) || protectedDir(source, uid) != nil {
		return nil, m, ErrAssets
	}
	r, e := os.OpenRoot(source)
	if e != nil {
		return nil, m, ErrAssets
	}
	b, e := readFile(r, "manifest.json", uid, 16384)
	if e != nil || decode(b, &m) != nil || Validate(m) != nil || digest(b) != want {
		r.Close()
		return nil, Manifest{}, ErrAssets
	}
	entries, e := os.ReadDir(source)
	if e != nil || len(entries) != len(names)+1 {
		r.Close()
		return nil, Manifest{}, ErrAssets
	}
	for _, entry := range entries {
		if entry.Name() != "manifest.json" {
			if _, ok := m.Files[entry.Name()]; !ok {
				r.Close()
				return nil, Manifest{}, ErrAssets
			}
		}
	}
	return r, m, nil
}

// Prepare consumes a fresh ID before copying. Source is a flat approved bundle;
// expectedDigest must come from explicit review, not an untrusted sibling file.
// This does not place files on PATH or alter a running service.
func Prepare(ctx context.Context, source, expectedDigest string) (Receipt, error) {
	if os.Geteuid() != 0 {
		return Receipt{}, ErrAssets
	}
	return prepare(ctx, source, Parent, expectedDigest, 0)
}
func prepare(ctx context.Context, source, parent, want string, uid uint32) (Receipt, error) {
	return prepareWithSpace(ctx, source, parent, want, uid, spaceAt)
}
func prepareWithSpace(ctx context.Context, source, parent, want string, uid uint32, checkSpace func(int, int64) error) (Receipt, error) {
	if ctx == nil || ctx.Err() != nil || protectedDir(parent, uid) != nil {
		return Receipt{}, ErrAssets
	}
	rootInfo, e := os.Lstat(parent)
	if e != nil || rootInfo.Mode().Perm() != 0700 {
		return Receipt{}, ErrAssets
	}
	src, m, e := sourceManifest(source, want, uid)
	if e != nil {
		return Receipt{}, ErrAssets
	}
	defer src.Close()
	lock, e := prepareLock(parent, uid)
	if e != nil {
		return Receipt{}, ErrAssets
	}
	defer lock.Close()
	parentFD, e := os.OpenFile(parent, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return Receipt{}, ErrAssets
	}
	defer parentFD.Close()
	var remainingAssets int64
	for _, entry := range m.Files {
		remainingAssets += entry.Size
	}
	if checkSpace(int(parentFD.Fd()), remainingAssets) != nil {
		return Receipt{}, ErrAssets
	}
	dest := filepath.Join(parent, m.ID)
	if os.Mkdir(dest, 0700) != nil {
		return Receipt{}, ErrAssets
	}
	// The fresh directory itself is the permanent consumed intent, including when
	// a later fsync or metadata write is uncertain. Never remove or adopt it.
	if os.Chmod(dest, 0700) != nil || syncDir(parent) != nil {
		return Receipt{}, ErrAssets
	}
	out, e := os.OpenRoot(dest)
	if e != nil {
		return Receipt{}, ErrAssets
	}
	defer out.Close()
	raw, _ := json.Marshal(m)
	if write(out, "intent.json", raw, 0400) != nil || syncDir(dest) != nil {
		return Receipt{}, ErrAssets
	}
	for _, n := range names {
		remainingAssets -= m.Files[n].Size
		if copyAsset(ctx, src, out, n, m, uid, remainingAssets, checkSpace) != nil {
			return Receipt{}, ErrAssets
		}
	}
	receipt := Receipt{Version, m.ID, want, "artifacts_prepared_not_activated", false}
	raw, _ = json.Marshal(receipt)
	if ctx.Err() != nil || checkSpace(int(parentFD.Fd()), 0) != nil || write(out, "complete.json", raw, 0400) != nil || syncDir(dest) != nil {
		return Receipt{}, ErrAssets
	}
	return receipt, nil
}
func copyAsset(ctx context.Context, src, out *os.Root, n string, m Manifest, uid uint32, remainingOther int64, checkSpace func(int, int64) error) error {
	e := m.Files[n]
	f, err := openFile(src, n, uid, e.Size)
	if err != nil {
		return ErrAssets
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || before.Size() != e.Size {
		return ErrAssets
	}
	if n != "runtime.raw" {
		ef, err := elf.NewFile(f)
		if err != nil {
			return ErrAssets
		}
		machine := elf.EM_X86_64
		if m.Architecture == "arm64" {
			machine = elf.EM_AARCH64
		}
		if ef.Class != elf.ELFCLASS64 || ef.Data != elf.ELFDATA2LSB || ef.Machine != machine || (ef.Type != elf.ET_EXEC && ef.Type != elf.ET_DYN) {
			return ErrAssets
		}
		if _, err = f.Seek(0, 0); err != nil {
			return ErrAssets
		}
	}
	var dst *os.File
	if out != nil {
		mode := os.FileMode(0555)
		if n == "runtime.raw" {
			mode = 0400
		}
		dst, err = out.OpenFile(n, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return ErrAssets
		}
		defer dst.Close()
		if dst.Chmod(mode) != nil {
			return ErrAssets
		}
	}
	h := sha256.New()
	w := io.Writer(h)
	if dst != nil {
		w = io.MultiWriter(dst, h)
	}
	remaining := e.Size
	buf := make([]byte, 1<<20)
	for remaining > 0 {
		if ctx.Err() != nil || (dst != nil && checkSpace(int(dst.Fd()), remaining+remainingOther) != nil) {
			return ErrAssets
		}
		take := int64(len(buf))
		if remaining < take {
			take = remaining
		}
		count, re := io.ReadFull(f, buf[:take])
		if re != nil {
			return ErrAssets
		}
		if _, err = w.Write(buf[:count]); err != nil {
			return ErrAssets
		}
		remaining -= int64(count)
	}
	after, err := f.Stat()
	if err != nil || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Sys().(*syscall.Stat_t).Ctim != after.Sys().(*syscall.Stat_t).Ctim || hex.EncodeToString(h.Sum(nil)) != e.SHA256 || ctx.Err() != nil {
		return ErrAssets
	}
	if dst != nil && dst.Sync() != nil {
		return ErrAssets
	}
	return nil
}

// Inspect verifies an existing complete preparation; it never resumes a partial
// write or treats directory presence as completion. Fixed paths are derived only
// from the exact approved ID and manifest digest.
func Inspect(ctx context.Context, id, want string) (Receipt, error) {
	if os.Geteuid() != 0 {
		return Receipt{}, ErrAssets
	}
	return inspect(ctx, Parent, id, want, 0)
}
func inspect(ctx context.Context, parent, id, want string, uid uint32) (Receipt, error) {
	if ctx == nil || ctx.Err() != nil || !hash(id) || !hash(want) || protectedDir(parent, uid) != nil {
		return Receipt{}, ErrAssets
	}
	dest := filepath.Join(parent, id)
	if protectedDir(dest, uid) != nil {
		return Receipt{}, ErrAssets
	}
	st, e := os.Stat(dest)
	if e != nil || st.Mode().Perm() != 0700 {
		return Receipt{}, ErrAssets
	}
	r, e := os.OpenRoot(dest)
	if e != nil {
		return Receipt{}, ErrAssets
	}
	defer r.Close()
	raw, e := readFile(r, "intent.json", uid, 16384)
	var m Manifest
	if e != nil || decode(raw, &m) != nil || Validate(m) != nil || m.ID != id || digest(raw) != want {
		return Receipt{}, ErrAssets
	}
	raw, e = readFile(r, "complete.json", uid, 16384)
	var receipt Receipt
	if e != nil || decode(raw, &receipt) != nil || ValidateReceipt(m, receipt) != nil {
		return Receipt{}, ErrAssets
	}
	entries, e := os.ReadDir(dest)
	if e != nil || len(entries) != len(names)+2 {
		return Receipt{}, ErrAssets
	}
	for _, n := range names {
		s, e := r.Lstat(n)
		mode := os.FileMode(0555)
		if n == "runtime.raw" {
			mode = 0400
		}
		if e != nil || s.Mode().Perm() != mode || copyAsset(ctx, r, nil, n, m, uid, 0, nil) != nil {
			return Receipt{}, ErrAssets
		}
	}
	return receipt, nil
}
