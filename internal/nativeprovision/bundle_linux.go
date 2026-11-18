//go:build linux

// Package nativeprovision transports an explicitly approved, deployment-loaded
// native-owner bundle. Preparation is never activation or runtime authority.
package nativeprovision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
)

var ErrInvalid = errors.New("invalid approved native preparation bundle")
var ErrUnknown = errors.New("native preparation outcome unknown; inspect the same approved preparation ID, never upload again")

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Bundle struct {
	mu       sync.Mutex
	manifest nativeprepare.Manifest
	files    []File
	handles  map[string]*os.File
	closed   bool
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func protected(path string, uid uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalid
	}
	for p := path; ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil || !s.IsDir() || s.Mode().Perm()&0022 != 0 {
			return ErrInvalid
		}
		st, ok := s.Sys().(*syscall.Stat_t)
		if !ok || (st.Uid != uid && st.Uid != 0) {
			return ErrInvalid
		}
		if p == "/" {
			return nil
		}
	}
}

// LoadBundle opens only protected deployment files. Private key/config bytes are
// never returned to a browser, receipt or log; retained FDs are closed by Close.
func LoadBundle(path string) (*Bundle, error) { return loadBundle(path, uint32(os.Geteuid()), true) }
func loadBundle(path string, uid uint32, checkAncestors bool) (*Bundle, error) {
	if checkAncestors && protected(path, uid) != nil {
		return nil, ErrInvalid
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return nil, ErrInvalid
	}
	defer root.Close()
	b := &Bundle{handles: map[string]*os.File{}}
	fail := func() (*Bundle, error) { b.Close(); return nil, ErrInvalid }
	open := func(name string, limit int64, private bool) (*os.File, error) {
		s, e := root.Lstat(name)
		if e != nil || !s.Mode().IsRegular() || s.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, ErrInvalid
		}
		f, e := root.Open(name)
		if e != nil {
			return nil, ErrInvalid
		}
		s2, e := f.Stat()
		if e != nil || !os.SameFile(s, s2) {
			f.Close()
			return nil, ErrInvalid
		}
		st, ok := s2.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uid || st.Nlink != 1 || s2.Size() < 1 || s2.Size() > limit || s2.Mode().Perm()&0022 != 0 || (private && s2.Mode().Perm() != 0600) {
			f.Close()
			return nil, ErrInvalid
		}
		return f, nil
	}
	mf, e := open("manifest.json", 16384, false)
	if e != nil {
		return fail()
	}
	raw, e := io.ReadAll(io.LimitReader(mf, 16385))
	mf.Close()
	if e != nil || len(raw) > 16384 {
		return fail()
	}
	if json.Unmarshal(raw, &b.manifest) != nil || nativeprepare.Validate(b.manifest) != nil {
		return fail()
	}
	entries, e := os.ReadDir(path)
	if e != nil || len(entries) != len(nativeprepare.FileNames())+1 {
		return fail()
	}
	allowed := map[string]bool{"manifest.json": true}
	for _, n := range nativeprepare.FileNames() {
		allowed[n] = true
	}
	for _, ent := range entries {
		if !allowed[ent.Name()] {
			return fail()
		}
	}
	execs := map[string]bool{}
	for _, n := range nativeprepare.ExecutableNames() {
		execs[n] = true
	}
	names := nativeprepare.FileNames()
	sort.Strings(names)
	// The pinned preparation helper is first so inspect recovery never executes an unverified path.
	names = append([]string{"capacity-native-prepare"}, remove(names, "capacity-native-prepare")...)
	var total int64
	for _, n := range names {
		limit := int64(1 << 20)
		if execs[n] {
			limit = 64 << 20
		}
		f, e := open(n, limit, !execs[n])
		if e != nil {
			return fail()
		}
		b.handles[n] = f
		s, _ := f.Stat()
		total += s.Size()
		if total > 512<<20 {
			return fail()
		}
		h := sha256.New()
		count, e := io.Copy(h, io.NewSectionReader(f, 0, s.Size()))
		if e != nil || count != s.Size() || hex.EncodeToString(h.Sum(nil)) != b.manifest.Files[n] {
			return fail()
		}
		b.files = append(b.files, File{n, s.Size(), b.manifest.Files[n]})
	}
	return b, nil
}
func remove(v []string, n string) []string {
	out := []string{}
	for _, s := range v {
		if s != n {
			out = append(out, s)
		}
	}
	return out
}
func (b *Bundle) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, f := range b.handles {
		f.Close()
	}
	return nil
}
