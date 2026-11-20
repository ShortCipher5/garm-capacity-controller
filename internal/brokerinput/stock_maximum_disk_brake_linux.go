package brokerinput

import "time"

type stockDiskAmount struct{ bytes, inodes uint64 }

// All fields must describe one independently scoped backing filesystem. The
// arithmetic cannot establish this and must not be called with an inferred
// project/host/pool match. Available means available to the runner workload,
// not filesystem blocks reserved to root.
type stockDiskHeadroomSample struct {
	at               time.Time
	total, available stockDiskAmount
}

type stockDiskReserve struct {
	absolute                          stockDiskAmount
	byteBasisPoints, inodeBasisPoints uint64
}

// This must be estimated in the context of this exact free-space sample and
// scope. It is not derived from whole used capacity, and it grants nothing.
type stockDiskPendingEstimate struct {
	sample    stockDiskHeadroomSample // Exact value checked by the pending estimator, not filesystem provenance.
	unspent   stockDiskAmount
	expiresAt time.Time
}

type stockMaximumDiskInputs struct {
	sample  *stockDiskHeadroomSample
	reserve stockDiskReserve
	// Only the unspent estimated part of already accepted/pending work.
	// Existing materialized usage is already reflected in available space and
	// must not be deducted again. Nil means unknown; a known zero is explicit.
	unspentPending *stockDiskPendingEstimate
	maxAge         time.Duration
}

type stockMaximumDiskCandidate struct {
	proposal    StockMaximumProposal
	incremental *stockDiskAmount // Conservative incremental growth estimate; nil is unknown.
}

type stockMaximumDiskReason string

const (
	stockDiskUnavailable    stockMaximumDiskReason = "disk_headroom_unavailable"
	stockDiskStale          stockMaximumDiskReason = "disk_headroom_stale"
	stockDiskPendingUnknown stockMaximumDiskReason = "disk_pending_estimate_unavailable"
	stockDiskGrowthUnknown  stockMaximumDiskReason = "disk_growth_estimate_unavailable"
	stockDiskBytesReserve   stockMaximumDiskReason = "disk_bytes_reserve"
	stockDiskInodesReserve  stockMaximumDiskReason = "disk_inodes_reserve"
)

type stockMaximumDiskVeto struct {
	pool   string
	reason stockMaximumDiskReason
}

type stockMaximumDiskBrake struct {
	// Structural refusal is not a per-proposal disk veto. No partial set is
	// returned; a caller must hold the malformed batch as a whole.
	invalidBatch bool
	proposals    []StockMaximumProposal
	vetoes       []stockMaximumDiskVeto
	growthExpiry time.Time // Expiry only for surviving growth; never extends existing plan evidence.
}

// vetoStockMaximumDiskGrowth filters an existing cycle for a SINGLE separately
// matched filesystem, in the planner's original order. Reductions pass through
// unchanged and never credit free space. Missing or stale evidence vetoes
// growth only. Success means this brake did not veto the proposal: it is not
// proof of a filesystem quota, route, settled capacity or permission to scale.
func vetoStockMaximumDiskGrowth(rows []stockMaximumDiskCandidate, in *stockMaximumDiskInputs, now time.Time) stockMaximumDiskBrake {
	if len(rows) > stockMaximumDiskRows {
		return stockMaximumDiskBrake{invalidBatch: true}
	}
	brake := newStockMaximumDiskBudget(in, now)
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.proposal.PoolID]++
	}
	var out stockMaximumDiskBrake
	for _, row := range rows {
		p := row.proposal
		if p.Suggested <= p.Observed {
			out.proposals = append(out.proposals, p)
			continue
		}
		reason := brake.check(p, counts[p.PoolID], row.incremental)
		if reason != "" {
			out.vetoes = append(out.vetoes, stockMaximumDiskVeto{p.PoolID, reason})
			continue
		}
		brake.debit(*row.incremental)
		out.proposals = append(out.proposals, p)
		out.growthExpiry = brake.expiresAt
	}
	return out
}

const stockMaximumDiskRows = 1000 // Existing stock maximum batch input bound.

type stockMaximumDiskBudget struct {
	available stockDiskAmount
	expiresAt time.Time
	reason    stockMaximumDiskReason
}

func newStockMaximumDiskBudget(in *stockMaximumDiskInputs, now time.Time) stockMaximumDiskBudget {
	available, expiresAt, reason := stockDiskGrowthAvailable(in, now)
	return stockMaximumDiskBudget{available, expiresAt, reason}
}

func (b stockMaximumDiskBudget) check(p StockMaximumProposal, count int, incremental *stockDiskAmount) stockMaximumDiskReason {
	switch {
	case b.reason != "":
		return b.reason
	case p.PoolID == "" || count != 1 || p.Observed <= 0 || p.Suggested-p.Observed != 1:
		return stockDiskGrowthUnknown
	case incremental == nil || incremental.bytes == 0 || incremental.inodes == 0:
		return stockDiskGrowthUnknown
	case incremental.bytes > b.available.bytes:
		return stockDiskBytesReserve
	case incremental.inodes > b.available.inodes:
		return stockDiskInodesReserve
	}
	return ""
}

// The caller must first check every participating budget. An earlier vetoed
// change never spends resources in any observation.
func (b *stockMaximumDiskBudget) debit(cost stockDiskAmount) {
	b.available.bytes -= cost.bytes
	b.available.inodes -= cost.inodes
}

func stockDiskGrowthAvailable(in *stockMaximumDiskInputs, now time.Time) (stockDiskAmount, time.Time, stockMaximumDiskReason) {
	fail := func(reason stockMaximumDiskReason) (stockDiskAmount, time.Time, stockMaximumDiskReason) {
		return stockDiskAmount{}, time.Time{}, reason
	}
	if in == nil || in.sample == nil || now.IsZero() || in.maxAge <= 0 || in.maxAge > pressureAge {
		return fail(stockDiskUnavailable)
	}
	s := in.sample
	if s.at.IsZero() || s.at.After(now) {
		return fail(stockDiskUnavailable)
	}
	expiry := s.at.Add(in.maxAge)
	if !expiry.After(now) {
		return fail(stockDiskStale)
	}
	if s.total.bytes == 0 || s.total.inodes == 0 || s.available.bytes > s.total.bytes || s.available.inodes > s.total.inodes ||
		in.reserve.byteBasisPoints > 10000 || in.reserve.inodeBasisPoints > 10000 ||
		in.reserve.absolute.bytes == 0 && in.reserve.byteBasisPoints == 0 ||
		in.reserve.absolute.inodes == 0 && in.reserve.inodeBasisPoints == 0 {
		return fail(stockDiskUnavailable)
	}
	if in.unspentPending == nil || !in.unspentPending.expiresAt.After(now) ||
		!sameStockDiskHeadroomSample(in.unspentPending.sample, *s) {
		return fail(stockDiskPendingUnknown)
	}
	bytesFloor := max(in.reserve.absolute.bytes, stockDiskFractionCeil(s.total.bytes, in.reserve.byteBasisPoints))
	inodesFloor := max(in.reserve.absolute.inodes, stockDiskFractionCeil(s.total.inodes, in.reserve.inodeBasisPoints))
	pending := in.unspentPending.unspent
	if in.unspentPending.expiresAt.Before(expiry) {
		expiry = in.unspentPending.expiresAt
	}
	if bytesFloor > s.available.bytes || pending.bytes > s.available.bytes-bytesFloor {
		return fail(stockDiskBytesReserve)
	}
	if inodesFloor > s.available.inodes || pending.inodes > s.available.inodes-inodesFloor {
		return fail(stockDiskInodesReserve)
	}
	return stockDiskAmount{s.available.bytes - bytesFloor - pending.bytes, s.available.inodes - inodesFloor - pending.inodes}, expiry, ""
}

func sameStockDiskHeadroomSample(a, b stockDiskHeadroomSample) bool {
	return a.at.Equal(b.at) && a.total == b.total && a.available == b.available
}

// The caller bounds basis points to 10000; divide first to avoid overflow.
func stockDiskFractionCeil(total, basisPoints uint64) uint64 {
	const scale = 10000
	return total/scale*basisPoints + (total%scale*basisPoints+scale-1)/scale
}
