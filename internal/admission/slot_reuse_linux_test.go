package admission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const slotReuseFixtureSource = SlotReuseSourceClass("fixture_qualified_retirement")

func registerSlotReuseFixtureSource(t *testing.T) {
	t.Helper()
	qualifiedSlotReuseSources[slotReuseFixtureSource] = true
	t.Cleanup(func() { delete(qualifiedSlotReuseSources, slotReuseFixtureSource) })
}

// slotReuseFixtureEvidence builds the full positive synthetic case bound to
// the exact hold: every fact group present, correctly ordered and sourced.
func slotReuseFixtureEvidence(req Request, epoch int64, now time.Time) SlotReuseEvidence {
	identity := SlotReuseIdentity{
		Source: slotReuseFixtureSource, RequestKey: req.Key, Pool: req.Pool, Host: req.Host,
		CPU: req.Resources.CPU, RAM: req.Resources.RAM, Disk: req.Resources.Disk,
		Controller: "controller-1", SourceID: "source-1",
		Route: strings.Repeat("a", 64), Daemon: strings.Repeat("b", 64), Project: strings.Repeat("c", 64),
		Instance: "11111111-1111-4111-8111-111111111111", Generation: "22222222-2222-4222-8222-222222222222",
		ResourceID: "runner-1", Epoch: epoch, ObservedAt: now.Add(-3 * time.Minute),
	}
	return SlotReuseEvidence{
		Version:  SlotReuseEvidenceVersion,
		Identity: identity,
		Settlement: SlotReuseSettlement{
			Source: slotReuseFixtureSource, RequestKey: req.Key,
			JobTerminal: true, PostJobFinished: true, AssignmentClosed: true, StartRetryClosed: true, OneJobEphemeral: true,
			ObservedAt: now.Add(-2 * time.Minute),
		},
		Teardown: SlotReuseTeardown{
			Source: slotReuseFixtureSource, RequestKey: req.Key,
			InstanceAbsent: true, StorageReleased: true, ProcessesSettled: true,
			ObservedAt: now.Add(-time.Minute),
		},
		Fence: SlotReuseFence{
			Source: slotReuseFixtureSource, RequestKey: req.Key,
			IdentityDigest: slotReuseIdentityDigest(identity), Epoch: epoch, WriterFenced: true,
			ObservedAt: now.Add(-30 * time.Second),
		},
	}
}

func slotReuseHold(t *testing.T) (*SQLiteStore, Request, int64) {
	t.Helper()
	store, req, _ := releaseTestFixture(t)
	if _, err := store.MarkDeleting(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	fence := reconcileSQLite(t, store, []InventoryResource{{ID: "runner-1", Host: "a", Pool: "app", ReservationKey: req.Key, Resources: req.Resources, State: "deleting"}})
	return store, req, fence.Epoch
}

func slotReuseCharged(t *testing.T, store *SQLiteStore) Resources {
	t.Helper()
	view, err := store.ReadCapacityStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return view.Total
}

func TestClassifySlotReuseAcceptsOnlyTheCompletePositiveCase(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	now := time.Now().UTC()
	req := request(evidenceDigest([]byte("controller-1\x00app\x00runner-1")))
	complete := slotReuseFixtureEvidence(req, 7, now)
	if verdict := ClassifySlotReuse(req, complete, now); verdict.Status != SlotReuseCandidateComplete {
		t.Fatalf("complete positive case must classify complete: %+v", verdict)
	}
	for name, tc := range map[string]struct {
		mutate func(*SlotReuseEvidence)
		status SlotReuseStatus
	}{
		"wrong evidence version":          {func(e *SlotReuseEvidence) { e.Version = "slot-reuse-evidence/v0" }, SlotReuseUnsupportedSource},
		"unqualified identity source":     {func(e *SlotReuseEvidence) { e.Identity.Source = "caller_assertion" }, SlotReuseUnsupportedSource},
		"unqualified settlement source":   {func(e *SlotReuseEvidence) { e.Settlement.Source = "" }, SlotReuseUnsupportedSource},
		"unqualified teardown source":     {func(e *SlotReuseEvidence) { e.Teardown.Source = "incus_list" }, SlotReuseUnsupportedSource},
		"unqualified fence source":        {func(e *SlotReuseEvidence) { e.Fence.Source = "operator_checkbox" }, SlotReuseUnsupportedSource},
		"runner class out of scope":       {func(e *SlotReuseEvidence) { e.Settlement.OneJobEphemeral = false }, SlotReuseUnsupportedSource},
		"wrong pool":                      {func(e *SlotReuseEvidence) { e.Identity.Pool = "other" }, SlotReuseStaleOrConflictingEvidence},
		"wrong host":                      {func(e *SlotReuseEvidence) { e.Identity.Host = "b" }, SlotReuseStaleOrConflictingEvidence},
		"wrong key":                       {func(e *SlotReuseEvidence) { e.Identity.RequestKey = evidenceDigest([]byte("other")) }, SlotReuseStaleOrConflictingEvidence},
		"wrong cpu scope":                 {func(e *SlotReuseEvidence) { e.Identity.CPU = 2000 }, SlotReuseStaleOrConflictingEvidence},
		"wrong ram scope":                 {func(e *SlotReuseEvidence) { e.Identity.RAM = 4096 }, SlotReuseStaleOrConflictingEvidence},
		"wrong disk scope":                {func(e *SlotReuseEvidence) { e.Identity.Disk = 10 }, SlotReuseStaleOrConflictingEvidence},
		"wrong daemon":                    {func(e *SlotReuseEvidence) { e.Identity.Daemon = strings.Repeat("d", 64) }, SlotReuseStaleOrConflictingEvidence},
		"wrong project":                   {func(e *SlotReuseEvidence) { e.Identity.Project = "not-a-fingerprint" }, SlotReuseStaleOrConflictingEvidence},
		"wrong route":                     {func(e *SlotReuseEvidence) { e.Identity.Route = strings.Repeat("e", 64) }, SlotReuseStaleOrConflictingEvidence},
		"same name different incarnation": {func(e *SlotReuseEvidence) { e.Identity.Generation = "33333333-3333-4333-8333-333333333333" }, SlotReuseStaleOrConflictingEvidence},
		"missing instance":                {func(e *SlotReuseEvidence) { e.Identity.Instance = "" }, SlotReuseStaleOrConflictingEvidence},
		"settlement for another hold":     {func(e *SlotReuseEvidence) { e.Settlement.RequestKey = evidenceDigest([]byte("other")) }, SlotReuseStaleOrConflictingEvidence},
		"job not terminal":                {func(e *SlotReuseEvidence) { e.Settlement.JobTerminal = false }, SlotReuseAwaitingJobSettlement},
		"post job unfinished":             {func(e *SlotReuseEvidence) { e.Settlement.PostJobFinished = false }, SlotReuseAwaitingJobSettlement},
		"assignment open":                 {func(e *SlotReuseEvidence) { e.Settlement.AssignmentClosed = false }, SlotReuseAwaitingJobSettlement},
		"start retry open":                {func(e *SlotReuseEvidence) { e.Settlement.StartRetryClosed = false }, SlotReuseAwaitingJobSettlement},
		"instance still present":          {func(e *SlotReuseEvidence) { e.Teardown.InstanceAbsent = false }, SlotReuseAwaitingTeardownEvidence},
		"processes unsettled":             {func(e *SlotReuseEvidence) { e.Teardown.ProcessesSettled = false }, SlotReuseAwaitingTeardownEvidence},
		"storage not released":            {func(e *SlotReuseEvidence) { e.Teardown.StorageReleased = false }, SlotReuseAwaitingStorageEvidence},
		"fence missing":                   {func(e *SlotReuseEvidence) { e.Fence.WriterFenced = false }, SlotReuseFenceUnproven},
		"fence digest absent":             {func(e *SlotReuseEvidence) { e.Fence.IdentityDigest = "" }, SlotReuseFenceUnproven},
		"fence for another identity":      {func(e *SlotReuseEvidence) { e.Fence.IdentityDigest = strings.Repeat("f", 64) }, SlotReuseStaleOrConflictingEvidence},
		"fence wrong epoch":               {func(e *SlotReuseEvidence) { e.Fence.Epoch = 8 }, SlotReuseStaleOrConflictingEvidence},
		"stale identity":                  {func(e *SlotReuseEvidence) { e.Identity.ObservedAt = now.Add(-SlotReuseMaxEvidenceAge - time.Second) }, SlotReuseStaleOrConflictingEvidence},
		"future fence":                    {func(e *SlotReuseEvidence) { e.Fence.ObservedAt = now.Add(time.Second) }, SlotReuseStaleOrConflictingEvidence},
		"fence before settlement":         {func(e *SlotReuseEvidence) { e.Fence.ObservedAt = now.Add(-150 * time.Second) }, SlotReuseStaleOrConflictingEvidence},
		"teardown before identity":        {func(e *SlotReuseEvidence) { e.Teardown.ObservedAt = now.Add(-4 * time.Minute) }, SlotReuseStaleOrConflictingEvidence},
	} {
		t.Run(name, func(t *testing.T) {
			evidence := slotReuseFixtureEvidence(req, 7, now)
			tc.mutate(&evidence)
			if verdict := ClassifySlotReuse(req, evidence, now); verdict.Status != tc.status {
				t.Fatalf("got %+v, want %s", verdict, tc.status)
			}
		})
	}
	// With the production registry empty, even the complete positive case is
	// an unsupported source: a caller-constructed assertion never validates.
	delete(qualifiedSlotReuseSources, slotReuseFixtureSource)
	if verdict := ClassifySlotReuse(req, slotReuseFixtureEvidence(req, 7, now), now); verdict.Status != SlotReuseUnsupportedSource {
		t.Fatalf("production registry must fail closed: %+v", verdict)
	}
}

func TestReleaseRetiredSlotReleasesOnceAndReplaysWithoutCredit(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := context.Background()
	store, req, epoch := slotReuseHold(t)
	now := time.Now().UTC()
	before := slotReuseCharged(t, store)
	evidence := slotReuseFixtureEvidence(req, epoch, now)
	decision, err := store.ReleaseRetiredSlot(ctx, req, evidence)
	if err != nil || decision.Replay || decision.Status != SlotReuseCandidateComplete || decision.EvidenceDigest == "" {
		t.Fatalf("first release must commit: %+v %v", decision, err)
	}
	after := slotReuseCharged(t, store)
	if after != (Resources{}) || before != req.Resources {
		t.Fatalf("release must free exactly the charged vector once: before %v after %v", before, after)
	}
	// Identical evidence replay returns the prior decision with zero
	// additional credit, including after a store restart.
	replay, err := store.ReleaseRetiredSlot(ctx, req, evidence)
	if err != nil || !replay.Replay || replay.EvidenceDigest != decision.EvidenceDigest || !replay.ReleasedAt.Equal(decision.ReleasedAt) {
		t.Fatalf("identical replay must return the prior decision: %+v %v", replay, err)
	}
	if got := slotReuseCharged(t, store); got != (Resources{}) {
		t.Fatalf("replay must not change accounting: %v", got)
	}
	reopened := reopenSlotReuseStore(t, store)
	defer reopened.Close()
	replay, err = reopened.ReleaseRetiredSlot(ctx, req, evidence)
	if err != nil || !replay.Replay || replay.EvidenceDigest != decision.EvidenceDigest {
		t.Fatalf("restart replay must return the prior decision: %+v %v", replay, err)
	}
	// A later reconciliation advancing the epoch does not invalidate the
	// committed decision: the tombstone, not the epoch, answers replay.
	reconcileSQLite(t, reopened, nil)
	replay, err = reopened.ReleaseRetiredSlot(ctx, req, evidence)
	if err != nil || !replay.Replay || replay.EvidenceDigest != decision.EvidenceDigest {
		t.Fatalf("replay after epoch advance must return the prior decision: %+v %v", replay, err)
	}
	// A mismatched replay is contradictory: refused, and the tombstone and
	// accounting stay exactly as committed.
	conflicting := slotReuseFixtureEvidence(req, epoch, now)
	conflicting.Identity.Instance = "44444444-4444-4444-8444-444444444444"
	conflicting.Fence.IdentityDigest = slotReuseIdentityDigest(conflicting.Identity)
	if _, err := reopened.ReleaseRetiredSlot(ctx, req, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched replay must conflict: %v", err)
	}
	if got := slotReuseCharged(t, reopened); got != (Resources{}) {
		t.Fatalf("conflicting replay must not change accounting: %v", got)
	}
	tombstone, err := reopened.LookupRelease(ctx, req.Key)
	if err != nil || tombstone.AbsenceDigest != decision.EvidenceDigest {
		t.Fatalf("tombstone must be the original decision: %+v %v", tombstone, err)
	}
}

func reopenSlotReuseStore(t *testing.T, store *SQLiteStore) *SQLiteStore {
	t.Helper()
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	return reopened
}

func TestReleaseRetiredSlotKeepsChargeOnMissingOrContradictoryEvidence(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for name, tc := range map[string]struct {
		mutate func(*SlotReuseEvidence)
		status SlotReuseStatus
	}{
		"incomplete cleanup storage": {func(e *SlotReuseEvidence) { e.Teardown.StorageReleased = false }, SlotReuseAwaitingStorageEvidence},
		"job unsettled":              {func(e *SlotReuseEvidence) { e.Settlement.JobTerminal = false }, SlotReuseAwaitingJobSettlement},
		"fence unproven":             {func(e *SlotReuseEvidence) { e.Fence.WriterFenced = false }, SlotReuseFenceUnproven},
		"stale evidence":             {func(e *SlotReuseEvidence) { e.Identity.ObservedAt = now.Add(-time.Hour) }, SlotReuseStaleOrConflictingEvidence},
	} {
		t.Run(name, func(t *testing.T) {
			store, req, epoch := slotReuseHold(t)
			defer store.Close()
			evidence := slotReuseFixtureEvidence(req, epoch, now)
			tc.mutate(&evidence)
			decision, err := store.ReleaseRetiredSlot(ctx, req, evidence)
			if !errors.Is(err, ErrSlotReuseEvidence) || decision.Status != tc.status {
				t.Fatalf("got %+v %v, want %s", decision, err, tc.status)
			}
			if got := slotReuseCharged(t, store); got != req.Resources {
				t.Fatalf("charge must be preserved in full: %v", got)
			}
		})
	}
	// Evidence for the wrong reconciliation epoch is stale at commit even
	// though every fact is individually well formed.
	store, req, epoch := slotReuseHold(t)
	defer store.Close()
	if _, err := store.ReleaseRetiredSlot(ctx, req, slotReuseFixtureEvidence(req, epoch+1, now)); !errors.Is(err, ErrSlotReuseEvidence) {
		t.Fatal("epoch mismatch must fail closed", err)
	}
	if got := slotReuseCharged(t, store); got != req.Resources {
		t.Fatalf("epoch mismatch must preserve the charge: %v", got)
	}
	// A reservation that was never marked deleting is not a retirement.
	store2, req2, _ := releaseTestFixture(t)
	defer store2.Close()
	fence := reconcileSQLite(t, store2, []InventoryResource{{ID: "runner-1", Host: "a", Pool: "app", ReservationKey: req2.Key, Resources: req2.Resources, State: "idle"}})
	decision, err := store2.ReleaseRetiredSlot(ctx, req2, slotReuseFixtureEvidence(req2, fence.Epoch, now))
	if !errors.Is(err, ErrSlotReuseEvidence) || decision.Status != SlotReuseAwaitingTeardownEvidence {
		t.Fatalf("unmarked reservation must stay charged: %+v %v", decision, err)
	}
	// A missing charged inventory row is not absence proof.
	store3, req3, _ := releaseTestFixture(t)
	defer store3.Close()
	if _, err := store3.MarkDeleting(context.Background(), req3); err != nil {
		t.Fatal(err)
	}
	fence3 := reconcileSQLite(t, store3, []InventoryResource{{ID: "runner-1", Host: "a", Pool: "app", ReservationKey: req3.Key, Resources: req3.Resources, State: "deleting"}})
	if err := store3.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM inventory WHERE reservation_key=?", req3.Key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = store3.ReleaseRetiredSlot(ctx, req3, slotReuseFixtureEvidence(req3, fence3.Epoch, now))
	if !errors.Is(err, ErrSlotReuseEvidence) || decision.Status != SlotReuseAwaitingStorageEvidence {
		t.Fatalf("missing inventory must hold: %+v %v", decision, err)
	}
}

func TestReleaseRetiredSlotConcurrentReconciliationCreditsOnce(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := context.Background()
	store, req, epoch := slotReuseHold(t)
	defer store.Close()
	now := time.Now().UTC()
	evidence := slotReuseFixtureEvidence(req, epoch, now)
	const workers = 8
	decisions := make([]SlotReuseDecision, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			decisions[i], errs[i] = store.ReleaseRetiredSlot(ctx, req, evidence)
		}(i)
	}
	wg.Wait()
	committed, replayed := 0, 0
	for i := range decisions {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if decisions[i].Replay {
			replayed++
		} else {
			committed++
		}
		if decisions[i].EvidenceDigest == "" {
			t.Fatalf("worker %d lost the decision digest", i)
		}
	}
	if committed != 1 || replayed != workers-1 {
		t.Fatalf("exactly one commit and %d replays expected: %d/%d", workers-1, committed, replayed)
	}
	if got := slotReuseCharged(t, store); got != (Resources{}) {
		t.Fatalf("concurrent reconciliation must credit exactly once: %v", got)
	}
}

func TestReleaseRetiredSlotConflictingInventoryStaysCharged(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// A reappearing or renamed resource under the same reservation key is a
	// conflict, never a release.
	store, req, _ := releaseTestFixture(t)
	defer store.Close()
	if _, err := store.MarkDeleting(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	fence := reconcileSQLite(t, store, []InventoryResource{{ID: "runner-1", Host: "a", Pool: "app", ReservationKey: req.Key, Resources: req.Resources, State: "deleting"}})
	if err := store.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE inventory SET resource_id='runner-1-renamed',state='unknown' WHERE reservation_key=?", req.Key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseRetiredSlot(ctx, req, slotReuseFixtureEvidence(req, fence.Epoch, now)); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting inventory must fail closed", err)
	}
	if got := slotReuseCharged(t, store); got != req.Resources {
		t.Fatalf("conflicting inventory must stay charged: %v", got)
	}
}

func TestReleaseRetiredSlotProductionRegistryStaysFailClosed(t *testing.T) {
	// No fixture source registration: the exact production configuration.
	ctx := context.Background()
	store, req, epoch := slotReuseHold(t)
	defer store.Close()
	now := time.Now().UTC()
	decision, err := store.ReleaseRetiredSlot(ctx, req, slotReuseFixtureEvidence(req, epoch, now))
	if !errors.Is(err, ErrSlotReuseEvidence) || decision.Status != SlotReuseUnsupportedSource {
		t.Fatalf("production must refuse caller-constructed evidence: %+v %v", decision, err)
	}
	if got := slotReuseCharged(t, store); got != req.Resources {
		t.Fatalf("stock scope must stay charged: %v", got)
	}
}

func ExampleClassifySlotReuse() {
	req := request(evidenceDigest([]byte("controller-1\x00app\x00runner-1")))
	verdict := ClassifySlotReuse(req, SlotReuseEvidence{Version: SlotReuseEvidenceVersion}, time.Now())
	fmt.Println(verdict.Status)
	// Output: unsupported_source
}
