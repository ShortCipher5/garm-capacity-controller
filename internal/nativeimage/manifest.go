// Package nativeimage prepares a deterministic, fully inventoried native runtime
// filesystem. An image build is not qualification of its Incus/kernel behavior.
package nativeimage

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedns"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativelxc"
	"io"
	"path"
	"sort"
	"strings"
)

var ErrImage = errors.New("native runtime image input or reproducibility check refused")

const Version = "native-runtime-image/v3"

type File struct {
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}
type Manifest struct {
	DNSResolvers []string          `json:"dns_resolvers"`
	Version      string            `json:"version"`
	Epoch        int64             `json:"epoch"`
	SizeMiB      int64             `json:"size_mib"`
	Architecture string            `json:"architecture"`
	IncusVersion string            `json:"incus_version"`
	LXCPackage   string            `json:"lxc_package,omitempty"`
	LXCVersion   string            `json:"lxc_version"`
	LXCLibrary   string            `json:"lxc_library"`
	ZFSVersion   string            `json:"zfs_version"`
	Files        map[string]File   `json:"files"`
	Links        map[string]string `json:"links"`
}

var required = []string{"usr/lib/garm-capacity/native-stage", "usr/lib/garm-capacity/quota-read", "usr/libexec/incus/incusd", "usr/sbin/zfs"}
var emptyDirs = []string{"dev", "proc", "sys", "run", "tmp", "var/lib/garm-capacity-native"}

func hash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func relative(s string) bool {
	return s != "" && s != "." && !strings.HasPrefix(s, "/") && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\x00\n\r\\")
}
func (m Manifest) Validate() error {
	resolver, e := nativedns.Content(m.DNSResolvers)
	if e != nil {
		return ErrImage
	}
	sum := sha256.Sum256(resolver)
	entry, ok := m.Files["etc/resolv.conf"]
	if !ok || entry.Mode != 0444 || entry.SHA256 != hex.EncodeToString(sum[:]) {
		return ErrImage
	}

	if m.Version != Version || m.Epoch < 1 || m.Epoch > 2147483647 || m.SizeMiB < 64 || m.SizeMiB > 16384 || (m.Architecture != "amd64" && m.Architecture != "arm64") || m.IncusVersion != "6.0.4" || m.ZFSVersion != "2.2.6" || len(m.Files) < len(required) || len(m.Files) > 20000 || len(m.Links) > 20000 {
		return ErrImage
	}
	if !m.validLXC() {
		return ErrImage
	}
	if _, ok := m.Files[m.LXCLibrary]; !ok {
		return ErrImage
	}
	for p, f := range m.Files {
		if !relative(p) || !hash(f.SHA256) || (f.Mode != 0444 && f.Mode != 0555) {
			return ErrImage
		}
		if _, ok := m.Links[p]; ok {
			return ErrImage
		}
		if reserved(p) {
			return ErrImage
		}
	}
	for _, p := range required {
		f, ok := m.Files[p]
		if !ok || f.Mode != 0555 {
			return ErrImage
		}
	}
	for p, target := range m.Links {
		if !relative(p) || reserved(p) || target == "" || strings.ContainsAny(target, "\x00\n\r\\") {
			return ErrImage
		}
		if _, e := m.resolve(p); e != nil {
			return e
		}
	}
	for p := range m.Files {
		if !m.parents(p) {
			return ErrImage
		}
	}
	for p := range m.Links {
		if !m.parents(p) {
			return ErrImage
		}
	}
	return nil
}
func reserved(p string) bool {
	for _, d := range emptyDirs {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return p == "etc/fstab" || p == "etc/machine-id"
}
func (m Manifest) parents(p string) bool {
	for d := path.Dir(p); d != "."; d = path.Dir(d) {
		if _, ok := m.Files[d]; ok {
			return false
		}
		if _, ok := m.Links[d]; ok {
			return false
		}
	}
	return true
}
func (m Manifest) resolve(p string) (string, error) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		if seen[p] {
			return "", ErrImage
		}
		seen[p] = true
		if _, ok := m.Files[p]; ok {
			return p, nil
		}
		t, ok := m.Links[p]
		if !ok {
			return "", ErrImage
		}
		if strings.HasPrefix(t, "/") {
			p = strings.TrimPrefix(path.Clean(t), "/")
		} else {
			p = path.Clean(path.Join(path.Dir(p), t))
		}
		if !relative(p) {
			return "", ErrImage
		}
	}
	return "", ErrImage
}
func (m Manifest) Digest() string {
	raw, _ := json.Marshal(m)
	v := sha256.Sum256(raw)
	return hex.EncodeToString(v[:])
}
func (m Manifest) paths() []string {
	out := make([]string, 0, len(m.Files))
	for p := range m.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// CheckELF checks architecture and every declared direct DT_NEEDED/interpreter
// against inventoried paths. It does not invent missing plugins or certify ABI.
func (m Manifest) CheckELF(p string, reader io.ReaderAt) error {
	e, err := elf.NewFile(reader)
	if err != nil {
		return ErrImage
	}
	defer e.Close()
	want := elf.EM_X86_64
	if m.Architecture == "arm64" {
		want = elf.EM_AARCH64
	}
	if e.Machine != want || e.Class != elf.ELFCLASS64 || e.Data != elf.ELFDATA2LSB {
		return ErrImage
	}
	for _, prog := range e.Progs {
		if prog.Type == elf.PT_INTERP {
			raw, err := io.ReadAll(io.LimitReader(prog.Open(), 4097))
			if err != nil || len(raw) > 4096 || len(raw) < 2 || raw[len(raw)-1] != 0 || raw[0] != '/' {
				return ErrImage
			}
			interp := strings.TrimPrefix(string(raw[:len(raw)-1]), "/")
			if _, err = m.resolve(interp); err != nil {
				return fmt.Errorf("%w: interpreter for %s", ErrImage, p)
			}
		}
	}
	for _, tag := range []elf.DynTag{elf.DT_RPATH, elf.DT_RUNPATH} {
		v, err := e.DynString(tag)
		if err != nil {
			return ErrImage
		}
		if len(v) > 0 {
			return fmt.Errorf("%w: dynamic search path", ErrImage)
		}
	}
	needed, err := e.ImportedLibraries()
	if err != nil {
		return ErrImage
	}
	for _, lib := range needed {
		if lib == "" || strings.Contains(lib, "/") {
			return ErrImage
		}
		found := false
		for candidate := range m.Files {
			if libraryCandidate(candidate, lib, m.Architecture) {
				found = true
			}
		}
		for candidate := range m.Links {
			if libraryCandidate(candidate, lib, m.Architecture) {
				if _, e := m.resolve(candidate); e == nil {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("%w: missing library %s for %s", ErrImage, lib, p)
		}
	}
	return nil
}
func libraryCandidate(p, base, arch string) bool {
	triplet := "x86_64-linux-gnu"
	if arch == "arm64" {
		triplet = "aarch64-linux-gnu"
	}
	for _, dir := range []string{"lib", "lib64", "usr/lib", "usr/lib64", "lib/" + triplet, "usr/lib/" + triplet} {
		if p == dir+"/"+base {
			return true
		}
	}
	return false
}

// dependencyKinds is checked after the complete real-byte inventory has been
// inspected, so an inventoried text file cannot stand in for a dynamic loader.
type dependency struct {
	path        string
	interpreter bool
}

func (m Manifest) dependencies(reader io.ReaderAt) ([]dependency, error) {
	f, e := elf.NewFile(reader)
	if e != nil {
		return nil, ErrImage
	}
	defer f.Close()
	var out []dependency
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			raw, e := io.ReadAll(io.LimitReader(p.Open(), 4097))
			if e != nil || len(raw) < 2 || len(raw) > 4096 || raw[0] != '/' || raw[len(raw)-1] != 0 {
				return nil, ErrImage
			}
			resolved, e := m.resolve(string(raw[1 : len(raw)-1]))
			if e != nil {
				return nil, e
			}
			out = append(out, dependency{resolved, true})
		}
	}
	needed, e := f.ImportedLibraries()
	if e != nil {
		return nil, ErrImage
	}
	for _, lib := range needed {
		found := false
		for p := range m.Files {
			if libraryCandidate(p, lib, m.Architecture) {
				out = append(out, dependency{p, false})
				found = true
			}
		}
		for p := range m.Links {
			if libraryCandidate(p, lib, m.Architecture) {
				r, e := m.resolve(p)
				if e != nil {
					return nil, e
				}
				out = append(out, dependency{r, false})
				found = true
			}
		}
		if !found {
			return nil, ErrImage
		}
	}
	return out, nil
}
func dependencyKinds(m Manifest, actual map[string]bool, refs map[string][]dependency) bool {
	for _, deps := range refs {
		for _, d := range deps {
			if !actual[d.path] || d.interpreter && m.Files[d.path].Mode != 0555 {
				return false
			}
		}
	}
	return true
}

// validLXC binds the distro profile to independently verified vendor bytes,
// never an arbitrary checksum supplied with a package/version label.
func (m Manifest) validLXC() bool {
	if m.LXCVersion == "7.0.1" {
		return m.LXCPackage == "" && libraryCandidate(m.LXCLibrary, "liblxc.so.1", m.Architecture)
	}
	if m.LXCVersion != nativelxc.DebianVersion || m.LXCPackage != nativelxc.DebianPackage || m.Architecture != nativelxc.DebianArchitecture || m.LXCLibrary != nativelxc.DebianLibrary {
		return false
	}
	f, ok := m.Files[m.LXCLibrary]
	if !ok || f.SHA256 != nativelxc.DebianLibrarySHA256 || f.Mode != 0444 {
		return false
	}
	p, e := m.resolve("usr/lib/x86_64-linux-gnu/liblxc.so.1")
	return e == nil && p == m.LXCLibrary
}
