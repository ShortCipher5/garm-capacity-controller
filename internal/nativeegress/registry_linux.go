//go:build linux

package nativeegress

import (
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// The one root-owned registry must be shared by all network births on this host.
// A failed/partial journal poisons new allocation rather than guessing free space.
func reserveNetwork(registry string, r Record) error {
	f, e := os.OpenFile(filepath.Join(registry, "lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return ErrUnknown
	}
	defer f.Close()
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return ErrRefused
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	entries, e := os.ReadDir(registry)
	if e != nil || len(entries) > 10000 {
		return ErrRefused
	}
	p, e := prefix(r.TransitCIDR)
	if e != nil {
		return ErrRefused
	}
	for _, entry := range entries {
		if entry.Name() == "lock" {
			continue
		}
		if !entry.IsDir() || !birth(entry.Name()) {
			return ErrUnknown
		}
		raw, e := os.ReadFile(filepath.Join(registry, entry.Name(), "attempt.json"))
		if e != nil || len(raw) > 8192 {
			return ErrUnknown
		}
		var prior Record
		if json.Unmarshal(raw, &prior) != nil || prior.BirthID != entry.Name() {
			return ErrUnknown
		}
		old, e := prefix(prior.TransitCIDR)
		if e != nil || overlaps(old, p) {
			return ErrRefused
		}
	}
	dir := filepath.Join(registry, r.BirthID)
	if os.Mkdir(dir, 0700) != nil || syncDir(registry) != nil || writeRecord(dir, "attempt.json", r) != nil {
		return ErrUnknown
	}
	return nil
}
