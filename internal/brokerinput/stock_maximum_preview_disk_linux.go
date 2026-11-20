package brokerinput

import (
	"maps"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

// Private inputs for one explicitly required, already-scoped advisory capture.
// Nobody can derive these from guest usage or configured eligible hosts: a
// future trusted caller must independently match every supplied candidate
// estimate and the pending set to this exact backing filesystem and capture.
// There is deliberately no runtime producer yet.
type stockMaximumPreviewDisk struct {
	filesystem hostinventory.DirFilesystemSample
	reserve    stockDiskReserve
	pending    *stockDiskPendingEstimate
	maxAge     time.Duration
	// A missing entry is unknown (including any pool lacking a proven mapping
	// to filesystem). Values are independent estimates for this observation.
	incremental map[string]stockDiskAmount
}

func ownStockMaximumPreviewDisk(in *stockMaximumPreviewDisk) *stockMaximumPreviewDisk {
	if in == nil || len(in.incremental) > stockMaximumDiskRows {
		return nil
	}
	out := *in
	out.incremental = maps.Clone(in.incremental)
	if in.pending != nil {
		copy := *in.pending
		out.pending = &copy
	}
	return &out
}

func (s *stockMaximumPreviewDisk) budget(now time.Time) stockMaximumDiskBudget {
	if s == nil || s.filesystem.TotalBytes <= 0 || s.filesystem.TotalInodes <= 0 ||
		s.filesystem.AvailableBytes < 0 || s.filesystem.AvailableInodes < 0 || len(s.incremental) > stockMaximumDiskRows {
		return stockMaximumDiskBudget{reason: stockDiskUnavailable}
	}
	f := s.filesystem
	return newStockMaximumDiskBudget(&stockMaximumDiskInputs{
		sample: &stockDiskHeadroomSample{
			at:        f.ObservedAt,
			total:     stockDiskAmount{uint64(f.TotalBytes), uint64(f.TotalInodes)},
			available: stockDiskAmount{uint64(f.AvailableBytes), uint64(f.AvailableInodes)},
		},
		reserve: s.reserve, unspentPending: s.pending, maxAge: s.maxAge,
	}, now)
}

// Apply a veto, never a capacity grant, to the planner's existing order. Both
// independent captures must accept a growth before either budget is debited.
// No cut credits disk, and this cannot add or reorder a recommendation.
func filterStockMaximumPreviewDisk(proposals []StockMaximumProposal, previous, current scalingPreviewSample, now time.Time) stockMaximumDiskBrake {
	if !previous.diskRequired && !current.diskRequired {
		return stockMaximumDiskBrake{proposals: proposals}
	}
	if len(proposals) > stockMaximumDiskRows {
		return stockMaximumDiskBrake{invalidBatch: true}
	}
	a, b := previous.disk, current.disk
	pairValid := previous.diskRequired && current.diskRequired && a != nil && b != nil &&
		a.filesystem.Scope == b.filesystem.Scope && a.filesystem.Scope.Valid() &&
		!a.filesystem.ObservedAt.Before(previous.start) && !a.filesystem.ObservedAt.After(previous.finish) &&
		!b.filesystem.ObservedAt.Before(current.start) && !b.filesystem.ObservedAt.After(current.finish) &&
		a.reserve == b.reserve && a.maxAge == b.maxAge
	prior, latest := a.budget(now), b.budget(now)
	counts := make(map[string]int, len(proposals))
	for _, p := range proposals {
		counts[p.PoolID]++
	}
	var result stockMaximumDiskBrake
	for _, p := range proposals {
		if p.Suggested <= p.Observed {
			result.proposals = append(result.proposals, p)
			continue
		}
		reason := stockDiskUnavailable
		if pairValid {
			priorCost, currentCost := a.incremental[p.PoolID], b.incremental[p.PoolID]
			reason = prior.check(p, counts[p.PoolID], &priorCost)
			if reason == "" {
				reason = latest.check(p, counts[p.PoolID], &currentCost)
			}
			if reason == "" {
				prior.debit(priorCost)
				latest.debit(currentCost)
				result.proposals = append(result.proposals, p)
				result.growthExpiry = prior.expiresAt
				if latest.expiresAt.Before(result.growthExpiry) {
					result.growthExpiry = latest.expiresAt
				}
				continue
			}
		}
		result.vetoes = append(result.vetoes, stockMaximumDiskVeto{p.PoolID, reason})
	}
	return result
}
