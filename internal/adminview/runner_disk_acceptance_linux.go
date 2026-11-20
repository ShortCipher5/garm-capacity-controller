package adminview

import (
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

// RunnerDiskHealthBinding is an independently reviewed mapping from a ledger
// host to the actual backing filesystem of its runner storage pool. Populate
// it in trusted code from the route review, never from the observed sample or
// untrusted JSON. Matching the binding is not proof of the route itself.
type RunnerDiskHealthBinding struct {
	HostID          string
	Scope           hostinventory.DirFilesystemScope
	DirectoryInode  uint64
	ReviewExpiresAt time.Time
}

// AcceptRunnerDiskHealth joins one retained-descriptor sample to an independent
// route binding for display only. The caller supplies the current time and uses
// the returned host ID as the snapshot key. On mismatch, missing evidence or
// expiry it returns no host or counters (Unknown). It performs no I/O and does
// not provide planner headroom, allocate resources or allow settings changes.
func AcceptRunnerDiskHealth(binding RunnerDiskHealthBinding, observed hostinventory.BoundDirFilesystemSample, now time.Time) (string, RunnerDiskHealthReading, bool) {
	sample, inode, sampleReviewExpiresAt, ok := observed.Evidence()
	if !ok {
		return "", RunnerDiskHealthReading{}, false
	}
	return acceptRunnerDiskHealth(binding, sample, inode, sampleReviewExpiresAt, now)
}

// This private join assumes the counters have already been normalized by the
// retained-descriptor sampler. Only the opaque sampler result reaches it in
// production code; it does not normalize or parse another input format.
func acceptRunnerDiskHealth(binding RunnerDiskHealthBinding, sample hostinventory.DirFilesystemSample, inode uint64, sampleReviewExpiresAt, now time.Time) (string, RunnerDiskHealthReading, bool) {
	if binding.HostID == "" || !binding.Scope.Valid() || binding.Scope != sample.Scope ||
		binding.DirectoryInode == 0 || binding.DirectoryInode != inode ||
		!runnerDiskWireTime(binding.ReviewExpiresAt) || !runnerDiskWireTime(sampleReviewExpiresAt) ||
		!runnerDiskWireTime(sample.ObservedAt) || !runnerDiskWireTime(now) || sample.ObservedAt.After(now) {
		return "", RunnerDiskHealthReading{}, false
	}
	expiresAt := sample.ObservedAt.Add(maxRunnerDiskHealthAge)
	for _, cutoff := range [...]time.Time{binding.ReviewExpiresAt, sampleReviewExpiresAt} {
		if cutoff.Before(expiresAt) {
			expiresAt = cutoff
		}
	}
	if !runnerDiskWireTime(expiresAt) || !now.Before(expiresAt) {
		return "", RunnerDiskHealthReading{}, false
	}
	return binding.HostID, RunnerDiskHealthReading{
		StoragePool: sample.Scope.StoragePool, ObservedAt: sample.ObservedAt, ExpiresAt: expiresAt,
		TotalBytes: sample.TotalBytes, AvailableBytes: sample.AvailableBytes,
		TotalInodes: sample.TotalInodes, AvailableInodes: sample.AvailableInodes,
	}, true
}
