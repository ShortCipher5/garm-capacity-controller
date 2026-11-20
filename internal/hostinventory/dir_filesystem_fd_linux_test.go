package hostinventory

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testDirFilesystemBinding(s DirFilesystemScope, inode uint64, expiry time.Time) DirFilesystemFDBinding {
	return DirFilesystemFDBinding{
		HostFingerprint: s.HostFingerprint, StoragePool: s.StoragePool, BootID: s.BootID,
		MountNamespace: s.MountNamespace, FilesystemType: "ext4", DirectoryInode: inode, ReviewExpiresAt: expiry,
	}
}

func TestDirFilesystemFDUsesSameScopeAndConservativeCounters(t *testing.T) {
	scope, counters, at := dirFilesystemFixture()
	binding := testDirFilesystemBinding(scope, 294, at.Add(time.Minute))
	stat := unix.Statx_t{
		Mask: dirFilesystemStatxMask, Mode: unix.S_IFDIR | 0700,
		Ino: 294, Mnt_id: scope.MountID, Dev_major: scope.DeviceMajor, Dev_minor: scope.DeviceMinor,
	}
	fs := unix.Statfs_t{
		Type: unix.EXT4_SUPER_MAGIC, Frsize: 4096, Blocks: counters.TotalBlocks,
		Bfree: counters.FreeBlocks, Bavail: counters.AvailableBlocks, Files: counters.TotalInodes, Ffree: counters.FreeInodes,
	}
	want, ok := NormalizeDirFilesystem(scope, scope, "ext4", at, &counters)
	got, sampled := dirFilesystemFromDescriptorStats(scope, binding, stat, stat, fs, at)
	if !ok || !sampled || got != want || got.AvailableBytes >= int64(fs.Bfree)*int64(fs.Frsize) {
		t.Fatalf("bound availability sample not preserved: %+v %v", got, sampled)
	}
	for name, change := range map[string]func(*unix.Statx_t){
		"legacy mount id": func(s *unix.Statx_t) { s.Mask = (s.Mask &^ unix.STATX_MNT_ID_UNIQUE) | unix.STATX_MNT_ID },
		"mount changed":   func(s *unix.Statx_t) { s.Mnt_id++ },
		"device changed":  func(s *unix.Statx_t) { s.Dev_minor++ },
		"inode changed":   func(s *unix.Statx_t) { s.Ino++ },
		"not a directory": func(s *unix.Statx_t) { s.Mode = unix.S_IFREG },
	} {
		t.Run(name, func(t *testing.T) {
			after := stat
			change(&after)
			if got, ok := dirFilesystemFromDescriptorStats(scope, binding, stat, after, fs, at); ok || got != (DirFilesystemSample{}) {
				t.Fatal("descriptor drift accepted")
			}
		})
	}
	for _, change := range []func(*DirFilesystemFDBinding){
		func(b *DirFilesystemFDBinding) { b.BootID = "bad" },
		func(b *DirFilesystemFDBinding) { b.MountNamespace++ },
		func(b *DirFilesystemFDBinding) { b.StoragePool = "unreviewed" },
		func(b *DirFilesystemFDBinding) { b.FilesystemType = "ext3" }, // shared kernel magic is not proof of ext4
	} {
		drift := binding
		change(&drift)
		if got, ok := dirFilesystemFromDescriptorStats(scope, drift, stat, stat, fs, at); ok || got != (DirFilesystemSample{}) {
			t.Fatal("unmatched binding accepted")
		}
	}
	fs.Type = unix.TMPFS_MAGIC
	if got, ok := dirFilesystemFromDescriptorStats(scope, binding, stat, stat, fs, at); ok || got != (DirFilesystemSample{}) {
		t.Fatal("non-ext magic accepted")
	}
}

func TestDirFilesystemFDRefusesAbsentCanceledAndClosedHandle(t *testing.T) {
	scope, _, _ := dirFilesystemFixture()
	binding := testDirFilesystemBinding(scope, 294, time.Now().Add(time.Minute))
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, ok := SampleDirFilesystemFD(ctx, dir, scope, binding); ok || got != (BoundDirFilesystemSample{}) {
		t.Fatal("canceled observation accepted")
	}
	if got, ok := SampleDirFilesystemFD(t.Context(), nil, scope, binding); ok || got != (BoundDirFilesystemSample{}) {
		t.Fatal("absent descriptor accepted")
	}
	if got, ok := SampleDirFilesystemFD(t.Context(), new(os.File), scope, binding); ok || got != (BoundDirFilesystemSample{}) {
		t.Fatal("uninitialized descriptor accepted")
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	if got, ok := SampleDirFilesystemFD(t.Context(), dir, scope, binding); ok || got != (BoundDirFilesystemSample{}) {
		t.Fatal("closed descriptor accepted")
	}
}

func TestDirFilesystemFDTimestampPrecedesCountersAndDeadlineIsStrict(t *testing.T) {
	scope, _, _ := dirFilesystemFixture()
	base := time.Now()
	binding := testDirFilesystemBinding(scope, 12, base.Add(2*time.Minute))
	deadline := base.Add(time.Minute)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()

	for _, tc := range []struct {
		name          string
		postStatxAt   time.Time
		wantAvailable bool
	}{
		{"delayed success", deadline.Add(-time.Nanosecond), true},
		{"exact deadline", deadline, false},
		{"past deadline", deadline.Add(time.Second), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock, observedBeforeRead := base, base.Add(time.Second)
			reads := 0
			sample, ok := readDirFilesystemFD(ctx, scope, binding, func(st *unix.Statx_t) error {
				*st = unix.Statx_t{Mask: dirFilesystemStatxMask, Mode: unix.S_IFDIR, Ino: 12, Mnt_id: scope.MountID, Dev_major: scope.DeviceMajor, Dev_minor: scope.DeviceMinor}
				reads++
				if reads == 1 {
					clock = observedBeforeRead
				} else {
					clock = tc.postStatxAt
				}
				return nil
			}, func(fs *unix.Statfs_t) error {
				*fs = unix.Statfs_t{Type: unix.EXT4_SUPER_MAGIC, Frsize: 4096, Blocks: 10000, Bfree: 4400, Bavail: 3300, Files: 5000, Ffree: 2200}
				clock = base.Add(40 * time.Second) // delayed resumption, before post-Statx
				return nil
			}, func() time.Time { return clock })
			if ctx.Err() != nil || reads != 2 || ok != tc.wantAvailable {
				t.Fatal("deadline guard did not check elapsed post-read wall time independently of context timer")
			}
			if ok && !sample.ObservedAt.Equal(observedBeforeRead) {
				t.Fatal("sample time advanced during the counters syscall")
			}
			if !ok && sample != (DirFilesystemSample{}) {
				t.Fatal("expired read returned counters")
			}
		})
	}
}

func TestReviewedDirFilesystemFDPinsDirectoryAndReviewAcrossRead(t *testing.T) {
	scope, _, _ := dirFilesystemFixture()
	base := time.Now()
	expires := base.Add(time.Minute)
	ctx, cancel := context.WithDeadline(t.Context(), base.Add(time.Hour))
	defer cancel()
	const pinnedInode = 42
	validStat := unix.Statx_t{
		Mask: dirFilesystemStatxMask, Mode: unix.S_IFDIR, Ino: pinnedInode,
		Mnt_id: scope.MountID, Dev_major: scope.DeviceMajor, Dev_minor: scope.DeviceMinor,
	}
	for _, tc := range []struct {
		name                 string
		reviewedInode        uint64
		expiry               time.Time
		before, after        unix.Statx_t
		beforeAt, countersAt time.Time
		afterAt              time.Time
		statx, counters      int
		ok                   bool
	}{
		{"still reviewed", pinnedInode, expires, validStat, validStat, base, base.Add(time.Second), expires.Add(-1), 2, 1, true},
		{"wrong inode both reads on the right mount", pinnedInode + 1, expires, validStat, validStat, base, base, base, 1, 0, false},
		{"absent inode review", 0, expires, validStat, validStat, base, base, base, 1, 0, false},
		{"absent expiry", pinnedInode, time.Time{}, validStat, validStat, base, base, base, 0, 0, false},
		{"expires before counters", pinnedInode, expires, validStat, validStat, expires, expires, expires, 1, 0, false},
		{"expires during counters", pinnedInode, expires, validStat, validStat, base, expires, expires, 1, 1, false},
		{"expires at final identity check", pinnedInode, expires, validStat, validStat, base, base.Add(time.Second), expires, 2, 1, false},
		{"second statx changes directory", pinnedInode, expires, validStat, func() unix.Statx_t { s := validStat; s.Ino++; return s }(), base, base, base, 2, 1, false},
		{"second statx remounts same device", pinnedInode, expires, validStat, func() unix.Statx_t { s := validStat; s.Mnt_id++; return s }(), base, base, base, 2, 1, false},
		{"second statx changes device", pinnedInode, expires, validStat, func() unix.Statx_t { s := validStat; s.Dev_minor++; return s }(), base, base, base, 2, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statx, counters, clock := 0, 0, base
			binding := testDirFilesystemBinding(scope, tc.reviewedInode, tc.expiry)
			sample, ok := readDirFilesystemFD(ctx, scope, binding, func(s *unix.Statx_t) error {
				statx++
				if statx == 1 {
					*s, clock = tc.before, tc.beforeAt
				} else {
					*s, clock = tc.after, tc.afterAt
				}
				return nil
			}, func(s *unix.Statfs_t) error {
				counters++
				*s = unix.Statfs_t{Type: unix.EXT4_SUPER_MAGIC, Frsize: 4096, Blocks: 10, Bfree: 5, Bavail: 4, Files: 6, Ffree: 3}
				clock = tc.countersAt
				return nil
			}, func() time.Time { return clock })
			if ctx.Err() != nil || ok != tc.ok || statx != tc.statx || counters != tc.counters {
				t.Fatalf("unexpected observation: %+v (%v), statx=%d, counters=%d", sample, ok, statx, counters)
			}
			if ok && (!sample.ObservedAt.Equal(base) || sample.Scope != scope || sample.AvailableBytes != 4*4096 || sample.AvailableInodes != 3) {
				t.Fatal("valid descriptor read lost its measured shape or earlier observation time")
			}
			if !ok && sample != (DirFilesystemSample{}) {
				t.Fatal("a refused sample retained counters")
			}
		})
	}
}

func TestZeroBoundDirFilesystemSampleHasNoEvidence(t *testing.T) {
	sample, inode, expires, ok := (BoundDirFilesystemSample{}).Evidence()
	if ok || sample != (DirFilesystemSample{}) || inode != 0 || !expires.IsZero() {
		t.Fatal("empty descriptor observation became evidence")
	}
}
