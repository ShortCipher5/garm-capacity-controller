package cihealth

import (
	"context"
	"encoding/json"
	"os"
	"time"
)

// JobUsageDisplay holds the independently retained display wire and its
// local publication lease. Only a checked exact job read can construct it.
type JobUsageDisplay struct {
	store                       *UsageAttachmentStore
	usageVersion, timingVersion *os.File
	issued, expires             time.Time
	wire                        json.RawMessage
}

// Current performs no I/O. Both directory owners replace their current
// descriptor on every committed revision, and clear it on close. A writer
// currently holding either lock causes a timing-only response.
func (v JobUsageDisplay) Current() bool {
	s := v.store
	if s == nil || !s.mu.TryLock() {
		return false
	}
	defer s.mu.Unlock()
	h := s.history
	if s.failed || s.currentFD != v.usageVersion || h == nil || !h.mu.TryLock() {
		return false
	}
	defer h.mu.Unlock()
	now := s.now().UTC()
	return !h.failed && h.currentFD == v.timingVersion && !now.Before(v.issued) && now.Before(v.expires)
}

func (v JobUsageDisplay) Wire() json.RawMessage {
	if !v.Current() {
		return nil
	}
	return append(json.RawMessage(nil), v.wire...)
}

// ObserveDisplay checks the owner's exact history instance, committed workflow
// cursor, current exact job membership and live timing against the original
// evidence window. It never performs an upstream read or updates a record.
func (s *UsageAttachmentStore) ObserveDisplay(ctx context.Context, h *History, cursor HistoryCursor, job JobKey, live Clock, cycleAt time.Time) (JobUsageDisplay, bool) {
	var none JobUsageDisplay
	if s == nil || h == nil || !validKey(job.Attempt) || job.JobID <= 0 || cursor.Revision <= 0 || !historyActive(ctx) || !s.mu.TryLock() {
		return none, false
	}
	defer s.mu.Unlock()
	if s.history != h || s.root == nil || !h.mu.TryLock() {
		return none, false
	}
	defer h.mu.Unlock()
	now := s.now().UTC()
	_, cp := h.currentCheckpoint(historyScope(job.Attempt))
	if cp.Cursor != cursor {
		return none, false
	}
	a, retained, expires, err := s.readCurrent(ctx, job, now)
	if err != nil || a.Job != job || !validTime(live.ObservedAt) || cycleAt.After(now) || live.ObservedAt.After(cycleAt) || a.EvidenceAsOf.After(cycleAt) || !live.StartedAt.Equal(a.WindowStart) ||
		!timingAllowsUsageWindow(live, a.WindowStart, a.WindowEnd) || live.ObservedAt.Before(a.WindowEnd) || !displayPhasesCompatible(retained, live) {
		return none, false
	}
	expires = minUsageDeadline(expires, a.EvidenceAsOf.Add(usageAttachmentRetention))
	wire := a.Evidence
	switch live.Phase {
	case Completed:
		// A shorter sampled prefix cannot be labeled as the whole completed job.
		if !live.CompletedAt.Equal(a.WindowEnd) || (!retained.CompletedAt.IsZero() && !retained.CompletedAt.Equal(live.CompletedAt)) {
			return none, false
		}
	case Running:
		if !live.CompletedAt.IsZero() || (!retained.CompletedAt.IsZero() && !live.ObservedAt.Before(retained.CompletedAt)) {
			return none, false
		}
		// A later identity or execution observation cannot refresh an old counter window.
		freshUntil := a.WindowEnd.Add(MaxJobResourceGap)
		if !now.Before(freshUntil) {
			wire = Wire{State: VerifiedUsageStale, Reason: UsageOmissionStaleEvidence, AsOf: wire.AsOf}
		} else {
			expires = minUsageDeadline(expires, freshUntil)
		}
	default:
		return none, false
	}
	raw, err := json.Marshal(wire)
	if err != nil || !historyActive(ctx) {
		return none, false
	}
	return JobUsageDisplay{s, s.currentFD, h.currentFD, now, expires, raw}, true
}

// Sparse timing rows can still positively contradict execution. Apply
// their independently observed phases even when their endpoints are absent.
func displayPhasesCompatible(retained, live Clock) bool {
	switch retained.Phase {
	case Skipped:
		return false
	case Queued:
		return !retained.ObservedAt.IsZero() && !retained.ObservedAt.After(live.StartedAt)
	case Completed:
		if live.Phase == Running {
			return !retained.ObservedAt.IsZero() && live.ObservedAt.Before(retained.ObservedAt)
		}
		return retained.ObservedAt.IsZero() || !retained.ObservedAt.Before(live.CompletedAt)
	case Running:
		return live.Phase != Completed || (!retained.ObservedAt.IsZero() && retained.ObservedAt.Before(live.CompletedAt))
	default:
		return true
	}
}

func minUsageDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
