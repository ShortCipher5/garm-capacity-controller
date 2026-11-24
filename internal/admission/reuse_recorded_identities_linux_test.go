package admission

import (
	"context"
	"errors"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/reuse"
)

// TestReleaseVerifiedBindsToRecordedIdentities covers the review requirement
// that evidence is validated against the durable retirement intent and the
// independently recorded runner/volume identities before any dimension is
// credited. Every tamper keeps the full charge.
func TestReleaseVerifiedBindsToRecordedIdentities(t *testing.T) {
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
	// Capture the provider-side teardown once; the fake daemon stands in for
	// the stock provider and each delivery below reuses the same proofs.
	teardown := f.teardownProof(t)
	cleanup := f.cleanupProof()
	assertCharged := func() {
		t.Helper()
		if got := sqliteSnapshot(t, f.store); got.Allocated.Global.Runners != 1 || len(got.Inventory) != 1 {
			t.Fatalf("tampered evidence credited capacity: %+v", got.Allocated.Global)
		}
	}
	// A volume binding digest that does not recompute from the enrolled scope
	// and the exact recorded name never validates.
	tamperedVolume := charge
	tamperedVolume.Volumes = append([]reuse.VolumeCharge(nil), charge.Volumes...)
	tamperedVolume.Volumes[0].BindingDigest = evidenceDigest([]byte("forged-binding"))
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(tamperedVolume, workload, teardown, cleanup)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("forged volume binding credited capacity: %v", err)
	}
	assertCharged()
	// A volume name that is not the independently recorded dispatch identity
	// (the recorded runner root volume) never validates.
	foreignVolume := charge
	foreignVolume.Volumes = []reuse.VolumeCharge{{Name: "other-volume", BindingDigest: reuse.VolumeBindingDigest(charge.Provider.ScopeFingerprint, "other-volume")}}
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(foreignVolume, workload, teardown, cleanup)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("foreign volume identity credited capacity: %v", err)
	}
	assertCharged()
	// Mutating the recorded dispatch identity invalidates the evidence
	// context; the original charge stays held until review.
	if _, err := f.store.db.Exec("UPDATE dispatches SET resource_id=? WHERE reservation_key=?", "runner-forged", f.request.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(charge, workload, teardown, cleanup)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("rebound dispatch identity credited capacity: %v", err)
	}
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(charge, workload, nil, cleanup)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("forged dispatch identity released on retry: %v", err)
	}
	assertCharged()
	// A different request identity (another reservation's profile) is refused.
	other := f.request
	other.Resources.RAM++
	if _, err := f.store.ReleaseVerified(ctx, other, f.evidence(charge, workload, nil, cleanup)); !errors.Is(err, ErrDenied) && !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign request identity credited capacity: %v", err)
	}
	assertCharged()
}
