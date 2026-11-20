package hostinventory

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDirFilesystemFDUsesSameScopeAndConservativeCounters(t *testing.T) {
	scope, counters, at := dirFilesystemFixture()
	binding := DirFilesystemFDBinding{scope.HostFingerprint, scope.StoragePool, scope.BootID, scope.MountNamespace, "ext4"}
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
	binding := DirFilesystemFDBinding{scope.HostFingerprint, scope.StoragePool, scope.BootID, scope.MountNamespace, "ext4"}
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, ok := SampleDirFilesystemFD(ctx, dir, scope, binding); ok || got != (DirFilesystemSample{}) {
		t.Fatal("canceled observation accepted")
	}
	if got, ok := SampleDirFilesystemFD(t.Context(), nil, scope, binding); ok || got != (DirFilesystemSample{}) {
		t.Fatal("absent descriptor accepted")
	}
	if got, ok := SampleDirFilesystemFD(t.Context(), new(os.File), scope, binding); ok || got != (DirFilesystemSample{}) {
		t.Fatal("uninitialized descriptor accepted")
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	if got, ok := SampleDirFilesystemFD(t.Context(), dir, scope, binding); ok || got != (DirFilesystemSample{}) {
		t.Fatal("closed descriptor accepted")
	}
}

func TestDirFilesystemFDTimestampPrecedesCountersAndDeadlineIsStrict(t *testing.T) {
	scope, _, _ := dirFilesystemFixture()
	binding := DirFilesystemFDBinding{scope.HostFingerprint, scope.StoragePool, scope.BootID, scope.MountNamespace, "ext4"}
	base := time.Now()
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
