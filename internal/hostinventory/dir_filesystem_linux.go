package hostinventory

import (
	"encoding/hex"
	"math"
	"strings"
	"time"
)

// DirFilesystemScope describes which independently reviewed Incus dir-pool
// mount a collector measured. The pool name alone is not filesystem identity.
// Mount ID and device numbers are meaningful only within this host's observed
// boot and mount namespace. These fields are not proof of the GARM route.
type DirFilesystemScope struct {
	HostFingerprint string
	StoragePool     string
	BootID          string
	MountNamespace  uint64
	MountID         uint64
	DeviceMajor     uint32
	DeviceMinor     uint32
}

// DirFilesystemCounters holds exact counter values from one descriptor statfs
// read (f_frsize, f_blocks, f_bfree, f_bavail, f_files and f_ffree respectively).
// A pointer to the complete typed read is required; do not populate these
// fields from partial JSON, display MiB, Incus usage, or guessed defaults.
type DirFilesystemCounters struct {
	FragmentBytes   int64
	TotalBlocks     uint64
	FreeBlocks      uint64
	AvailableBlocks uint64
	TotalInodes     uint64
	FreeInodes      uint64
}

type DirFilesystemSample struct {
	Scope           DirFilesystemScope
	ObservedAt      time.Time
	TotalBytes      int64
	AvailableBytes  int64
	TotalInodes     int64
	AvailableInodes int64
}

// NormalizeDirFilesystem checks a complete sample for the exact expected
// ext4-backed dir-pool mount. An external collector must bind observed scope
// and filesystem type to the same open descriptor and establish its mapping
// from the chosen Incus pool separately. This does not open paths, verify a
// remote source, prove freshness or grant any resize/management permission.
// Unknown returns a zero sample with false; true zero available is preserved.
func NormalizeDirFilesystem(expected, observed DirFilesystemScope, fsType string, at time.Time, counters *DirFilesystemCounters) (DirFilesystemSample, bool) {
	if expected != observed || !expected.Valid() || fsType != "ext4" || at.IsZero() || counters == nil {
		return DirFilesystemSample{}, false
	}
	c := *counters
	if c.FragmentBytes < 1024 || c.FragmentBytes > 65536 || c.FragmentBytes&(c.FragmentBytes-1) != 0 ||
		c.TotalBlocks == 0 || c.AvailableBlocks > c.FreeBlocks || c.FreeBlocks > c.TotalBlocks ||
		c.TotalBlocks > uint64(math.MaxInt64/c.FragmentBytes) ||
		c.TotalInodes == 0 || c.TotalInodes > math.MaxInt64 || c.FreeInodes > c.TotalInodes {
		return DirFilesystemSample{}, false
	}
	return DirFilesystemSample{
		Scope:           observed,
		ObservedAt:      at,
		TotalBytes:      int64(c.TotalBlocks) * c.FragmentBytes,
		AvailableBytes:  int64(c.AvailableBlocks) * c.FragmentBytes,
		TotalInodes:     int64(c.TotalInodes),
		AvailableInodes: int64(c.FreeInodes),
	}, true
}

// Valid checks the complete canonical scope identity, not its provenance.
func (s DirFilesystemScope) Valid() bool {
	if len(s.HostFingerprint) != 64 || len(s.BootID) != 36 || len(s.StoragePool) == 0 || len(s.StoragePool) > 63 ||
		s.MountNamespace == 0 || s.MountID == 0 || s.DeviceMajor == 0 {
		return false
	}
	if !nonzeroLowerHex(s.HostFingerprint) || s.BootID[8] != '-' || s.BootID[13] != '-' || s.BootID[18] != '-' || s.BootID[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(s.BootID, "-", "")
	if len(compact) != 32 || !nonzeroLowerHex(compact) {
		return false
	}
	for i, r := range s.StoragePool {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' && i > 0 {
			continue
		}
		return false
	}
	return true
}

// Retained descriptor sampling uses this package-local spelling; share the
// identical validation with consumers of a normalized scope.
func validDirFilesystemScope(s DirFilesystemScope) bool { return s.Valid() }

func nonzeroLowerHex(s string) bool {
	if s != strings.ToLower(s) {
		return false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return false
	}
	for _, n := range b {
		if n != 0 {
			return true
		}
	}
	return false
}
