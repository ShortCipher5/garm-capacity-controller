package cihealth

import (
	"context"
	"os"
	"time"
)

// CompletedUsageWindow pins one enrolled job's terminal endpoints, committed
// workflow cursor, and the two local store revisions for a bounded read. A
// revoke/re-enroll or remove/restore cycle invalidates the original window.
type CompletedUsageWindow struct {
	store                        *UsageAttachmentStore
	usageVersion, historyVersion *os.File
	job                          JobKey
	start, end, issued           time.Time
}

// tryCollectionLocks keeps a bounded collection from queuing behind file I/O
// on either store. Check cancellation again after acquiring both locks.
func (s *UsageAttachmentStore) tryCollectionLocks(ctx context.Context, job JobKey, expected *History) func() {
	if s == nil || !historyActive(ctx) || !s.mu.TryLock() {
		return nil
	}
	h := s.history
	if h == nil || (expected != nil && h != expected) || !s.enrolled(usageScopeOf(job)) || !h.mu.TryLock() {
		s.mu.Unlock()
		return nil
	}
	unlock := func() {
		h.mu.Unlock()
		s.mu.Unlock()
	}
	if !historyActive(ctx) || s.root == nil || s.currentFD == nil || s.failed || s.now == nil ||
		h.currentFD == nil || h.failed {
		unlock()
		return nil
	}
	return unlock
}

func (s *UsageAttachmentStore) BeginCompletedUsage(ctx context.Context, h *History, cursor HistoryCursor, job JobKey) (CompletedUsageWindow, bool) {
	var none CompletedUsageWindow
	if h == nil || cursor.Revision <= 0 || !validKey(job.Attempt) || job.JobID <= 0 {
		return none, false
	}
	unlock := s.tryCollectionLocks(ctx, job, h)
	if unlock == nil {
		return none, false
	}
	defer unlock()
	_, cp := h.currentCheckpoint(historyScope(job.Attempt))
	if cp.Cursor != cursor {
		return none, false
	}
	now := s.now().UTC()
	row, _, member := s.currentJobClock(ctx, job, now)
	// A valid closing bracket can follow completion by at most one gap,
	// then remain fresh for strictly less than one more gap.
	if !member || row.Phase != Completed || !validTime(row.StartedAt) || !row.CompletedAt.After(row.StartedAt) ||
		row.CompletedAt.Sub(row.StartedAt) > MaxJobResourceWindow || row.CompletedAt.After(row.ObservedAt) ||
		!now.Before(row.CompletedAt.Add(2*MaxJobResourceGap)) {
		return none, false
	}
	return CompletedUsageWindow{s, s.currentFD, h.currentFD, job, row.StartedAt.UTC(), row.CompletedAt.UTC(), now}, true
}

func (w CompletedUsageWindow) Endpoints() (time.Time, time.Time) { return w.start, w.end }

// Current is a local revision check during collection. Attach repeats it
// while holding both locks and revalidates retained membership and custody.
func (w CompletedUsageWindow) Current(ctx context.Context) bool {
	unlock := w.store.tryCollectionLocks(ctx, w.job, nil)
	if unlock == nil {
		return false
	}
	defer unlock()
	return w.currentLocked() && historyActive(ctx)
}

func (w CompletedUsageWindow) currentLocked() bool {
	s := w.store
	now := s.now().UTC()
	return !now.Before(w.issued) && now.Before(w.end.Add(2*MaxJobResourceGap)) && s.currentFD != nil && !s.failed && w.usageVersion == s.currentFD &&
		s.history.currentFD != nil && !s.history.failed && w.historyVersion == s.history.currentFD
}

// Attach rechecks the source's short-lived local receipts under the store
// locks. current must only inspect local proof, and must never enter a store.
func (w CompletedUsageWindow) Attach(ctx context.Context, claim JobAttributionClaim, set VerifiedUsageSampleSet, current func() bool) error {
	if len(set.Samples) > MaxJobResourceSamples || current == nil || w.job != claim.Job || !w.start.Equal(set.Scope.StartedAt) || !w.end.Equal(set.Scope.EndAt) ||
		claim.Execution.State != StockJobObservedEnded || !w.end.Equal(claim.Execution.EndedAt) {
		return ErrUsageAttachment
	}
	unlock := w.store.tryCollectionLocks(ctx, w.job, nil)
	if unlock == nil {
		return ErrUsageAttachment
	}
	defer unlock()
	return w.store.attachLocked(ctx, claim, set, &w, current)
}
