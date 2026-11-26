package cihealth

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestUsageDisplayPreservesEvidenceAndRevokesOnRevision(t *testing.T) {
	ctx := t.Context()
	f := newAttachmentFixture(t)
	a := f.attach(t)
	// Drive clock and revision transitions explicitly in this fixture.
	close(f.store.stop)
	<-f.store.done
	f.store.stop = nil
	key, h := f.claim.Job, f.in.History
	cursor, err := h.Cursor(ctx, historyScope(key.Attempt))
	if err != nil {
		t.Fatal(err)
	}
	// The retained terminal clock is later than the sample window. A live
	// running receipt supports the prefix before that independently known end.
	live := Clock{Phase: Running, StartedAt: a.WindowStart, ObservedAt: a.WindowEnd}
	get := func() (JobUsageDisplay, bool) { return f.store.ObserveDisplay(ctx, h, cursor, key, live, f.clock()) }
	v, ok := get()
	if !ok || !v.Current() {
		t.Fatal("eligible observation missing")
	}
	want, _ := json.Marshal(a.Evidence)
	wire := v.Wire()
	if !bytes.Equal(wire, want) || a.Evidence.Measured.Disk.Read.Bytes != "0" || a.Evidence.Measured.Disk.Write != nil || a.Evidence.Measured.CPU == nil || a.Evidence.Measured.RAM == nil {
		t.Fatalf("incorrect coverage: %s", wire)
	}
	wire[0] = 'x'
	if !bytes.Equal(v.Wire(), want) {
		t.Fatal("display exposed mutable evidence")
	}
	other := key
	other.Attempt.Attempt++
	if _, ok := f.store.ObserveDisplay(ctx, h, cursor, other, live, f.clock()); ok {
		t.Fatal("sibling attempt")
	}
	if _, ok := f.store.ObserveDisplay(ctx, &History{}, cursor, key, live, f.clock()); ok {
		t.Fatal("different history owner")
	}
	if _, ok := f.store.ObserveDisplay(ctx, h, HistoryCursor{Revision: cursor.Revision + 1}, key, live, f.clock()); ok {
		t.Fatal("wrong revision")
	}
	stop, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok := f.store.ObserveDisplay(stop, h, cursor, key, live, f.clock()); ok {
		t.Fatal("canceled read")
	}
	if _, ok := f.store.ObserveDisplay(ctx, h, cursor, key, live, a.EvidenceAsOf.Add(-time.Nanosecond)); ok {
		t.Fatal("future evidence in earlier cycle")
	}
	terminal := live
	terminal.Phase = Completed
	terminal.CompletedAt = a.WindowEnd
	if _, ok := f.store.ObserveDisplay(ctx, h, cursor, key, terminal, f.clock()); ok {
		t.Fatal("contradiction with retained terminal time")
	}
	if err := f.store.SetEnrolled(ctx, usageScopeOf(key), false); err != nil {
		t.Fatal(err)
	}
	if v.Current() || v.Wire() != nil {
		t.Fatal("revoked enrollment still visible")
	}
	if err := f.store.SetEnrolled(ctx, usageScopeOf(key), true); err != nil {
		t.Fatal(err)
	}
	if v.Current() {
		t.Fatal("re-enrollment resurrected old projection")
	}
	v, ok = get()
	if !ok {
		t.Fatal("new revision missing")
	}
	f.offset.Store(int64(a.WindowEnd.Add(MaxJobResourceGap).Sub(f.at)))
	if v.Current() {
		t.Fatal("running evidence survived freshness cutoff")
	}
	stale, ok := get()
	var omitted Wire
	if !ok || json.Unmarshal(stale.Wire(), &omitted) != nil || omitted.State != VerifiedUsageStale || omitted.Measured != nil || !omitted.AsOf.Equal(a.EvidenceAsOf) {
		t.Fatal("expired evidence did not preserve original as-of")
	}
	f.membership(t, false)
	if stale.Current() || stale.Wire() != nil {
		t.Fatal("timing membership revocation still visible")
	}
}

// RetainedDisplayFixture uses a synthetic provider origin and process epoch
// for an external-package test of the complete owner dashboard path. This
// helper and its capability matrix are compiled only into test binaries.
func RetainedDisplayFixture(t *testing.T, attempt AttemptFacts) (*UsageAttachmentStore, *History, HistoryCursor) {
	t.Helper()
	j := attempt.Jobs[0]
	claim, _ := attributionFixtureClaim()
	end, start := j.Clock.CompletedAt, j.Clock.StartedAt
	claim.Job = j.Key
	claim.Enrolled.GitHubSource, claim.Enrolled.GitHubRepositoryID = j.Key.Attempt.Source, j.Key.Attempt.RepositoryID
	claim.Execution.Job, claim.Execution.StartedAt, claim.Execution.EndedAt, claim.Execution.ObservedAt, claim.Execution.State = j.Key, start, end, end, StockJobObservedEnded
	claim.Row.ObservedAt = end
	claim.Incarnation.StartedAt = start.Add(-time.Minute)
	claim.Origin.Row, claim.Origin.Incarnation, claim.Origin.InstanceCreatedAt, claim.Origin.AllocationObservedAt = claim.Row, claim.Incarnation, claim.Incarnation.StartedAt, claim.Incarnation.StartedAt
	set := usageFixtureSet(claim, end, 3*time.Second)
	for i := range set.Samples {
		set.Samples[i].DiskReadBytes = knownResource(0)
	}
	_, historyDir := snapshotRoot(t)
	h := snapshotOpenMode(t, historyDir, true, true)
	scope := historyScope(attempt.Key)
	cursor := HistoryCursor{Revision: 1}
	if err := h.Apply(t.Context(), scope, HistoryCursor{}, cursor, []AttemptFacts{attempt}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, root := snapshotRoot(t)
	s, err := openUsageAttachments(t.Context(), UsageAttachmentInput{Enabled: true, Root: root, Initialize: true, History: h}, fixtureFullMatrix(), time.Now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetEnrolled(t.Context(), usageScopeOf(j.Key), true); err != nil {
		t.Fatal(err)
	}
	if err := s.Attach(t.Context(), claim, set); err != nil {
		t.Fatal(err)
	}
	close(s.stop)
	<-s.done
	s.stop = nil
	return s, h, cursor
}

func TestUsageDisplayRejectsContradictoryRetainedMembershipPhases(t *testing.T) {
	f := newAttachmentFixture(t)
	a := f.attach(t)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	start, end := a.WindowStart, a.WindowEnd
	for _, tc := range []struct {
		name               string
		row                Clock
		running, completed bool
	}{
		{"skipped", Clock{Phase: Skipped, ObservedAt: end}, false, false},
		{"late queue", Clock{Phase: Queued, ObservedAt: start.Add(time.Second)}, false, false},
		{"earlier queue", Clock{Phase: Queued, ObservedAt: start.Add(-time.Second)}, true, true},
		{"terminal already observed", Clock{Phase: Completed, ObservedAt: end.Add(-time.Second)}, false, false},
		{"terminal at window end", Clock{Phase: Completed, ObservedAt: end}, false, true},
		{"terminal later", Clock{Phase: Completed, ObservedAt: end.Add(time.Second)}, true, true},
		{"running at window end", Clock{Phase: Running, ObservedAt: end}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, dir := snapshotRoot(t)
			h := snapshotOpenMode(t, dir, true, true)
			scope := historyScope(a.Job.Attempt)
			cursor := HistoryCursor{Revision: 1}
			row := AttemptFacts{Key: a.Job.Attempt, AsOf: f.at, Clock: Clock{Phase: Unknown, ObservedAt: f.at}, Jobs: []JobFacts{{Key: a.Job, Clock: tc.row}}}
			if err := h.Apply(t.Context(), scope, HistoryCursor{}, cursor, []AttemptFacts{row}, f.at); err != nil {
				t.Fatal(err)
			}
			f.in.History = h
			f.open(t, false)
			close(f.store.stop)
			<-f.store.done
			f.store.stop = nil
			for _, phase := range []Phase{Running, Completed} {
				live := Clock{Phase: phase, StartedAt: start, ObservedAt: end}
				want := tc.running
				if phase == Completed {
					live.CompletedAt = end
					want = tc.completed
				}
				view, ok := f.store.ObserveDisplay(t.Context(), h, cursor, a.Job, live, f.at)
				if ok != want || view.Current() != want {
					t.Fatalf("live phase %v: got %v, want %v", phase, ok, want)
				}
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
