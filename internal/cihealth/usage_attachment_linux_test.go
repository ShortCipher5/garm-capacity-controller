package cihealth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func usageAttachmentFixtureRoot(t *testing.T) *os.Root {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func usageAttachmentFixtureJob(jobID int64) JobKey {
	return JobKey{Attempt: AttemptKey{Source: "fixture", RepositoryID: 7, WorkflowID: 11, RunID: 13, Attempt: 2}, JobID: jobID}
}

func usageAttachmentFixtureRecord(job JobKey, asOf time.Time) UsageAttachment {
	at := asOf.UTC()
	return UsageAttachment{
		Job: job,
		Evidence: Wire{
			State: VerifiedUsageVerified,
			AsOf:  &at,
			Measured: &MeasuredWire{
				Samples: 3, Window: "6000000000",
				CPU:  &MetricWire{CoveredNanoseconds: "6000000000", Complete: true, AverageMilliCPU: "1000", PeakMilliCPU: "1250"},
				Disk: DiskWire{State: VerifiedUsageVerified, Version: DiskWireVersion, Read: &DiskCounterWire{Bytes: "0", CoveredNanoseconds: "6000000000", Complete: true}},
			},
		},
		EvidenceAsOf: asOf,
		AttachedAt:   asOf,
	}
}

func openUsageAttachmentFixture(t *testing.T, root *os.Root, initialize bool) *UsageAttachmentStore {
	t.Helper()
	store, err := OpenUsageAttachments(context.Background(), UsageAttachmentInput{Enabled: true, Root: root, Initialize: initialize})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestUsageAttachmentRequiresEnrollmentBeforeStorageLookup(t *testing.T) {
	root := usageAttachmentFixtureRoot(t)
	store := openUsageAttachmentFixture(t, root, true)
	now := time.Now().UTC()
	job := usageAttachmentFixtureJob(5)
	if err := store.Attach(context.Background(), usageAttachmentFixtureRecord(job, now), now); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("attach without enrollment must fail closed", err)
	}
	if _, _, err := store.Read(context.Background(), job, now); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("read without enrollment must fail closed", err)
	}
	scope := UsageAttachmentScope{Source: "fixture", RepositoryID: 7}
	if err := store.SetEnrolled(context.Background(), scope, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Attach(context.Background(), usageAttachmentFixtureRecord(job, now), now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Read(context.Background(), job, now); err != nil || !found {
		t.Fatal("enrolled attach/read must succeed", err)
	}
	// Revocation immediately prevents the historical attachment from being
	// shown; the retained record is not rewritten or deleted.
	if err := store.SetEnrolled(context.Background(), scope, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read(context.Background(), job, now); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("revoked scope must not serve historical attachments", err)
	}
	if err := store.SetEnrolled(context.Background(), scope, true); err != nil {
		t.Fatal(err)
	}
	retained, found, err := store.Read(context.Background(), job, now)
	if err != nil || !found || !retained.EvidenceAsOf.Equal(now) || !retained.AttachedAt.Equal(now) {
		t.Fatal("re-enrollment must reveal the original old evidence times, never refreshed ones", err)
	}
}

func TestUsageAttachmentReplayConflictAndExactIdentity(t *testing.T) {
	root := usageAttachmentFixtureRoot(t)
	store := openUsageAttachmentFixture(t, root, true)
	now := time.Now().UTC()
	scope := UsageAttachmentScope{Source: "fixture", RepositoryID: 7}
	if err := store.SetEnrolled(context.Background(), scope, true); err != nil {
		t.Fatal(err)
	}
	job := usageAttachmentFixtureJob(5)
	record := usageAttachmentFixtureRecord(job, now)
	if err := store.Attach(context.Background(), record, now); err != nil {
		t.Fatal(err)
	}
	// A byte-identical replay is a no-op, not a second record.
	if err := store.Attach(context.Background(), record, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A conflicting payload for the same exact job at the same evidence time
	// never replaces retained evidence silently.
	conflicting := usageAttachmentFixtureRecord(job, now)
	conflicting.Evidence.Measured.CPU.AverageMilliCPU = "9999"
	if err := store.Attach(context.Background(), conflicting, now.Add(time.Second)); !errors.Is(err, ErrUsageAttachmentConflict) {
		t.Fatal("conflicting payload must be refused", err)
	}
	// Older evidence can never displace newer retained evidence.
	older := usageAttachmentFixtureRecord(job, now.Add(-time.Hour))
	if err := store.Attach(context.Background(), older, now); !errors.Is(err, ErrUsageAttachmentConflict) {
		t.Fatal("older evidence must not displace newer evidence", err)
	}
	// Newer evidence for the exact same job replaces the retained record.
	newer := usageAttachmentFixtureRecord(job, now.Add(time.Hour))
	newer.AttachedAt = newer.EvidenceAsOf
	if err := store.Attach(context.Background(), newer, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	retained, found, err := store.Read(context.Background(), job, now.Add(time.Hour))
	if err != nil || !found || !retained.EvidenceAsOf.Equal(now.Add(time.Hour)) {
		t.Fatal("newer evidence must replace the retained record", err)
	}
	// Sibling jobs and sibling attempts stay absent: identity is exact, never
	// inferred from names or nearby timestamps.
	for _, sibling := range []JobKey{
		usageAttachmentFixtureJob(6),
		{Attempt: AttemptKey{Source: "fixture", RepositoryID: 7, WorkflowID: 11, RunID: 13, Attempt: 3}, JobID: 5},
		{Attempt: AttemptKey{Source: "other", RepositoryID: 7, WorkflowID: 11, RunID: 13, Attempt: 2}, JobID: 5},
		{Attempt: AttemptKey{Source: "fixture", RepositoryID: 8, WorkflowID: 11, RunID: 13, Attempt: 2}, JobID: 5},
	} {
		if _, found, err := store.Read(context.Background(), sibling, now.Add(time.Hour)); err != nil && !errors.Is(err, ErrUsageAttachment) || found {
			t.Fatal("sibling or foreign identity must stay absent", sibling, err)
		}
	}
}

func TestUsageAttachmentMeasuredZeroAndUnavailableStayDistinct(t *testing.T) {
	root := usageAttachmentFixtureRoot(t)
	store := openUsageAttachmentFixture(t, root, true)
	now := time.Now().UTC()
	if err := store.SetEnrolled(context.Background(), UsageAttachmentScope{Source: "fixture", RepositoryID: 7}, true); err != nil {
		t.Fatal(err)
	}
	zero := usageAttachmentFixtureRecord(usageAttachmentFixtureJob(5), now)
	if zero.Evidence.Measured.Disk.Read.Bytes != "0" {
		t.Fatal("fixture must carry a measured zero")
	}
	if err := store.Attach(context.Background(), zero, now); err != nil {
		t.Fatal(err)
	}
	unavailable := UsageAttachment{
		Job:          usageAttachmentFixtureJob(6),
		Evidence:     Wire{State: VerifiedUsageUnavailable, Reason: UsageOmissionJoinUnsupported, AsOf: zero.Evidence.AsOf},
		EvidenceAsOf: now, AttachedAt: now,
	}
	if err := store.Attach(context.Background(), unavailable, now); err != nil {
		t.Fatal(err)
	}
	// A measured zero is a number; an unavailable record has no numbers. The
	// two must never merge.
	got, found, err := store.Read(context.Background(), zero.Job, now)
	if err != nil || !found || got.Evidence.Measured == nil || got.Evidence.Measured.Disk.Read.Bytes != "0" {
		t.Fatal("measured zero must be preserved as a measurement", err)
	}
	got, found, err = store.Read(context.Background(), unavailable.Job, now)
	if err != nil || !found || got.Evidence.Measured != nil || got.Evidence.Reason != UsageOmissionJoinUnsupported {
		t.Fatal("unavailable attachment must carry its reason and no numbers", err)
	}
	// An unavailable record with numbers, or a verified record without them,
	// is refused before retention.
	bad := unavailable
	bad.Evidence.Measured = zero.Evidence.Measured
	if err := store.Attach(context.Background(), bad, now); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("unavailable record with numbers must be refused", err)
	}
	bad = usageAttachmentFixtureRecord(usageAttachmentFixtureJob(8), now)
	bad.Evidence.Measured = nil
	if err := store.Attach(context.Background(), bad, now); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("verified record without numbers must be refused", err)
	}
}

func TestUsageAttachmentRestartRetentionAndBounds(t *testing.T) {
	root := usageAttachmentFixtureRoot(t)
	store := openUsageAttachmentFixture(t, root, true)
	now := time.Now().UTC()
	if err := store.SetEnrolled(context.Background(), UsageAttachmentScope{Source: "fixture", RepositoryID: 7}, true); err != nil {
		t.Fatal(err)
	}
	job := usageAttachmentFixtureJob(5)
	if err := store.Attach(context.Background(), usageAttachmentFixtureRecord(job, now), now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen: retained attachments survive a restart byte-exactly.
	reopened := openUsageAttachmentFixture(t, root, false)
	got, found, err := reopened.Read(context.Background(), job, now)
	if err != nil || !found || !got.EvidenceAsOf.Equal(now) {
		t.Fatal("restart must retain the exact attachment", err)
	}
	// Evidence older than the lookback is refused at attach; it is never
	// converted into a fresh sample.
	old := now.Add(-usageAttachmentLookback - time.Hour)
	stale := usageAttachmentFixtureRecord(usageAttachmentFixtureJob(9), old)
	if err := reopened.Attach(context.Background(), stale, now); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("old evidence must not attach as fresh", err)
	}
	// Retention: once the evidence ages out, reads report absence.
	later := now.Add(usageAttachmentRetention + time.Hour)
	if _, found, err := reopened.Read(context.Background(), job, later); err != nil || found {
		t.Fatal("expired attachment must read as absent", err)
	}
	// A leftover stage file fences reopen instead of being interpreted.
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.Name(), usageAttachmentStage), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenUsageAttachments(context.Background(), UsageAttachmentInput{Enabled: true, Root: root}); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("uncertain stage must fence reopen", err)
	}
	if err := os.Remove(filepath.Join(root.Name(), usageAttachmentStage)); err != nil {
		t.Fatal(err)
	}
	// Corrupt snapshots fail closed.
	if err := os.WriteFile(filepath.Join(root.Name(), usageAttachmentFile), []byte(`{"format":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenUsageAttachments(context.Background(), UsageAttachmentInput{Enabled: true, Root: root}); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("corrupt snapshot must fail closed", err)
	}
	// The disabled store is never opened implicitly.
	if store, err := OpenUsageAttachments(context.Background(), UsageAttachmentInput{}); err != nil || store != nil {
		t.Fatal("disabled input must return no store", err)
	}
}

func TestUsageAttachmentRejectsOversizedAndMalformed(t *testing.T) {
	root := usageAttachmentFixtureRoot(t)
	store := openUsageAttachmentFixture(t, root, true)
	now := time.Now().UTC()
	if err := store.SetEnrolled(context.Background(), UsageAttachmentScope{Source: "fixture", RepositoryID: 7}, true); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*UsageAttachment){
		"missing job id":         func(a *UsageAttachment) { a.Job.JobID = 0 },
		"future evidence":        func(a *UsageAttachment) { a.EvidenceAsOf = now.Add(time.Hour) },
		"attach before evidence": func(a *UsageAttachment) { a.AttachedAt = a.EvidenceAsOf.Add(-time.Second) },
		"bad state":              func(a *UsageAttachment) { a.Evidence.State = "unknown" },
		"missing reason":         func(a *UsageAttachment) { a.Evidence = Wire{State: VerifiedUsageUnavailable} },
		"oversized window":       func(a *UsageAttachment) { a.Evidence.Measured.Window = "86400000000001" },
	} {
		t.Run(name, func(t *testing.T) {
			record := usageAttachmentFixtureRecord(usageAttachmentFixtureJob(5), now)
			mutate(&record)
			if err := store.Attach(context.Background(), record, now); !errors.Is(err, ErrUsageAttachment) {
				t.Fatal("malformed attachment must be refused", err)
			}
		})
	}
	// Enrollment scope list is bounded. One scope is already enrolled above.
	for i := int64(0); i < usageAttachmentMaxScopes-1; i++ {
		if err := store.SetEnrolled(context.Background(), UsageAttachmentScope{Source: "s" + string(rune('a'+i%26)) + strings.Repeat("x", int(i/26)), RepositoryID: 1000 + i}, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetEnrolled(context.Background(), UsageAttachmentScope{Source: "onemore", RepositoryID: 1}, true); !errors.Is(err, ErrUsageAttachment) {
		t.Fatal("enrollment beyond the scope bound must be refused", err)
	}
}
