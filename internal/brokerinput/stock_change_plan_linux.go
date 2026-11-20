package brokerinput

import (
	"crypto/sha256"
	"errors"
	"time"
)

var errStockChangePlan = errors.New("diagnostic stock change plan unavailable")

// Deliberately package-private: this is not a public admission/write API.
// Zero-value options reject. Enabling this calculation enables no execution.
type stockChangePlanOptions struct {
	EnableValidation          bool
	MaxStep, Minimum, Maximum int64
}
type stockChangePlanRequest struct {
	PoolID, RepositoryID, Source, Fingerprint string
	Sequence, Revision, Observed, Delta       int64
	Scope                                     [sha256.Size]byte
}

// StockChangePlan describes checked advisory arithmetic only. It is NOT a
// current physical placement/headroom proof, reservation, write precondition,
// inverse operation or reusable execution permission. Actionable stays false
// until a separate, reviewed physical-input integration exists; this validator
// has no such input, so it cannot produce an actionable result.
type StockChangePlan struct {
	PoolID       string            `json:"pool_id"`
	RepositoryID string            `json:"repository_id"`
	Source       string            `json:"source"`
	Fingerprint  string            `json:"fingerprint"`
	Scope        [sha256.Size]byte `json:"scope"`
	Sequence     int64             `json:"ledger_sequence"`
	Revision     int64             `json:"policy_revision"`
	Observed     int64             `json:"observed"`
	Suggested    int64             `json:"suggested"`
	Delta        int64             `json:"delta"`
	Direction    string            `json:"direction"`
	ExpiresAt    time.Time         `json:"expires_at"`
	Actionable   bool              `json:"actionable"`
}

// Inputs retain the existing reader's pressure-veto expiry and configuration /
// host-binding / profile scope commitment. Do not replace these with browser
// DTOs or caller-created booleans. Even genuine samples are advisory snapshots:
// they do not prove current physical routing or justify positive writes.
func validateStockChangePlan(previous, current scalingPreviewSample, request stockChangePlanRequest, options stockChangePlanOptions, now time.Time) (StockChangePlan, error) {
	fail := func() (StockChangePlan, error) { return StockChangePlan{}, errStockChangePlan }
	if !options.EnableValidation || options.MaxStep != 1 || options.Minimum < 1 || options.Maximum < options.Minimum || options.Maximum > 1000000 || now.IsZero() || request.Delta != 1 && request.Delta != -1 {
		return fail()
	}
	if current.scope == ([sha256.Size]byte{}) || current.scope != previous.scope || request.Scope != current.scope || request.Source == "" || len(request.Source) > 256 || request.Source != current.source || request.Sequence != current.sequence || request.Revision != current.revision || request.Sequence < 0 || request.Revision < 0 {
		return fail()
	}
	for _, s := range []StockScalingSample{previous.StockScalingSample, current.StockScalingSample} {
		if s.start.IsZero() || s.finish.IsZero() || s.finish.Before(s.start) || s.finish.After(now) || !s.expires.After(s.finish) || !s.expires.After(now) || s.incomplete {
			return fail()
		}
	}
	p, ok := current.pools[request.PoolID]
	if !ok || request.PoolID == "" || len(request.PoolID) > 256 || request.RepositoryID == "" || len(request.RepositoryID) > 256 || request.RepositoryID != p.repo || request.Fingerprint == "" || len(request.Fingerprint) > 256 || request.Fingerprint != p.fingerprint || !p.enabled || p.current < 1 || p.current > 1000000 || p.current != request.Observed || p.idle < 0 || p.idle > p.current || p.used < 0 || p.used > 100000 || p.cap < 1 || p.cap > 1000000 {
		return fail()
	}
	target := p.current + request.Delta // current is bounded, so arithmetic cannot overflow.
	floor := max(options.Minimum, p.idle, p.used)
	if target < floor || target > min(options.Maximum, p.cap) || target <= 0 {
		return fail()
	}
	expiry := current.expires
	if previous.expires.Before(expiry) {
		expiry = previous.expires
	}
	proposals := ProposeStockMaximums(previous.StockScalingSample, current.StockScalingSample, now)
	if request.Delta > 0 && (previous.diskRequired || current.diskRequired) {
		// Use the whole weighted order: isolating the requested pool could spend
		// the same filesystem budget already consumed by an earlier proposal.
		disk := filterStockMaximumPreviewDisk(proposals, previous, current, now)
		if disk.invalidBatch || !disk.growthExpiry.After(now) {
			return fail()
		}
		proposals = disk.proposals
		if disk.growthExpiry.Before(expiry) {
			expiry = disk.growthExpiry
		}
	}
	confirmed := false
	for _, proposal := range proposals {
		if proposal.PoolID == request.PoolID && proposal.RepositoryID == request.RepositoryID && proposal.Observed == request.Observed && proposal.Suggested == target {
			confirmed = true
			break
		}
	}
	if !confirmed {
		return fail()
	}
	direction := "increase"
	if request.Delta < 0 {
		direction = "decrease"
	}
	return StockChangePlan{PoolID: request.PoolID, RepositoryID: request.RepositoryID, Source: request.Source, Fingerprint: request.Fingerprint, Scope: request.Scope, Sequence: request.Sequence, Revision: request.Revision, Observed: request.Observed, Suggested: target, Delta: request.Delta, Direction: direction, ExpiresAt: expiry, Actionable: false}, nil
}
