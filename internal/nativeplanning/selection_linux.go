//go:build linux

// Package nativeplanning carries reviewed configuration derived from authenticated
// OS discovery. Exported selections are configuration, never physical authority.
package nativeplanning

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"net/netip"
	"regexp"
	"time"
)

const Version = "native-discovery-selection/v1"
const Lifetime = 15 * time.Minute
const MiB = int64(1 << 20)

var ErrSelection = errors.New("native configuration selection invalid, expired or mismatched; rediscover and review")
var label = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var fingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
var user = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

type Binding struct {
	PreparationID   string                 `json:"preparation_id"`
	Host            string                 `json:"host"`
	NativeTarget    nativeprovision.Target `json:"native_target"`
	InventoryTarget sshinventory.Target    `json:"inventory_target"`
}
type Selection struct {
	Version                   string                    `json:"version"`
	Binding                   Binding                   `json:"binding"`
	Observation               sshinventory.Result       `json:"observation"`
	ObservationDigest         string                    `json:"observation_digest"`
	SavedAt                   time.Time                 `json:"saved_at"`
	ExpiresAt                 time.Time                 `json:"expires_at"`
	ReserveCPUMilli           int64                     `json:"reserve_cpu_milli"`
	ReserveRAMMiB             int64                     `json:"reserve_ram_mib"`
	Limits                    nativeboundary.Limits     `json:"limits"`
	Partition                 capsulepeer.PartitionSpec `json:"partition"`
	PhysicalCapacityQualified bool                      `json:"physical_capacity_qualified"`
}

func hash(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == v
}
func Digest(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ObservationDigest(b Binding, o sshinventory.Result) string {
	return Digest(struct {
		Binding     Binding
		Observation sshinventory.Result
	}{b, o})
}
func ValidateBinding(b Binding) error {
	if !hash(b.PreparationID) || !label.MatchString(b.Host) || !label.MatchString(b.InventoryTarget.ID) || !hash(b.InventoryTarget.ReviewDigest) || !fingerprint.MatchString(b.InventoryTarget.HostKeyFingerprint) || !fingerprint.MatchString(b.NativeTarget.AdminKeyFingerprint) || !user.MatchString(b.NativeTarget.AdminUser) {
		return ErrSelection
	}
	ip, e := netip.ParseAddr(b.NativeTarget.Address)
	if e != nil || !ip.IsGlobalUnicast() || ip.String() != b.NativeTarget.Address || b.NativeTarget.Port == 0 || b.InventoryTarget.Port == 0 {
		return ErrSelection
	}
	inventoryIP, parseErr := netip.ParseAddr(b.InventoryTarget.Address)
	if parseErr != nil || inventoryIP.String() != b.InventoryTarget.Address {
		return ErrSelection
	}
	key, e := hostbootstrap.PublicKey(b.NativeTarget.HostKey)
	if e != nil || key.Type() != ssh.KeyAlgoED25519 {
		return ErrSelection
	}
	return nil
}

// ValidateSelection verifies the complete saved configuration and source binding.
// It deliberately cannot mint an opaque observation or enrollment capability.
func ValidateSelection(s Selection) error {
	o := s.Observation
	if s.Version != Version || ValidateBinding(s.Binding) != nil || s.PhysicalCapacityQualified || o.PhysicalCapacityQualified || o.Source != "authenticated_linux_os_view" || o.ObservedAt.IsZero() || o.LogicalCPUs == nil || o.RAMMiB == nil || *o.LogicalCPUs < 1 || *o.LogicalCPUs > 1000000 || *o.RAMMiB < 1 || *o.RAMMiB > 1<<40 {
		return ErrSelection
	}
	if o.AvailableRAMMiB != nil && (*o.AvailableRAMMiB < 0 || *o.AvailableRAMMiB > *o.RAMMiB) {
		return ErrSelection
	}
	if s.ObservationDigest != ObservationDigest(s.Binding, o) || !s.ExpiresAt.Equal(o.ObservedAt.Add(Lifetime)) || s.SavedAt.Before(o.ObservedAt) || s.SavedAt.After(s.ExpiresAt) {
		return ErrSelection
	}
	if s.ReserveCPUMilli < 0 || s.ReserveCPUMilli >= *o.LogicalCPUs*1000 || s.ReserveRAMMiB < 0 || s.ReserveRAMMiB >= *o.RAMMiB || s.Limits.CPUMilli < 1 || s.Limits.CPUMilli > *o.LogicalCPUs*1000-s.ReserveCPUMilli || s.Limits.MemoryBytes < MiB || s.Limits.MemoryBytes%MiB != 0 || s.Limits.MemoryBytes/MiB > *o.RAMMiB-s.ReserveRAMMiB || s.Limits.Tasks < 1 || s.Limits.Tasks > 1000000 || s.Partition.Validate(s.Limits) != nil {
		return ErrSelection
	}
	return nil
}
func ValidateFreshSelection(s Selection, now time.Time) error {
	if ValidateSelection(s) != nil || now.Before(s.Observation.ObservedAt) || now.Before(s.SavedAt) || !now.Before(s.ExpiresAt) {
		return ErrSelection
	}
	return nil
}
