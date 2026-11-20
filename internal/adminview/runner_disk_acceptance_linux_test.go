package adminview

import (
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

func runnerDiskAcceptanceFixture(t *testing.T) (RunnerDiskHealthBinding, hostinventory.DirFilesystemSample) {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scope := hostinventory.DirFilesystemScope{
		HostFingerprint: strings.Repeat("1", 64), StoragePool: "ci-pool", BootID: "11111111-1111-4111-8111-111111111111",
		MountNamespace: 4, MountID: 10, DeviceMajor: 8, DeviceMinor: 1,
	}
	reading, ok := hostinventory.NormalizeDirFilesystem(scope, scope, "ext4", at, &hostinventory.DirFilesystemCounters{
		FragmentBytes: 4096, TotalBlocks: 2, FreeBlocks: 1, AvailableBlocks: 0, TotalInodes: 7, FreeInodes: 0,
	})
	if !ok {
		t.Fatal("invalid synthetic normalizer input")
	}
	return RunnerDiskHealthBinding{HostID: "ledger-host", Scope: scope, DirectoryInode: 33, ReviewExpiresAt: at.Add(9 * time.Minute)}, reading
}

func TestRunnerDiskAcceptanceUsesEarliestStrictCutoff(t *testing.T) {
	binding, sample := runnerDiskAcceptanceFixture(t)
	at := sample.ObservedAt
	for _, tc := range []struct {
		name                              string
		routeReview, sampleReview, cutoff time.Duration
	}{
		{"route review", 2 * time.Minute, 3 * time.Minute, 2 * time.Minute},
		{"sample review", 4 * time.Minute, 3 * time.Minute, 3 * time.Minute},
		{"reading age", 9 * time.Minute, 8 * time.Minute, maxRunnerDiskHealthAge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := binding
			b.ReviewExpiresAt = at.Add(tc.routeReview)
			deadline := at.Add(tc.sampleReview)
			wantExpiry := at.Add(tc.cutoff)
			current := wantExpiry.Add(-time.Nanosecond)
			host, row, ok := acceptRunnerDiskHealth(b, sample, binding.DirectoryInode, deadline, current)
			want := RunnerDiskHealthReading{StoragePool: "ci-pool", ObservedAt: at, ExpiresAt: wantExpiry, TotalBytes: 8192, TotalInodes: 7}
			if !ok || host != binding.HostID || row != want {
				t.Fatalf("got %q, %+v, %t; want %q, %+v, true", host, row, ok, binding.HostID, want)
			}
			if got := projectRunnerDiskHealth([]RunnerDiskHealthReading{row}, current); len(got) != 1 || got[0].State != "current" || got[0].AvailableBytes != "0" || got[0].AvailableInodes != "0" {
				t.Fatalf("admin projection lost current zero availability: %+v", got)
			}
			for _, now := range []time.Time{wantExpiry, wantExpiry.Add(time.Nanosecond)} {
				if gotHost, gotRow, ok := acceptRunnerDiskHealth(b, sample, binding.DirectoryInode, deadline, now); ok || gotHost != "" || gotRow != (RunnerDiskHealthReading{}) {
					t.Fatalf("expired reading leaked at %s: %q %+v %t", now, gotHost, gotRow, ok)
				}
			}
		})
	}
}

func TestRunnerDiskAcceptanceRefusesScopeAndClockMismatches(t *testing.T) {
	binding, sample := runnerDiskAcceptanceFixture(t)
	reviewUntil := binding.ReviewExpiresAt
	now := sample.ObservedAt.Add(time.Minute)
	assertUnknown := func(b RunnerDiskHealthBinding, s hostinventory.DirFilesystemSample, inode uint64, cutoff, now time.Time) {
		t.Helper()
		if host, row, ok := acceptRunnerDiskHealth(b, s, inode, cutoff, now); ok || host != "" || row != (RunnerDiskHealthReading{}) {
			t.Fatalf("unknown leaked: %q %+v %t", host, row, ok)
		}
	}
	for _, mutate := range []func(*hostinventory.DirFilesystemScope){
		func(s *hostinventory.DirFilesystemScope) { s.HostFingerprint = strings.Repeat("2", 64) },
		func(s *hostinventory.DirFilesystemScope) { s.StoragePool = "different-pool" },
		func(s *hostinventory.DirFilesystemScope) { s.BootID = "22222222-2222-4222-8222-222222222222" },
		func(s *hostinventory.DirFilesystemScope) { s.MountNamespace++ },
		func(s *hostinventory.DirFilesystemScope) { s.MountID++ },
		func(s *hostinventory.DirFilesystemScope) { s.DeviceMajor++ },
		func(s *hostinventory.DirFilesystemScope) { s.DeviceMinor++ },
	} {
		s := sample
		mutate(&s.Scope)
		assertUnknown(binding, s, binding.DirectoryInode, reviewUntil, now)
	}
	assertUnknown(binding, sample, binding.DirectoryInode+1, reviewUntil, now)
	assertUnknown(binding, sample, 0, reviewUntil, now)
	assertUnknown(binding, sample, binding.DirectoryInode, reviewUntil, time.Time{})
	assertUnknown(binding, sample, binding.DirectoryInode, reviewUntil, sample.ObservedAt.Add(-time.Nanosecond))
	assertUnknown(binding, sample, binding.DirectoryInode, time.Time{}, now)
	missing := binding
	missing.ReviewExpiresAt = time.Time{}
	assertUnknown(missing, sample, binding.DirectoryInode, reviewUntil, now)
	missing = binding
	missing.HostID = ""
	assertUnknown(missing, sample, binding.DirectoryInode, reviewUntil, now)
	if host, row, ok := AcceptRunnerDiskHealth(binding, hostinventory.BoundDirFilesystemSample{}, now); ok || host != "" || row != (RunnerDiskHealthReading{}) {
		t.Fatalf("unminted evidence leaked: %q %+v %t", host, row, ok)
	}
}
