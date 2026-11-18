//go:build linux

package nativeboundary

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
)

type StorageObservation struct {
	root, data string
	quota      nativequota.Observation
	at         time.Time
}

func (o StorageObservation) Valid() bool {
	return o.root != "" && o.data != "" && o.quota.Valid() && !o.at.IsZero()
}
func (o StorageObservation) Root() string          { return o.root }
func (o StorageObservation) DataDirectory() string { return o.data }
func (o StorageObservation) DatasetGUID() string   { return o.quota.GUID() }
func (o StorageObservation) Bytes() int64          { return o.quota.Bytes() }

// ObserveStorage is a PRE-BIRTH check. The complete fresh native data directory,
// including image/cache/state, must live under the exact quota-controlled dataset.
// It refuses existing nested mounts; no container or shared volume may be adopted.
// This does not bound writes to host paths outside that directory: native binary,
// logging paths and system packaging still need confinement/qualification.
func ObserveStorage(ctx context.Context, root, data, dataset string, bytes int64, reader *nativequota.Reader) (StorageObservation, error) {
	if os.Geteuid() != 0 || reader == nil || bytes <= 0 || !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(data) || filepath.Clean(data) != data || !strings.HasPrefix(data, root+"/") {
		return StorageObservation{}, ErrBoundary
	}
	// Native StartFresh consumes the missing data directory itself; never adopt it.
	if _, e := os.Lstat(data); !os.IsNotExist(e) {
		return StorageObservation{}, ErrBoundary
	}
	for path := filepath.Dir(data); ; path = filepath.Dir(path) {
		st, e := os.Lstat(path)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return StorageObservation{}, ErrBoundary
		}
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != 0 {
			return StorageObservation{}, ErrBoundary
		}
		if path == "/" {
			break
		}
	}
	read := func() (string, error) {
		b, e := os.ReadFile("/proc/self/mountinfo")
		if e != nil || len(b) > 8<<20 {
			return "", ErrBoundary
		}
		return string(b), nil
	}
	before, e := read()
	if e != nil || !datasetMount(before, root, dataset) || !confinedMounts(before, root) {
		return StorageObservation{}, ErrBoundary
	}
	q, e := reader.Read(ctx, dataset, bytes)
	if e != nil {
		return StorageObservation{}, ErrBoundary
	}
	after, e := read()
	if e != nil || after != before || !datasetMount(after, root, dataset) || !confinedMounts(after, root) {
		return StorageObservation{}, ErrBoundary
	}
	return StorageObservation{root, data, q, time.Now().UTC()}, nil
}
func datasetMount(raw, root, dataset string) bool {
	found := false
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		target := f[4]
		if strings.Contains(target, "\\") {
			return false
		}
		if strings.HasPrefix(target, root+"/") {
			return false
		}
		if target != root {
			continue
		}
		if found {
			return false
		}
		found = true
		sep := -1
		for i, v := range f {
			if v == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || sep+3 >= len(f) || f[3] != "/" || f[sep+1] != "zfs" || f[sep+2] != dataset || !option(f[5], "rw") {
			return false
		}
	}
	return found
}

// Check actual read-only mount effects, not only ProtectSystem= configuration.
func confinedMounts(raw, root string) bool {
	foundRoot := false
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		p := f[4]
		if strings.Contains(p, "\\") {
			return false
		}
		if p == "/" {
			foundRoot = true
			if !option(f[5], "ro") {
				return false
			}
		}
		if p == root || strings.HasPrefix(p, root+"/") {
			continue
		}
		api := false
		for _, prefix := range []string{"/proc", "/sys", "/dev"} {
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				api = true
			}
		}
		if !api && !option(f[5], "ro") {
			return false
		}
	}
	return foundRoot
}
func (o StorageObservation) Dataset() string { return o.quota.Dataset() }

// RecheckStorage retains the original pre-birth dataset GUID. Runtime descendant
// ZFS mounts must belong to this ancestor; tmpfs is charged to the finite memory
// ancestor, and other writable filesystem mounts are refused.
func RecheckStorage(ctx context.Context, o StorageObservation, reader *nativequota.Reader) error {
	if !o.Valid() || reader == nil {
		return ErrBoundary
	}
	raw, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil || len(raw) > 8<<20 || !runtimeDatasetMount(string(raw), o.root, o.quota.Dataset()) || !confinedMounts(string(raw), o.root) {
		return ErrBoundary
	}
	q, e := reader.Read(ctx, o.quota.Dataset(), o.quota.Bytes())
	if e != nil || q.GUID() != o.quota.GUID() {
		return ErrBoundary
	}
	return nil
}
func runtimeDatasetMount(raw, root, dataset string) bool {
	found := false
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		p := f[4]
		if strings.Contains(p, "\\") {
			return false
		}
		if p != root && !strings.HasPrefix(p, root+"/") {
			continue
		}
		sep := -1
		for i, v := range f {
			if v == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || sep+3 >= len(f) {
			return false
		}
		kind, source := f[sep+1], f[sep+2]
		if p == root {
			if found || f[3] != "/" || kind != "zfs" || source != dataset || !option(f[5], "rw") {
				return false
			}
			found = true
			continue
		}
		if option(f[5], "ro") {
			continue
		}
		if kind == "zfs" && (source == dataset || strings.HasPrefix(source, dataset+"/")) {
			continue
		}
		if kind == "tmpfs" {
			continue
		}
		return false
	}
	return found
}
