package cihealth

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func completedWindowFixture(t *testing.T) (*attachmentFixture, HistoryCursor) {
	t.Helper()
	f := newAttachmentFixture(t)
	// Drive store contention and revision changes explicitly in these tests.
	close(f.store.stop)
	<-f.store.done
	f.store.stop = nil
	f.claim.Execution.State = StockJobObservedEnded
	f.claim.Execution.EndedAt = f.at
	f.set.Execution = &f.claim.Execution
	f.set.Scope.EndAt = f.at
	f.set.Closing.ObservedAt = f.at
	f.set.Samples[len(f.set.Samples)-1].At = f.at
	cursor, err := f.in.History.Cursor(t.Context(), historyScope(f.claim.Job.Attempt))
	if err != nil {
		t.Fatal(err)
	}
	return f, cursor
}

func TestCompletedWindowRequiresSameHistoryAndEnrollmentThroughoutCapture(t *testing.T) {
	f, cursor := completedWindowFixture(t)
	ctx := t.Context()
	key, h := f.claim.Job, f.in.History
	begin := func() (CompletedUsageWindow, bool) { return f.store.BeginCompletedUsage(ctx, h, cursor, key) }
	if _, ok := begin(); ok {
		t.Fatal("unenrolled job")
	}
	if err := f.store.SetEnrolled(ctx, usageScopeOf(key), true); err != nil {
		t.Fatal(err)
	}
	other := key
	other.Attempt.Attempt++
	for _, bad := range []struct {
		history *History
		cursor  HistoryCursor
		job     JobKey
	}{
		{&History{}, cursor, key}, {h, HistoryCursor{Revision: cursor.Revision + 1}, key}, {h, cursor, other},
	} {
		if _, ok := f.store.BeginCompletedUsage(ctx, bad.history, bad.cursor, bad.job); ok {
			t.Fatal("foreign cursor, history or sibling attempt")
		}
	}
	w, ok := begin()
	if !ok || !w.Current(ctx) {
		t.Fatal("exact completed window unavailable")
	}
	start, end := w.Endpoints()
	if !start.Equal(f.claim.Execution.StartedAt) || !end.Equal(f.at) {
		t.Fatal("incorrect retained terminal endpoints")
	}
	if err := f.store.SetEnrolled(ctx, usageScopeOf(key), false); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetEnrolled(ctx, usageScopeOf(key), true); err != nil {
		t.Fatal(err)
	}
	check := func() bool { return true }
	assertRejected := func(w CompletedUsageWindow) {
		t.Helper()
		if w.Current(ctx) || !errors.Is(w.Attach(ctx, f.claim, f.set, check), ErrUsageAttachment) {
			t.Fatal("superseded proof accepted")
		}
		if _, found, err := f.store.Read(ctx, key); found || err != nil {
			t.Fatalf("refusal stored data: found %v err %v", found, err)
		}
	}
	assertRejected(w)
	w, ok = begin()
	if !ok {
		t.Fatal("new enrollment unavailable")
	}
	f.offset.Store(int64(time.Second))
	f.membership(t, false)
	f.offset.Store(int64(2 * time.Second))
	f.membership(t, true)
	assertRejected(w)
	if _, ok := begin(); ok {
		t.Fatal("old cursor restored after intervening revision")
	}
}

func TestCompletedWindowAttachRevalidatesAtPublicationAndPreservesStore(t *testing.T) {
	ctx := t.Context()
	f, cursor := completedWindowFixture(t)
	h, key := f.in.History, f.claim.Job
	if err := f.store.SetEnrolled(ctx, usageScopeOf(key), true); err != nil {
		t.Fatal(err)
	}
	w, ok := f.store.BeginCompletedUsage(ctx, h, cursor, key)
	if !ok {
		t.Fatal("no completed window")
	}
	check := func() bool { return true }
	bad := f.set
	bad.Scope.EndAt = bad.Scope.EndAt.Add(-time.Second)
	if err := w.Attach(ctx, f.claim, bad, check); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("partial job window accepted", err)
	}
	stop, cancel := context.WithCancel(ctx)
	cancel()
	if err := w.Attach(stop, f.claim, f.set, check); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("canceled collection accepted", err)
	}
	// Expire the source precisely after the complete stage has been synced
	// and before rename. The committed sidecar must remain usable.
	before, err := f.in.Root.ReadFile(usageAttachmentFile)
	if err != nil {
		t.Fatal(err)
	}
	current := func() bool {
		_, err := f.in.Root.Stat(usageAttachmentStage)
		return errors.Is(err, os.ErrNotExist)
	}
	if err := w.Attach(ctx, f.claim, f.set, current); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("expired source reached publication", err)
	}
	after, err := f.in.Root.ReadFile(usageAttachmentFile)
	if err != nil || string(after) != string(before) || !w.Current(ctx) {
		t.Fatal("refusal damaged retained state", err)
	}
	if _, err = f.in.Root.Stat(usageAttachmentStage); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unpublished stage remains", err)
	}
	if err := w.Attach(ctx, f.claim, f.set, check); err != nil {
		t.Fatal("verified retry refused", err)
	}
	if w.Current(ctx) {
		t.Fatal("attachment did not invalidate prior revision")
	}
	a, found, err := f.store.Read(ctx, key)
	if err != nil || !found || a.Evidence.State != VerifiedUsageVerified || a.Evidence.Measured == nil || !a.EvidenceAsOf.Equal(f.at) {
		t.Fatalf("missing exact measured record: %v %v", found, err)
	}
	w, ok = f.store.BeginCompletedUsage(ctx, h, cursor, key)
	if !ok || w.Attach(ctx, f.claim, f.set, check) != nil {
		t.Fatal("fresh idempotent replay failed")
	}
	f.offset.Store(int64(2 * MaxJobResourceGap))
	if _, ok = f.store.BeginCompletedUsage(ctx, h, cursor, key); ok {
		t.Fatal("unbounded completed-job lookback")
	}
	if err := w.Attach(ctx, f.claim, f.set, check); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("stale proof replayed")
	}
}

func TestCompletedCollectionNeverWaitsForStoreLocks(t *testing.T) {
	ctx := t.Context()
	f, cursor := completedWindowFixture(t)
	h, job := f.in.History, f.claim.Job
	if err := f.store.SetEnrolled(ctx, usageScopeOf(job), true); err != nil {
		t.Fatal(err)
	}
	begin := func() (CompletedUsageWindow, bool) { return f.store.BeginCompletedUsage(ctx, h, cursor, job) }
	w, ok := begin()
	if !ok {
		t.Fatal("expected eligible window")
	}
	for _, lock := range []struct {
		name string
		mu   *sync.Mutex
	}{{"sidecar", &f.store.mu}, {"history", &h.mu}} {
		for _, call := range []struct {
			name    string
			refused func() bool
		}{
			{"begin", func() bool { _, ok := begin(); return !ok }},
			{"current", func() bool { return !w.Current(ctx) }},
			{"attach", func() bool {
				return errors.Is(w.Attach(ctx, f.claim, f.set, func() bool { return true }), ErrUsageAttachment)
			}},
		} {
			t.Run(lock.name+"/"+call.name, func(t *testing.T) {
				// The lock stays unavailable throughout the call. The watchdog
				// bounds a regression that waits indefinitely for an unlock.
				lock.mu.Lock()
				done := make(chan bool, 1)
				go func() { done <- call.refused() }()
				select {
				case refused := <-done:
					lock.mu.Unlock()
					if !refused {
						t.Fatal("accepted collection while locked")
					}
				case <-time.After(3 * time.Second):
					lock.mu.Unlock()
					<-done
					t.Fatal("collection waited for a contended store lock")
				}
				if !w.Current(ctx) {
					t.Fatal("lock or revision leaked after refusal")
				}
			})
		}
	}
	if _, found, err := f.store.Read(ctx, job); err != nil || found {
		t.Fatal("contended collection wrote an attachment", err)
	}
}
