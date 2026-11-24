package admission

import (
	"context"
	"errors"
	"testing"
)

// TestReleaseVerifiedSharedLedgerCompetingRepositories is the review-requested
// regression: two repositories' runners share ONE ledger and pool budget, so
// only verified teardown of one charge frees capacity the other side can
// compete for. Every release is scoped to its own charge; nothing is freed
// by name collision, cross-wired evidence or the reduction-style reasoning.
func TestReleaseVerifiedSharedLedgerCompetingRepositories(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	h.jobBody("/repos/owner/competitor/actions/jobs/93", 93, 14, 18, "runner-2", "owner", "competitor", true)
	repoA := reuseRepo("a", "repo")
	repoB := reuseRepo("b", "competitor")
	// The pool budget fits exactly two runners: both repositories' charges
	// saturate it, so any growth requires a verified release.
	budget := Resources{CPU: 2000, RAM: 4096, Runners: 2}
	config := reuseFixtureConfig()
	config.Global = budget
	for id, pool := range config.Pools {
		pool.Budget = budget
		config.Pools[id] = pool
	}
	for id, host := range config.Hosts {
		host.Budget = budget
		config.Hosts[id] = host
	}
	store, fake := reuseStoreFixture(t, config)
	fA := newReuseRunner(t, h, store, fake, "controller-1", "app", "runner-1", 17, repoA)
	fB := newReuseRunner(t, h, store, fake, "controller-1", "app", "runner-2", 18, repoB)
	reconcileSQLite(t, store, []InventoryResource{fA.inventoryRow(), fB.inventoryRow()})
	charged := func(want int64) {
		t.Helper()
		if got := sqliteSnapshot(t, store).Allocated.Global.Runners; got != want {
			t.Fatalf("shared ledger runners = %d, want %d: %+v", got, want, sqliteSnapshot(t, store).Allocated.Global)
		}
	}
	charged(2)
	// Saturated: neither repository's growth fits while both charges hold.
	growth := Request{Key: reuseRunnerKey("controller-1", "app", "runner-3"), Pool: "app", Host: "a", Resources: Resources{CPU: 1000, RAM: 2048, Runners: 1}}
	if _, err := store.Admit(ctx, growth); !errors.Is(err, ErrDenied) {
		t.Fatalf("saturated pool admitted growth: %v", err)
	}
	// Cross-wired evidence never releases: repo A's workload bound to repo B's
	// charge fails matching on the shared ledger.
	chargeA := fA.buildCharge(t, fA.observeStockRegistration(t))
	chargeB := fB.buildCharge(t, fB.observeStockRegistration(t))
	workloadA := fA.workloadProof(t, fA.observeLifecycle(t, 91))
	teardownA := fA.teardownProof(t)
	cleanupA := fA.cleanupProof()
	teardownB := fB.teardownProof(t)
	cleanupB := fB.cleanupProof()
	if _, err := store.MarkDeleting(ctx, fA.request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDeleting(ctx, fB.request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseVerified(ctx, fB.request, fB.evidence(chargeB, workloadA, teardownB, cleanupB)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("cross-wired evidence released on shared ledger: %v", err)
	}
	charged(2)
	// Repo A's own qualified evidence releases exactly A on the shared ledger.
	if _, err := store.ReleaseVerified(ctx, fA.request, fA.evidence(chargeA, workloadA, teardownA, cleanupA)); err != nil {
		t.Fatal(err)
	}
	charged(1)
	// The freed slot is real capacity now: the competing repository's growth
	// request fits exactly because A's charge was verified gone.
	if _, err := store.Admit(ctx, growth); err != nil {
		t.Fatalf("verified release did not fund growth: %v", err)
	}
	charged(2)
	// Repo B is still fully charged and cannot be released by replayed or
	// foreign evidence; only B's own scoped proof releases B.
	if _, err := store.ReleaseVerified(ctx, fB.request, fB.evidence(chargeB, workloadA, teardownA, cleanupA)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("foreign reused evidence released B: %v", err)
	}
	workloadB := fB.workloadProof(t, fB.observeLifecycle(t, 93))
	if _, err := store.ReleaseVerified(ctx, fB.request, fB.evidence(chargeB, workloadB, teardownB, cleanupB)); err != nil {
		t.Fatal(err)
	}
	charged(1)
	if _, err := store.LookupRelease(ctx, fB.request.Key); err != nil {
		t.Fatal(err)
	}
	// The growth charge itself has no evidence: it stays charged.
	if _, err := store.ReleaseVerified(ctx, growth, fA.evidence(chargeA, workloadA, teardownA, cleanupA)); !errors.Is(err, ErrReuseEvidence) && !errors.Is(err, ErrDenied) {
		t.Fatalf("growth released without its own evidence: %v", err)
	}
	charged(1)
}
