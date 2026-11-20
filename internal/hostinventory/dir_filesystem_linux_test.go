package hostinventory

import (
	"math"
	"strings"
	"testing"
	"time"
)

func dirFilesystemFixture() (DirFilesystemScope, DirFilesystemCounters, time.Time) {
	return DirFilesystemScope{
			HostFingerprint: strings.Repeat("1", 64), StoragePool: "fixture-pool", BootID: "11111111-1111-4111-8111-111111111111",
			MountNamespace: 12, MountID: 17, DeviceMajor: 8, DeviceMinor: 0,
		}, DirFilesystemCounters{
			FragmentBytes: 4096, TotalBlocks: 10000, FreeBlocks: 4400, AvailableBlocks: 3300, TotalInodes: 5000, FreeInodes: 2200,
		}, time.Unix(100, 0).UTC()
}

func TestNormalizeDirFilesystemUsesAvailableNotAllFree(t *testing.T) {
	s, c, at := dirFilesystemFixture()
	got, ok := NormalizeDirFilesystem(s, s, "ext4", at, &c)
	want := DirFilesystemSample{Scope: s, ObservedAt: at, TotalBytes: 40960000, AvailableBytes: 13516800, TotalInodes: 5000, AvailableInodes: 2200}
	if !ok || got != want {
		t.Fatalf("using reserved bytes or losing sample scope: %+v, %v", got, ok)
	}
	c.AvailableBlocks, c.FreeInodes = 0, 0
	got, ok = NormalizeDirFilesystem(s, s, "ext4", at, &c)
	if !ok || got.TotalBytes != want.TotalBytes || got.TotalInodes != want.TotalInodes || got.AvailableBytes != 0 || got.AvailableInodes != 0 {
		t.Fatalf("genuine zero availability became unknown: %+v, %v", got, ok)
	}
}

func TestNormalizeDirFilesystemRejectsOtherScopeAndUnknown(t *testing.T) {
	s, c, at := dirFilesystemFixture()
	for _, change := range []func(*DirFilesystemScope){
		func(v *DirFilesystemScope) { v.HostFingerprint = strings.Repeat("2", 64) },
		func(v *DirFilesystemScope) { v.StoragePool = "another" },
		func(v *DirFilesystemScope) { v.BootID = "22222222-2222-4222-8222-222222222222" },
		func(v *DirFilesystemScope) { v.MountNamespace++ },
		func(v *DirFilesystemScope) { v.MountID++ },
		func(v *DirFilesystemScope) { v.DeviceMajor++ },
		func(v *DirFilesystemScope) { v.DeviceMinor++ },
	} {
		v := s
		change(&v)
		if got, ok := NormalizeDirFilesystem(s, v, "ext4", at, &c); ok || got != (DirFilesystemSample{}) {
			t.Fatalf("accepted different filesystem scope: %+v", v)
		}
	}
	for _, incomplete := range []DirFilesystemScope{
		{}, {HostFingerprint: strings.Repeat("1", 64), StoragePool: "fixture-pool", BootID: "11111111-1111-4111-8111-111111111111"},
	} {
		if _, ok := NormalizeDirFilesystem(incomplete, incomplete, "ext4", at, &c); ok {
			t.Fatal("matching empty scope accepted")
		}
	}
	bad := s
	bad.StoragePool = "../default"
	if _, ok := NormalizeDirFilesystem(bad, bad, "ext4", at, &c); ok {
		t.Fatal("path accepted as storage pool")
	}
	for _, fsType := range []string{"", "btrfs", "ext3", "overlay", "EXT4"} {
		if _, ok := NormalizeDirFilesystem(s, s, fsType, at, &c); ok {
			t.Fatalf("unreviewed filesystem accepted: %s", fsType)
		}
	}
	if _, ok := NormalizeDirFilesystem(s, s, "ext4", at, nil); ok {
		t.Fatal("missing counters interpreted as known")
	}
	if _, ok := NormalizeDirFilesystem(s, s, "ext4", time.Time{}, &c); ok {
		t.Fatal("missing time interpreted as known")
	}
}

func TestNormalizeDirFilesystemRejectsImpossibleAndOverflowCounters(t *testing.T) {
	for _, change := range []func(*DirFilesystemCounters){
		func(c *DirFilesystemCounters) { c.FragmentBytes = 0 },
		func(c *DirFilesystemCounters) { c.FragmentBytes = 4095 },
		func(c *DirFilesystemCounters) { c.TotalBlocks = 0 },
		func(c *DirFilesystemCounters) { c.TotalBlocks = math.MaxInt64 },
		func(c *DirFilesystemCounters) { c.FreeBlocks = c.TotalBlocks + 1 },
		func(c *DirFilesystemCounters) { c.AvailableBlocks = c.FreeBlocks + 1 },
		func(c *DirFilesystemCounters) { c.TotalInodes = 0; c.FreeInodes = 0 },
		func(c *DirFilesystemCounters) { c.FreeInodes = c.TotalInodes + 1 },
		func(c *DirFilesystemCounters) { c.TotalInodes = math.MaxUint64 },
	} {
		s, c, at := dirFilesystemFixture()
		change(&c)
		if got, ok := NormalizeDirFilesystem(s, s, "ext4", at, &c); ok || got != (DirFilesystemSample{}) {
			t.Fatalf("impossible counters escaped: %+v", c)
		}
	}
}
