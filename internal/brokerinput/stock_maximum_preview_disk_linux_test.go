package brokerinput

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

func diskPreviewSample(t *testing.T, at time.Time) *stockMaximumPreviewDisk {
	t.Helper()
	scope := hostinventory.DirFilesystemScope{
		HostFingerprint: strings.Repeat("a", 64), StoragePool: "dir-runners",
		BootID: "11111111-1111-4111-8111-111111111111", MountNamespace: 5, MountID: 6, DeviceMajor: 8,
	}
	fs, ok := hostinventory.NormalizeDirFilesystem(scope, scope, "ext4", at, &hostinventory.DirFilesystemCounters{
		FragmentBytes: 4096, TotalBlocks: 200, FreeBlocks: 70, AvailableBlocks: 60, TotalInodes: 100, FreeInodes: 12,
	})
	if !ok {
		t.Fatal("invalid fixture")
	}
	out := &stockMaximumPreviewDisk{
		filesystem:  fs,
		reserve:     stockDiskReserve{absolute: stockDiskAmount{4096, 1}},
		maxAge:      30 * time.Second,
		incremental: map[string]stockDiskAmount{"z": {4096, 3}, "a": {4096, 3}},
	}
	bindEmptyDiskPreviewPending(t, out, at.Add(27*time.Second))
	return out
}

func bindEmptyDiskPreviewPending(t *testing.T, out *stockMaximumPreviewDisk, expires time.Time) {
	t.Helper()
	f := out.filesystem
	s := stockDiskHeadroomSample{
		at:        f.ObservedAt,
		total:     stockDiskAmount{uint64(f.TotalBytes), uint64(f.TotalInodes)},
		available: stockDiskAmount{uint64(f.AvailableBytes), uint64(f.AvailableInodes)},
	}
	out.pending = estimateStockDiskPending(&s, &stockDiskPendingCapture{
		sample: s, expiresAt: expires, scope: map[string]string{},
	}, nil, s.at)
	if out.pending == nil {
		t.Fatal("empty pending fixture was not bound")
	}
}

func diskPreviewFixture(t *testing.T) []scalingPreviewSample {
	t.Helper()
	h := maximumCycleHistory(4, 7)
	for i := range h {
		h[i].diskRequired = true
		h[i].disk = diskPreviewSample(t, h[i].finish)
	}
	return h
}

func diskPreviewPools(rows []StockMaximumProposal) []string {
	ids := make([]string, 0, len(rows))
	for _, p := range rows {
		ids = append(ids, p.PoolID)
	}
	return ids
}

func TestStockMaximumDiskPreviewSharedOrderAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name            string
		edit            func(a, b *stockMaximumPreviewDisk)
		ordinary, batch []string
		short           bool
	}{
		{"both fit", func(a, b *stockMaximumPreviewDisk) {}, []string{"empty", "z", "a"}, []string{"z", "a"}, true},
		{"one surviving change", func(a, b *stockMaximumPreviewDisk) { a.filesystem.AvailableInodes = 6 }, []string{"empty", "z"}, []string{"z", "empty"}, true},
		{"later candidate uses capacity not consumed by veto", func(a, b *stockMaximumPreviewDisk) {
			a.filesystem.AvailableInodes = 6
			b.incremental["z"] = stockDiskAmount{1 << 20, 3}
		}, []string{"empty", "a"}, []string{"a", "empty"}, true},
		{"stale pending estimate preserves cuts", func(a, b *stockMaximumPreviewDisk) {
			a.pending.expiresAt = b.filesystem.ObservedAt
		}, []string{"empty"}, []string{"empty"}, false},
		{"stale filesystem sample preserves cuts", func(a, b *stockMaximumPreviewDisk) {
			a.maxAge, b.maxAge = 20*time.Second, 20*time.Second
		}, []string{"empty"}, []string{"empty"}, false},
		{"spare inode cannot replace missing bytes", func(a, b *stockMaximumPreviewDisk) {
			b.filesystem.AvailableBytes = 4096
		}, []string{"empty"}, []string{"empty"}, false},
		{"unproved mounted route per pool", func(a, b *stockMaximumPreviewDisk) {
			delete(a.incremental, "z")
			delete(b.incremental, "a")
		}, []string{"empty"}, []string{"empty"}, false},
		{"scope changed", func(a, b *stockMaximumPreviewDisk) {
			b.filesystem.Scope.MountID++
		}, []string{"empty"}, []string{"empty"}, false},
		{"matching partial scopes still invalid", func(a, b *stockMaximumPreviewDisk) {
			a.filesystem.Scope = hostinventory.DirFilesystemScope{StoragePool: "dir-runners"}
			b.filesystem.Scope = a.filesystem.Scope
		}, []string{"empty"}, []string{"empty"}, false},
		{"filesystem sampled after earlier stock capture", func(a, b *stockMaximumPreviewDisk) {
			a.filesystem.ObservedAt = a.filesystem.ObservedAt.Add(time.Nanosecond)
		}, []string{"empty"}, []string{"empty"}, false},
		{"old duplicate sample", func(a, b *stockMaximumPreviewDisk) {
			b.filesystem.ObservedAt = a.filesystem.ObservedAt
		}, []string{"empty"}, []string{"empty"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := diskPreviewFixture(t)
			tc.edit(h[2].disk, h[3].disk)
			// Keep the pending inputs valid in these cases so each intended
			// scope, timestamp, expiry, or resource condition is isolated.
			for _, s := range h[2:] {
				bindEmptyDiskPreviewPending(t, s.disk, s.disk.pending.expiresAt)
			}
			var one scalingPreviewHistory
			var batch stockMaximumBatchHistory
			var result, selected *StockMaximumPreview
			for _, s := range h {
				result = one.accept(s, nil, s.finish, s.expires)
				selected = batch.accept(s, nil, s.finish, s.expires)
			}
			if !slices.Equal(diskPreviewPools(result.Proposals), tc.ordinary) || !slices.Equal(diskPreviewPools(selected.Proposals), tc.batch) {
				t.Fatalf("ordinary=%+v batch=%+v", result, selected)
			}
			expiry := h[2].expires
			if tc.short {
				expiry = h[2].disk.pending.expiresAt
			}
			if !result.ExpiresAt.Equal(expiry) || !selected.ExpiresAt.Equal(expiry) {
				t.Fatalf("expiry ordinary=%s batch=%s want=%s", result.ExpiresAt, selected.ExpiresAt, expiry)
			}
			if result.HostEvidence != "configured_only" || selected.HostEvidence != "configured_only" {
				t.Fatal("filesystem evidence promoted configured route")
			}
		})
	}
}

func TestStockMaximumDiskPreviewOffOrRequiredMissing(t *testing.T) {
	h := maximumCycleHistory(4, 7)
	one := scalingPreviewHistory{prior: h[2]}
	now := h[3].finish
	out := one.accept(h[3], nil, now, h[3].expires)
	if !slices.Equal(diskPreviewPools(out.Proposals), []string{"empty", "z", "a"}) {
		t.Fatal("default path changed", out)
	}
	h[3].diskRequired = true
	one.prior = h[2]
	out = one.accept(h[3], nil, now, h[3].expires)
	if !slices.Equal(diskPreviewPools(out.Proposals), []string{"empty"}) {
		t.Fatal("required partially supplied inputs permitted growth", out)
	}
	h[2].diskRequired = true
	cycle, err := planStockMaximumCycle(h, stockMaximumCycleOptions{StableFor: 45 * time.Second, MaxChanges: 2}, now)
	if err != nil || !slices.Equal(diskPreviewPools(cycle.proposals), []string{"empty"}) {
		t.Fatal("required absent pair permitted growth or vetoed reductions", cycle, err)
	}
}

func TestStockMaximumDiskPreviewOverflowAndOwnedInputs(t *testing.T) {
	h := diskPreviewFixture(t)
	now := h[3].finish
	tooMany := make([]StockMaximumProposal, stockMaximumDiskRows+1)
	tooMany[0] = StockMaximumProposal{PoolID: "empty", Observed: 2, Suggested: 1}
	if result := filterStockMaximumPreviewDisk(tooMany, h[2], h[3], now); !result.invalidBatch || len(result.proposals) != 0 || len(result.vetoes) != 0 {
		t.Fatal("overflow emitted partial advice", result)
	}
	var history scalingPreviewHistory
	history.accept(h[2], nil, h[2].finish, h[2].expires)
	h[2].disk.pending.expiresAt = now
	clear(h[2].disk.incremental)
	out := history.accept(h[3], nil, now, h[3].expires)
	if !slices.Equal(diskPreviewPools(out.Proposals), []string{"empty", "z", "a"}) {
		t.Fatal("caller changed retained sample", out)
	}
}

func TestStockMaximumDiskPreviewRejectsPostCaptureLatestDisk(t *testing.T) {
	h := diskPreviewFixture(t)
	h[3].disk.filesystem.ObservedAt = h[3].finish.Add(time.Nanosecond)
	bindEmptyDiskPreviewPending(t, h[3].disk, h[3].disk.pending.expiresAt)
	now := h[3].finish.Add(time.Second)
	// The final time is after the disk read; ordinary future-time rejection
	// alone would accept it. Advice still requires the exact capture window.
	var history scalingPreviewHistory
	history.accept(h[2], nil, h[2].finish, h[2].expires)
	out := history.accept(h[3], nil, now, h[3].expires)
	cycle, err := planStockMaximumCycle(h, stockMaximumCycleOptions{StableFor: 45 * time.Second, MaxChanges: 2}, now)
	if err != nil || !slices.Equal(diskPreviewPools(out.Proposals), []string{"empty"}) ||
		!slices.Equal(diskPreviewPools(cycle.proposals), []string{"empty"}) {
		t.Fatal("post-capture latest sample permitted growth", out, cycle, err)
	}
}

func TestStockMaximumDiskPreviewRejectsPendingFromDifferentFreeSpace(t *testing.T) {
	h := diskPreviewFixture(t)
	// This changed read is still healthy and scoped. The previously bound
	// pending estimate cannot be used against it, even when it is empty.
	h[3].disk.filesystem.AvailableBytes++
	now := h[3].finish
	var history scalingPreviewHistory
	history.accept(h[2], nil, h[2].finish, h[2].expires)
	out := history.accept(h[3], nil, now, h[3].expires)
	cycle, err := planStockMaximumCycle(h, stockMaximumCycleOptions{StableFor: 45 * time.Second, MaxChanges: 2}, now)
	if err != nil || !slices.Equal(diskPreviewPools(out.Proposals), []string{"empty"}) ||
		!slices.Equal(diskPreviewPools(cycle.proposals), []string{"empty"}) {
		t.Fatal("sample-bound pending was accepted with different free space", out, cycle, err)
	}
}
