package brokerinput

import (
	"math"
	"time"
)

type stockDiskWorkPhase uint8

const (
	stockDiskWorkUnknown stockDiskWorkPhase = iota
	// Accepted, in-flight, running, or cleaning work whose potential future
	// allocation is still bounded by the supplied planned footprint.
	stockDiskWorkMayAllocate
	// Independently observed non-writing retained work. Its existing footprint
	// remains in the filesystem statistics; this is not proof of removal.
	stockDiskWorkQuiescentRetained
)

type stockDiskWorkFootprint struct {
	id, profile string
	phase       stockDiskWorkPhase
	// The disjoint physical footprint already reflected in the exact free
	// sample. Nil is unknown; explicit zero means known not materialized.
	accounted *stockDiskAmount
}

// The caller must independently scope these data to one actual filesystem,
// supply a complete canonical work membership (one ID per allocation), and
// establish that accounted footprints do not overlap and refer to sample.
// Comparing the sampled values checks correspondence, not mount identity.
type stockDiskPendingCapture struct {
	sample    stockDiskHeadroomSample
	expiresAt time.Time
	scope     map[string]string // Work ID -> reviewed planned profile. Nil is unknown; explicit empty is known empty.
	rows      []stockDiskWorkFootprint
}

// estimateStockDiskPending computes ONLY additional estimated demand not yet
// reflected in the sampled free space. It neither reads nor changes charges,
// persists a decision, clears unknown work, or turns a proposal into capacity.
func estimateStockDiskPending(sample *stockDiskHeadroomSample, capture *stockDiskPendingCapture, planned map[string]stockDiskAmount, now time.Time) *stockDiskPendingEstimate {
	const maxWork, maxProfiles = 1000, 1000
	if sample == nil || capture == nil || capture.scope == nil || len(capture.scope) > maxWork || len(capture.rows) != len(capture.scope) ||
		len(planned) > maxProfiles || now.IsZero() || sample.at.IsZero() || sample.at.After(now) || !sameStockDiskHeadroomSample(capture.sample, *sample) ||
		!capture.expiresAt.After(now) || capture.expiresAt.After(sample.at.Add(pressureAge)) ||
		sample.total.bytes == 0 || sample.total.inodes == 0 ||
		sample.available.bytes > sample.total.bytes || sample.available.inodes > sample.total.inodes {
		return nil
	}
	// This is only a bound on what could already be accounted, not a source of
	// work identity and never an additional deduction from available space.
	unavailable := stockDiskAmount{sample.total.bytes - sample.available.bytes, sample.total.inodes - sample.available.inodes}
	seen := make(map[string]bool, len(capture.rows))
	var unspent stockDiskAmount
	for _, row := range capture.rows {
		profile, expected := capture.scope[row.id]
		limit, hasSize := planned[profile]
		if !expected || seen[row.id] || !stockDiskWorkLabel(row.id, 256) || !stockDiskWorkLabel(profile, 128) || row.profile != profile ||
			!hasSize || limit.bytes == 0 || limit.inodes == 0 || row.accounted == nil ||
			(row.phase != stockDiskWorkMayAllocate && row.phase != stockDiskWorkQuiescentRetained) {
			return nil
		}
		seen[row.id] = true
		got := *row.accounted
		if got.bytes > limit.bytes || got.inodes > limit.inodes || got.bytes > unavailable.bytes || got.inodes > unavailable.inodes {
			return nil
		}
		unavailable.bytes -= got.bytes
		unavailable.inodes -= got.inodes
		if row.phase == stockDiskWorkQuiescentRetained {
			continue // No extra estimated writes and no credit for retained bytes/inodes.
		}
		left := stockDiskAmount{limit.bytes - got.bytes, limit.inodes - got.inodes}
		if left.bytes > math.MaxUint64-unspent.bytes || left.inodes > math.MaxUint64-unspent.inodes {
			return nil
		}
		unspent.bytes += left.bytes
		unspent.inodes += left.inodes
	}
	return &stockDiskPendingEstimate{sample: *sample, unspent: unspent, expiresAt: capture.expiresAt}
}

func stockDiskWorkLabel(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for _, c := range []byte(value) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' || c == '.') {
			return false
		}
	}
	return true
}
