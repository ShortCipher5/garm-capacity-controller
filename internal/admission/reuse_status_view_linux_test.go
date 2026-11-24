package admission

import (
	"context"
	"testing"
)

func TestReadReuseStatusReflectsDecisionsAndHeldCharges(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	workload := f.workloadProof(t, f.observeLifecycle(t, 91))
	view, err := f.store.ReadReuseStatus(ctx)
	if err != nil || len(view.Rows) != 1 {
		t.Fatalf("status view: %+v %v", view, err)
	}
	if row := view.Rows[0]; row.Disposition != ReuseStatusCharged || len(row.Reasons) != 0 || row.Remaining != f.request.Resources {
		t.Fatalf("charged row wrong: %+v", row)
	}
	if _, err := f.store.MarkDeleting(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	view, err = f.store.ReadReuseStatus(ctx)
	if err != nil || view.Rows[0].Disposition != ReuseStatusPendingCleanup || len(view.Rows[0].Reasons) != 1 || view.Rows[0].Reasons[0] != "awaiting_qualified_terminal_evidence" {
		t.Fatalf("pending cleanup row wrong: %+v %v", view.Rows[0], err)
	}
	// Delayed teardown: partial disk-only decision shows disk freed, compute
	// held with reasons.
	delayed := f.evidence(charge, workload, nil, f.cleanupProof())
	if _, err := f.store.ReleaseVerified(ctx, f.request, delayed); err != nil {
		t.Fatal(err)
	}
	view, err = f.store.ReadReuseStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := view.Rows[0]
	if row.Disposition != ReuseStatusPartiallyReusable || !row.Reusable.Disk || row.Reusable.CPU || row.Remaining.Disk != 0 || row.Remaining.CPU != f.request.Resources.CPU || row.Remaining.Runners != 1 {
		t.Fatalf("partial row wrong: %+v", row)
	}
	if len(row.Reasons) == 0 {
		t.Fatal("held reasons not visible")
	}
}

func TestReadReuseStatusShowsVerifiedReusableAfterFullRelease(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	workload := f.workloadProof(t, f.observeLifecycle(t, 91))
	if _, err := f.store.MarkDeleting(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(charge, workload, f.teardownProof(t), f.cleanupProof())); err != nil {
		t.Fatal(err)
	}
	view, err := f.store.ReadReuseStatus(ctx)
	if err != nil || len(view.Rows) != 1 {
		t.Fatalf("status view: %+v %v", view, err)
	}
	row := view.Rows[0]
	if row.Disposition != ReuseStatusVerifiedReusable || !row.Reusable.All() || row.Remaining != (Resources{}) || len(row.Reasons) != 1 || row.Reasons[0] != "released_verified_teardown" {
		t.Fatalf("released row wrong: %+v", row)
	}
}
