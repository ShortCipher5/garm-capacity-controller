package cihealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type attachmentFixture struct {
	claim  JobAttributionClaim
	set    VerifiedUsageSampleSet
	at     time.Time
	offset atomic.Int64
	in     UsageAttachmentInput
	store  *UsageAttachmentStore
}

func (f *attachmentFixture) clock() time.Time { return f.at.Add(time.Duration(f.offset.Load())) }

func (f *attachmentFixture) open(t *testing.T, init bool) {
	t.Helper()
	f.in.Initialize = init
	s, err := openUsageAttachments(t.Context(), f.in, fixtureFullMatrix(), f.clock, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	f.store = s
	t.Cleanup(func() { s.Close() })
}

func (f *attachmentFixture) membership(t *testing.T, member bool) {
	t.Helper()
	h := f.in.History
	scope := historyScope(f.claim.Job.Attempt)
	cursor, err := h.Cursor(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	at := f.clock()
	a := snapAttempt(scope, f.claim.Job.Attempt.RunID, f.claim.Job.Attempt.Attempt, at)
	a.Jobs = nil
	if member {
		a.Jobs = []JobFacts{{Key: f.claim.Job, Clock: Clock{Phase: Completed, StartedAt: f.set.Scope.StartedAt, CompletedAt: at, ObservedAt: at}}}
	}
	if err := h.Apply(t.Context(), scope, cursor, HistoryCursor{Revision: cursor.Revision + 1}, []AttemptFacts{a}, at); err != nil {
		t.Fatal(err)
	}
}

func newAttachmentFixture(t *testing.T) *attachmentFixture {
	t.Helper()
	claim, at := attributionFixtureClaim()
	f := &attachmentFixture{claim: claim, at: at, set: usageFixtureSet(claim, at, MaxJobResourceGap)}
	for i := range f.set.Samples {
		f.set.Samples[i].DiskReadBytes = knownResource(9)
		f.set.Samples[i].DiskUsedBytes = knownResource(uint64(i + 10))
	}
	_, dir := snapshotRoot(t)
	_, histDir := snapshotRoot(t)
	f.in = UsageAttachmentInput{Enabled: true, Root: dir, History: snapshotOpenMode(t, histDir, true, true)}
	f.open(t, true)
	f.membership(t, true)
	return f
}

func (f *attachmentFixture) attach(t *testing.T) UsageAttachment {
	t.Helper()
	if err := f.store.SetEnrolled(t.Context(), usageScopeOf(f.claim.Job), true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Attach(t.Context(), f.claim, f.set); err != nil {
		t.Fatal(err)
	}
	a, found, err := f.store.Read(t.Context(), f.claim.Job)
	if err != nil || !found {
		t.Fatalf("missing attachment: %v %v", found, err)
	}
	return a
}

func TestUsageAttachmentBuildsFromVerifiedEvidenceAndReplays(t *testing.T) {
	f := newAttachmentFixture(t)
	if err := f.store.Attach(t.Context(), f.claim, f.set); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("missing enrollment", err)
	}
	if s, err := OpenUsageAttachments(t.Context(), f.in); !errors.Is(err, ErrUsageAttachment) || s != nil {
		t.Fatal("stock source accepted", err)
	}
	f.offset.Store(int64(time.Second))
	first := f.attach(t)
	if first.Evidence.Measured.Disk.Read.Bytes != "0" || first.Evidence.Measured.Disk.Write != nil ||
		!first.AttachedAt.Equal(f.clock()) || !first.EvidenceAsOf.Equal(f.at) {
		t.Fatalf("bad measurement/time: %+v", first)
	}
	digest := first.Integrity
	// Neither source mutation nor mutation of a returned value reaches retained state.
	f.set.Samples[1].DiskReadBytes.Value++
	first.Evidence.Measured.Disk.Read.Bytes = "999"
	again, _, err := f.store.Read(t.Context(), f.claim.Job)
	if err != nil || again.Integrity != digest || again.Evidence.Measured.Disk.Read.Bytes != "0" {
		t.Fatal("retained alias", err)
	}
	f.set.Samples[1].DiskReadBytes.Value--
	f.offset.Store(int64(2 * MaxJobResourceGap))
	if err := f.store.Attach(t.Context(), f.claim, f.set); err != nil {
		t.Fatal("historical exact replay", err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, false)
	again, found, err := f.store.Read(t.Context(), f.claim.Job)
	if err != nil || !found || again.Integrity != digest || !again.AttachedAt.Equal(first.AttachedAt) || !again.EvidenceAsOf.Equal(f.at) {
		t.Fatal("reopen refreshed facts", err)
	}

	f.set.Samples[1].RAMBytes.Value++
	if err := f.store.Attach(t.Context(), f.claim, f.set); !errors.Is(err, ErrUsageAttachmentConflict) {
		t.Fatal("different evidence for same observation", err)
	}
	other := f.claim
	other.Origin = VerifiedProviderOrigin{}
	if err := f.store.Attach(t.Context(), other, f.set); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("unproven origin", err)
	}
	other = f.claim
	other.Incarnation.Generation = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	other.Origin.Incarnation = other.Incarnation
	if err := f.store.Attach(t.Context(), other, f.set); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("samples for prior runner", err)
	}
}

func TestUsageAttachmentCurrentMembershipAndRevocation(t *testing.T) {
	f := newAttachmentFixture(t)
	first := f.attach(t)
	if err := f.store.SetEnrolled(t.Context(), usageScopeOf(f.claim.Job), false); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.store.Read(t.Context(), f.claim.Job); !errors.Is(err, ErrUsageAttachment) || found {
		t.Fatal("revoked scope served", err)
	}
	if err := f.store.SetEnrolled(t.Context(), usageScopeOf(f.claim.Job), true); err != nil {
		t.Fatal(err)
	}
	f.offset.Store(int64(time.Second))
	f.membership(t, false)
	// A superseded timing version can still contain the job. Only the latest row counts.
	if _, found, err := f.store.Read(t.Context(), f.claim.Job); !errors.Is(err, ErrUsageAttachment) || found {
		t.Fatal("removed job served", err)
	}
	if err := f.store.Attach(t.Context(), f.claim, f.set); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("replay masked revocation", err)
	}
	sibling := f.claim.Job
	sibling.JobID++
	if _, found, err := f.store.Read(t.Context(), sibling); !errors.Is(err, ErrUsageAttachment) || found {
		t.Fatal("sibling served", err)
	}
	f.offset.Store(int64(2 * time.Second))
	f.membership(t, true)
	a, found, err := f.store.Read(t.Context(), f.claim.Job)
	if !found || err != nil || a.Integrity != first.Integrity {
		t.Fatal("membership restoration changed evidence", err)
	}
	if err := f.in.History.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.store.Read(t.Context(), f.claim.Job); !errors.Is(err, ErrUsageAttachment) || found {
		t.Fatal("closed membership store served old result", err)
	}
}

func TestUsageAttachmentPhysicallyPrunesAtCutoff(t *testing.T) {
	for _, mode := range []string{"idle", "restart"} {
		t.Run(mode, func(t *testing.T) {
			f := newAttachmentFixture(t)
			a := f.attach(t)
			f.offset.Store(int64(usageAttachmentRetention - time.Nanosecond))
			raw, err := f.in.Root.ReadFile(usageAttachmentFile)
			if err != nil || !bytes.Contains(raw, []byte(a.Integrity)) {
				t.Fatal("removed before cutoff", err)
			}
			if mode == "restart" {
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			f.offset.Store(int64(usageAttachmentRetention))
			if mode == "restart" {
				f.open(t, false)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				raw, err := f.in.Root.ReadFile(usageAttachmentFile)
				if err != nil {
					t.Fatal(err)
				}
				var state usageAttachmentState
				if err := json.Unmarshal(raw, &state); err != nil {
					t.Fatal(err)
				}
				if len(state.Attachments) == 0 {
					break
				}
				if mode == "restart" || time.Now().After(deadline) {
					t.Fatal("expired record physically retained")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestUsageAttachmentReopenRejectsCorruptCanonicalRecords(t *testing.T) {
	f := newAttachmentFixture(t)
	original := f.attach(t)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	path := f.in.Root
	raw, err := path.ReadFile(usageAttachmentFile)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*UsageAttachment){
		"runner unbound":       func(a *UsageAttachment) { a.RunnerDigest = "" },
		"wrong evidence clock": func(a *UsageAttachment) { a.EvidenceAsOf = a.EvidenceAsOf.Add(-time.Second) },
		"future attachment":    func(a *UsageAttachment) { a.AttachedAt = a.AttachedAt.Add(time.Minute) },
		"missing wire clock":   func(a *UsageAttachment) { a.Evidence.AsOf = nil },
		"impossible gauge":     func(a *UsageAttachment) { a.Evidence.Measured.Disk.Used.PeakAt = a.WindowStart.Add(-time.Second) },
		"bad window":           func(a *UsageAttachment) { a.Evidence.Measured.Window = "1" },
		"bad counter":          func(a *UsageAttachment) { a.Evidence.Measured.Disk.Read.Bytes = "01" },
		"bad coverage":         func(a *UsageAttachment) { a.Evidence.Measured.Disk.Read.Complete = false },
		"impossible CPU":       func(a *UsageAttachment) { a.Evidence.Measured.CPU.AverageMilliCPU = "99999" },
		"invalid RAM":          func(a *UsageAttachment) { a.Evidence.Measured.RAM.AverageMilliCPU = "5" },
		"invented state":       func(a *UsageAttachment) { a.Evidence.State = "complete" },
		"unknown omission": func(a *UsageAttachment) {
			a.Evidence.Measured = nil
			a.Evidence.State = VerifiedUsageUnavailable
			a.Evidence.Reason = "arbitrary"
		},
		"sibling with old checksum": func(a *UsageAttachment) { a.Job.JobID++ },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var state usageAttachmentState
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			a := &state.Attachments[0]
			mutate(a)
			if name != "sibling with old checksum" {
				a.Integrity = a.checksum()
			}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := path.WriteFile(usageAttachmentFile, data, 0600); err != nil {
				t.Fatal(err)
			}
			f.in.Initialize = false
			s, err := openUsageAttachments(t.Context(), f.in, fixtureFullMatrix(), f.clock, time.Minute)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrUsageAttachment) {
				t.Fatalf("reopened corrupt record %s, original %s", name, original.Integrity)
			}
		})
	}
}

func TestUsageAttachmentReopenRefusesWithdrawnCapability(t *testing.T) {
	for _, withdrawn := range []AttributionFact{FactRAMCounters, FactProcessEpoch, FactProviderOriginJoin} {
		t.Run(string(withdrawn), func(t *testing.T) {
			f := newAttachmentFixture(t)
			f.attach(t)
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			f.in.Initialize = false
			matrix := fixtureFullMatrix()
			for i := range matrix.Sources {
				source := &matrix.Sources[i]
				proven := source.Proves[:0]
				for _, fact := range source.Proves {
					if fact != withdrawn {
						proven = append(proven, fact)
					}
				}
				source.Proves = proven
			}
			store, err := openUsageAttachments(t.Context(), f.in, matrix, f.clock, time.Minute)
			if store != nil || !errors.Is(err, ErrUsageAttachment) {
				t.Fatalf("reopen accepted retained measurements without their source capability: %v", err)
			}
		})
	}
}

func TestUsageAttachmentOmissionAndStaleNeverBecomeNumbers(t *testing.T) {
	for _, state := range []VerifiedUsageState{VerifiedUsageUnavailable, VerifiedUsageStale} {
		t.Run(string(state), func(t *testing.T) {
			f := newAttachmentFixture(t)
			if state == VerifiedUsageStale {
				f.offset.Store(int64(2 * MaxJobResourceGap))
			} else {
				f.set.Samples = nil
			}
			a := f.attach(t)
			if a.Evidence.State != state || a.Evidence.Measured != nil || a.Evidence.Reason == "" {
				t.Fatalf("unexpected omission: %+v", a)
			}
		})
	}
}

func TestUsageAttachmentDisabledAndExclusive(t *testing.T) {
	s, err := OpenUsageAttachments(context.Background(), UsageAttachmentInput{})
	if err != nil || s != nil {
		t.Fatal("disabled sidecar opened", err)
	}
	f := newAttachmentFixture(t)
	f.in.Initialize = false
	if second, err := openUsageAttachments(t.Context(), f.in, fixtureFullMatrix(), f.clock, time.Minute); !errors.Is(err, ErrUsageAttachment) || second != nil {
		t.Fatal("second owner admitted", err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.in.Root.WriteFile(usageAttachmentStage, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if second, err := openUsageAttachments(t.Context(), f.in, fixtureFullMatrix(), f.clock, time.Minute); !errors.Is(err, ErrUsageAttachment) || second != nil {
		t.Fatal("pending publication reopened", err)
	}
}

func TestUsageAttachmentUninitializedStoreFailsClosed(t *testing.T) {
	f := newAttachmentFixture(t)
	var zero UsageAttachmentStore
	if err := zero.Attach(t.Context(), f.claim, f.set); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal(err)
	}
	if _, found, err := zero.Read(t.Context(), f.claim.Job); found || !errors.Is(err, ErrUsageAttachment) {
		t.Fatal(found, err)
	}
}

func TestUsageAttachmentReopensWithStandaloneRAMGauge(t *testing.T) {
	f := newAttachmentFixture(t)
	f.set.Samples[0].RAMBytes = ResourceValue{}
	f.set.Samples[1].RAMBytes = knownResource(0)
	f.set.Samples[2].RAMBytes = ResourceValue{}
	original := f.attach(t)
	if original.Evidence.State != VerifiedUsagePartial {
		t.Fatalf("standalone gauge cannot establish full coverage: %s", original.Evidence.State)
	}
	wire, err := json.Marshal(original.Evidence)
	if err != nil || !bytes.Contains(wire, []byte(`"ram":{"sample_peak_bytes":"0","gauge_samples":1}`)) {
		t.Fatalf("standalone gauge lost its measured zero or acquired coverage: %s %v", wire, err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, false)
	retained, found, err := f.store.Read(t.Context(), f.claim.Job)
	if !found || err != nil || retained.Integrity != original.Integrity {
		t.Fatalf("gauged record changed after reopen: %v %v", found, err)
	}
}

func TestUsageAttachmentRejectsNewerEvidenceForDifferentWindowOrRunner(t *testing.T) {
	for _, change := range []string{"window", "runner", "same window and runner"} {
		t.Run(change, func(t *testing.T) {
			f := newAttachmentFixture(t)
			previous := f.attach(t)
			f.offset.Store(int64(time.Second))
			claim := f.claim
			claim.Row.ObservedAt = f.clock()
			claim.Origin.Row = claim.Row
			claim.Execution.ObservedAt = f.clock()
			if change == "runner" {
				claim.Incarnation.Generation = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
				claim.Origin.Incarnation = claim.Incarnation
			}
			fresh := usageFixtureSet(claim, f.clock(), MaxJobResourceGap)
			if change == "window" {
				fresh.Scope.EndAt = fresh.Scope.EndAt.Add(time.Second)
				fresh.Closing.ObservedAt = fresh.Scope.EndAt
			}
			r := BuildVerifiedJobUsage(fixtureFullMatrix(), claim, fresh, f.clock())
			if r.Measured == nil || !r.AsOf.After(previous.EvidenceAsOf) {
				t.Fatal("fixture must supply a coherent newer observation")
			}
			err := f.store.Attach(t.Context(), claim, fresh)
			stored, found, readErr := f.store.Read(t.Context(), claim.Job)
			if !found || readErr != nil {
				t.Fatal("lost original observation", readErr)
			}
			if change == "same window and runner" {
				if err != nil || !stored.EvidenceAsOf.Equal(r.AsOf) {
					t.Fatal("same-window update lost", err)
				}
			} else if !errors.Is(err, ErrUsageAttachmentConflict) || stored.Integrity != previous.Integrity {
				t.Fatal("newer clock displaced a different window or runner", err)
			}
		})
	}
}

func TestUsageAttachmentCancellationAndTimingHistoryCompatibility(t *testing.T) {
	f := newAttachmentFixture(t)
	readTiming := func() []byte {
		raw, err := f.in.History.root.ReadFile(historyFile)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := readTiming()
	a := f.attach(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.store.Attach(ctx, f.claim, f.set); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("canceled replay", err)
	}
	if _, found, err := f.store.Read(ctx, f.claim.Job); found || !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("canceled read", err)
	}
	if err := f.store.SetEnrolled(ctx, usageScopeOf(f.claim.Job), false); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("canceled revocation", err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, false)
	stored, found, err := f.store.Read(t.Context(), f.claim.Job)
	if !found || err != nil || a.Integrity != stored.Integrity {
		t.Fatal("canceled operation changed attachment", err)
	}
	if !bytes.Equal(before, readTiming()) {
		t.Fatal("sidecar rewrote independently owned timing history")
	}
	if s, err := OpenUsageAttachments(t.Context(), UsageAttachmentInput{}); err != nil || s != nil {
		t.Fatal("ordinary startup opened the sidecar", err)
	}
}

func TestUsageAttachmentChecksLatestTimingEndpointsOnReadAndReplay(t *testing.T) {
	f := newAttachmentFixture(t)
	f.attach(t)
	f.offset.Store(int64(time.Second))
	history := f.in.History
	scope := historyScope(f.claim.Job.Attempt)
	row := snapAttempt(scope, f.claim.Job.Attempt.RunID, f.claim.Job.Attempt.Attempt, f.clock())
	row.Jobs = []JobFacts{{Key: f.claim.Job, Clock: Clock{
		Phase: Completed, StartedAt: f.set.Scope.StartedAt, CompletedAt: f.set.Scope.EndAt.Add(-time.Second), ObservedAt: f.clock(),
	}}}
	cursor, err := history.Cursor(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Apply(t.Context(), scope, cursor, HistoryCursor{Revision: cursor.Revision + 1}, []AttemptFacts{row}, f.clock()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.store.Read(t.Context(), f.claim.Job); !errors.Is(err, ErrUsageAttachmentConflict) || found {
		t.Fatal("latest completion contradicts retained usage", err)
	}
	if err := f.store.Attach(t.Context(), f.claim, f.set); !errors.Is(err, ErrUsageAttachmentConflict) {
		t.Fatal("replay masked terminal cutoff", err)
	}
}
