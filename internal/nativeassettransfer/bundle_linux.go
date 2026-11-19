//go:build linux

// Package nativeassettransfer streams an explicitly approved runtime asset bundle
// into offline staging. Preparation is never activation or runtime authority.
package nativeassettransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
)

var ErrInvalid = errors.New("invalid approved runtime asset transfer")
var ErrUnknown = errors.New("runtime asset transfer outcome unknown; inspect the same approved ID, never upload again")

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Bundle struct {
	mu       sync.Mutex
	manifest nativeassets.Manifest
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

// LoadBundle opens and hashes protected deployment files without executing them.
// The independently approved helper is separate from the exact asset inventory.
// Retained FDs are closed by Close.
func LoadBundle(path, helper, expectedManifestDigest, expectedHelperDigest string) (*Bundle, error) {
	return loadBundle(path, helper, expectedManifestDigest, expectedHelperDigest, uint32(os.Geteuid()), true)
}
func loadBundle(path, helper, expectedManifestDigest, expectedHelperDigest string, uid uint32, checkAncestors bool) (*Bundle, error) {
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
		f, e := root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
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
	if json.Unmarshal(raw, &b.manifest) != nil || nativeassets.Validate(b.manifest) != nil || !bytes.Equal(raw, []byte(canonicalManifest(b.manifest))) || digest(raw) != expectedManifestDigest || !validHash(expectedHelperDigest) {
		return fail()
	}
	entries, e := os.ReadDir(path)
	if e != nil || len(entries) != len(nativeassets.Names())+1 {
		return fail()
	}
	allowed := map[string]bool{"manifest.json": true}
	for _, n := range nativeassets.Names() {
		allowed[n] = true
	}
	for _, ent := range entries {
		if !allowed[ent.Name()] {
			return fail()
		}
	}
	if checkAncestors && protected(filepath.Dir(helper), uid) != nil {
		return fail()
	}
	hr, e := os.OpenRoot(filepath.Dir(helper))
	if e != nil {
		return fail()
	}
	defer hr.Close()
	oldroot := root
	root = hr
	hf, e := open(filepath.Base(helper), 64<<20, false)
	root = oldroot
	if e != nil {
		return fail()
	}
	b.handles["capacity-native-assets"] = hf
	hs, _ := hf.Stat()
	hh := sha256.New()
	count, e := io.Copy(hh, io.NewSectionReader(hf, 0, hs.Size()))
	if e != nil || count != hs.Size() || hex.EncodeToString(hh.Sum(nil)) != expectedHelperDigest {
		return fail()
	}
	b.files = append(b.files, File{"capacity-native-assets", hs.Size(), expectedHelperDigest})
	for _, n := range nativeassets.Names() {
		entry := b.manifest.Files[n]
		f, e := open(n, entry.Size, false)
		if e != nil {
			return fail()
		}
		b.handles[n] = f
		st, _ := f.Stat()
		if st.Size() != entry.Size {
			return fail()
		}
		h := sha256.New()
		count, e := io.Copy(h, io.NewSectionReader(f, 0, entry.Size))
		if e != nil || count != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return fail()
		}
		b.files = append(b.files, File{n, entry.Size, entry.SHA256})
	}
	return b, nil
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

func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func canonicalManifest(m nativeassets.Manifest) string { b, _ := json.Marshal(m); return string(b) }
func manifestDigest(m nativeassets.Manifest) string    { d, _ := nativeassets.ManifestDigest(m); return d }
