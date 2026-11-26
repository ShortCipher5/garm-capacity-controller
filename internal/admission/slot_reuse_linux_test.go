package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
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
		Epoch: epoch, ObservedAt: now.Add(-3 * time.Minute),
	}
	identity.ResourceID = slotReuseResourceID(identity)
	return SlotReuseEvidence{
		Version:  SlotReuseEvidenceVersion,
		Identity: identity,
		Settlement: SlotReuseSettlement{
			Source: slotReuseFixtureSource, RequestKey: req.Key,
			IdentityDigest: slotReuseIdentityDigest(identity),
			JobTerminal:    true, PostJobFinished: true, AssignmentClosed: true, StartRetryClosed: true, OneJobEphemeral: true,
			ObservedAt: now.Add(-2 * time.Minute),
		},
		Teardown: SlotReuseTeardown{
			Source: slotReuseFixtureSource, RequestKey: req.Key,
			IdentityDigest: slotReuseIdentityDigest(identity),
			InstanceAbsent: true, StorageReleased: true, ProcessesSettled: true,
			ObservedAt: now,
		},
		Fence: SlotReuseFence{
			Source: slotReuseFixtureSource, RequestKey: req.Key,
			IdentityDigest: slotReuseIdentityDigest(identity), Epoch: epoch, WriterFenced: true,
			ObservedAt: now,
		},
	}
}

func slotFixtureBinding(host string, at time.Time) HostBinding {
	daemon, scope := strings.Repeat("b", 64), strings.Repeat("c", 64)
	if host == "b" {
		daemon, scope = strings.Repeat("e", 64), strings.Repeat("f", 64)
	}
	return HostBinding{Host: host, ServerKeyFingerprint: daemon, ScopeFingerprint: scope,
		OriginFingerprint: evidenceDigest([]byte(host)), ServerName: "fixture-" + host, ServerVersion: "6.0.4", ObservedAt: at}
}

// The offline inventory producer supplies a fresh, independent observation
// after each actual BeginReconciliation. Production uses the sealed Incus list.
type slotInventoryFixture struct {
	identity  SlotReuseIdentity
	name      string
	createdAt int64
}

func reconcileSlotInventory(t *testing.T, s *SQLiteStore, rows []InventoryResource, ids map[string]slotInventoryFixture, wantErr ...error) Fence {
	t.Helper()
	ctx := t.Context()
	fence, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	state := sqliteSnapshot(t, s)
	rows = append([]InventoryResource(nil), rows...)
	for i, r := range rows {
		sample, ok := ids[r.ID]
		if !ok {
			continue
		}
		id := sample.identity
		binding := slotFixtureBinding(r.Host, now)
		rows[i].listed = incusapi.ListedInstanceObservation{
			StartedAt: now, CompletedAt: now, FenceDigest: sha256.Sum256([]byte(fence.Nonce)),
			Subject: incusapi.InstanceIdentitySubject{BackendKeyFingerprint: id.Daemon, ScopeFingerprint: id.Project,
				OriginFingerprint: binding.OriginFingerprint, ServerName: binding.ServerName, ServerVersion: binding.ServerVersion,
				UUID: id.Instance, Generation: id.Generation, ControllerID: id.Controller, Name: sample.name, Project: "fixture",
				PoolID: r.Pool, ReservationKey: r.ReservationKey, CPUMillis: r.Resources.CPU, RAMMiB: r.Resources.RAM,
				CreatedAt: time.Unix(sample.createdAt, 0).UTC()},
		}
	}
	var expected error
	if len(wantErr) > 0 {
		expected = wantErr[0]
	}
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(state.Config, now), Resources: rows}); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	return fence
}

func slotReuseHold(t *testing.T) (*SQLiteStore, Request, int64) {
	t.Helper()
	ctx := context.Background()
	config := fixture(time.Now())
	store := sqliteFixtureStore(t, config, false)
	barrier, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := store.now()
	if err := store.BindHostIdentities(ctx, barrier, []HostBinding{slotFixtureBinding("a", at), slotFixtureBinding("b", at)}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteReconciliation(ctx, barrier, InventorySnapshot{Hosts: observations(config, at)}); err != nil {
		t.Fatal(err)
	}
	req := request(evidenceDigest([]byte("controller-1\x00app\x00runner-1")))
	req.Resources.Disk = 8192
	if _, err := store.Admit(ctx, req); err != nil {
		t.Fatal(err)
	}
	identity := slotReuseFixtureEvidence(req, 0, store.now()).Identity
	row := InventoryResource{ID: identity.ResourceID, Host: req.Host, Pool: req.Pool,
		ReservationKey: req.Key, Resources: req.Resources, State: "idle"}
	reconcileSlotInventory(t, store, []InventoryResource{row}, map[string]slotInventoryFixture{row.ID: {identity, "runner-1", 1}})
	if _, err := store.MarkDeleting(ctx, req); err != nil {
		t.Fatal(err)
	}
	row.State = "deleting"
	fence := reconcileSlotInventory(t, store, []InventoryResource{row}, map[string]slotInventoryFixture{row.ID: {identity, "runner-1", 1}})
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
	// A reappearing allocation is fully charged even when an old tombstone
	// answers lookup. Check both expired evidence and a closed admission barrier.
	row := InventoryResource{ID: evidence.Identity.ResourceID, Host: req.Host, Pool: req.Pool,
		ReservationKey: req.Key, Resources: req.Resources, State: "unknown"}
	reconcileSQLite(t, reopened, []InventoryResource{row})
	reopened.now = func() time.Time { return now.Add(time.Hour) }
	for _, blocked := range []bool{false, true} {
		if blocked {
			if _, err := reopened.BeginReconciliation(ctx); err != nil {
				t.Fatal(err)
			}
		}
		replay, err := reopened.ReleaseRetiredSlot(ctx, req, evidence)
		if err != nil || !replay.Replay || replay.EvidenceDigest != decision.EvidenceDigest ||
			!replay.ReleasedAt.Equal(decision.ReleasedAt) || replay.Request != req {
			t.Fatalf("historical replay (blocked=%v): %+v %v", blocked, replay, err)
		}
		conflicting := evidence
		conflicting.Fence.WriterFenced = false
		refusal, err := reopened.ReleaseRetiredSlot(ctx, req, conflicting)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("conflicting replay (blocked=%v): %+v %v", blocked, refusal, err)
		}
		assertNoSlotReuseCredit(t, refusal)
		conflictingHold := req
		conflictingHold.Resources.Disk++
		refusal, err = reopened.ReleaseRetiredSlot(ctx, conflictingHold, evidence)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("different request replay (blocked=%v): %+v %v", blocked, refusal, err)
		}
		assertNoSlotReuseCredit(t, refusal)
		if got := slotReuseCharged(t, reopened); got != req.Resources {
			t.Fatalf("replay changed reappearing charge: %v", got)
		}
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
	store3, req3, epoch3 := slotReuseHold(t)
	defer store3.Close()
	if err := store3.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM inventory WHERE reservation_key=?", req3.Key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = store3.ReleaseRetiredSlot(ctx, req3, slotReuseFixtureEvidence(req3, epoch3, store3.now()))
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
	for _, tc := range []struct{ name, change string }{
		{"display name only", "resource_id='runner-1'"},
		{"larger disk", "disk=disk+1"},
		{"smaller disk", "disk=disk-1"},
		{"running", "state='running'"},
		{"unknown", "state='unknown'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, req, epoch := slotReuseHold(t)
			if _, err := store.db.Exec("UPDATE inventory SET "+tc.change+" WHERE reservation_key=?", req.Key); err != nil {
				t.Fatal(err)
			}
			before := slotReuseCharged(t, store)
			decision, err := store.ReleaseRetiredSlot(ctx, req, slotReuseFixtureEvidence(req, epoch, store.now()))
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("conflicting inventory: %+v %v", decision, err)
			}
			assertNoSlotReuseCredit(t, decision)
			if got := slotReuseCharged(t, store); got != before {
				t.Fatalf("conflict changed charge from %v to %v", before, got)
			}
		})
	}
	// Internally consistent evidence and fence for another generation under
	// the same request cannot match the original charged incarnation.
	store, req, epoch := slotReuseHold(t)
	other := slotReuseFixtureEvidence(req, epoch, store.now())
	other.Identity.Generation = "33333333-3333-4333-8333-333333333333"
	other.Identity.ResourceID = slotReuseResourceID(other.Identity)
	other.Fence.IdentityDigest = slotReuseIdentityDigest(other.Identity)
	other.Settlement.IdentityDigest = other.Fence.IdentityDigest
	other.Teardown.IdentityDigest = other.Fence.IdentityDigest
	if verdict := ClassifySlotReuse(req, other, store.now()); verdict.Status != SlotReuseCandidateComplete {
		t.Fatalf("expected complete candidate for other incarnation: %+v", verdict)
	}
	decision, err := store.ReleaseRetiredSlot(ctx, req, other)
	if !errors.Is(err, ErrConflict) || slotReuseCharged(t, store) != req.Resources {
		t.Fatalf("different incarnation must stay charged: %+v %v", decision, err)
	}
	assertNoSlotReuseCredit(t, decision)
}

func TestReleaseRetiredSlotRejectsUnboundFactGroupsAndNilIncarnations(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	store, req, epoch := slotReuseHold(t)
	evidence := slotReuseFixtureEvidence(req, epoch, store.now())
	other := evidence.Identity
	other.Generation = "33333333-3333-4333-8333-333333333333"
	other.ResourceID = slotReuseResourceID(other)
	otherDigest := slotReuseIdentityDigest(other)
	for _, group := range []string{"settlement", "teardown", "nil instance", "nil generation"} {
		t.Run(group, func(t *testing.T) {
			candidate := evidence
			switch group {
			case "settlement":
				candidate.Settlement.IdentityDigest = otherDigest
			case "teardown":
				candidate.Teardown.IdentityDigest = otherDigest
			default:
				if group == "nil instance" {
					candidate.Identity.Instance = "00000000-0000-0000-0000-000000000000"
				} else {
					candidate.Identity.Generation = "00000000-0000-0000-0000-000000000000"
				}
				candidate.Identity.ResourceID = slotReuseResourceID(candidate.Identity)
				digest := slotReuseIdentityDigest(candidate.Identity)
				candidate.Settlement.IdentityDigest = digest
				candidate.Teardown.IdentityDigest = digest
				candidate.Fence.IdentityDigest = digest
			}
			if verdict := ClassifySlotReuse(req, candidate, store.now()); verdict.Status != SlotReuseStaleOrConflictingEvidence {
				t.Fatalf("invalid incarnation classified as %+v", verdict)
			}
			got, err := store.ReleaseRetiredSlot(context.Background(), req, candidate)
			if !errors.Is(err, ErrSlotReuseEvidence) || slotReuseCharged(t, store) != req.Resources {
				t.Fatalf("incarnation mismatch changed accounting: %+v %v", got, err)
			}
			assertNoSlotReuseCredit(t, got)
		})
	}
}

func TestReleaseRetiredSlotSerializesWithReconciliationOnAnotherHandle(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := context.Background()
	store, req, epoch := slotReuseHold(t)
	other, err := OpenSQLite(store.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	evidence := slotReuseFixtureEvidence(req, epoch, store.now())
	start := make(chan struct{})
	fenced := make(chan error, 1)
	go func() {
		<-start
		_, err := other.BeginReconciliation(ctx)
		fenced <- err
	}()
	close(start)
	d, releaseErr := store.ReleaseRetiredSlot(ctx, req, evidence)
	if err := <-fenced; err != nil {
		t.Fatal(err)
	}
	snapshot := sqliteSnapshot(t, other)
	if snapshot.Ready || snapshot.Epoch != epoch+1 {
		t.Fatalf("reconciliation barrier lost (ready=%v, epoch=%d)", snapshot.Ready, snapshot.Epoch)
	}
	if releaseErr == nil {
		replay, err := other.ReleaseRetiredSlot(ctx, req, evidence)
		if d.Replay || err != nil || !replay.Replay || replay.EvidenceDigest != d.EvidenceDigest ||
			slotReuseCharged(t, other) != (Resources{}) {
			t.Fatalf("committed release did not serialize before barrier: %+v %+v %v", d, replay, err)
		}
	} else {
		if !errors.Is(releaseErr, ErrReconciliation) || slotReuseCharged(t, other) != req.Resources {
			t.Fatalf("release changed charge after barrier: %+v %v", d, releaseErr)
		}
		assertNoSlotReuseCredit(t, d)
	}
}

func assertNoSlotReuseCredit(t *testing.T, d SlotReuseDecision) {
	t.Helper()
	if d.Replay || d.Status == SlotReuseCandidateComplete || d.EvidenceDigest != "" || !d.ReleasedAt.IsZero() {
		t.Fatalf("refusal reported a release: %+v", d)
	}
}

func TestReleaseRetiredSlotEvidenceExpiresWhileWaitingForFinalization(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := context.Background()
	store, req, epoch := slotReuseHold(t)
	now := store.now()
	evidence := slotReuseFixtureEvidence(req, epoch, now)
	evidence.Identity.ObservedAt = now.Add(-SlotReuseMaxEvidenceAge)
	if verdict := ClassifySlotReuse(req, evidence, now); verdict.Status != SlotReuseCandidateComplete {
		t.Fatalf("pre-lock candidate: %+v", verdict)
	}

	// Occupy the only connection until ReleaseRetiredSlot has reached BeginTx.
	var offset atomic.Int64
	store.now = func() time.Time { return now.Add(time.Duration(offset.Load())) }
	store.db.SetMaxOpenConns(1)
	blocker, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	waited := store.db.Stats().WaitCount
	type outcome struct {
		d   SlotReuseDecision
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		d, err := store.ReleaseRetiredSlot(ctx, req, evidence)
		done <- outcome{d, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for store.db.Stats().WaitCount == waited {
		if time.Now().After(deadline) {
			t.Fatal("release did not reach the locked connection")
		}
		time.Sleep(time.Millisecond)
	}
	offset.Store(int64(time.Second))
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if !errors.Is(out.err, ErrSlotReuseEvidence) || out.d.Status != SlotReuseStaleOrConflictingEvidence {
		t.Fatalf("expired evidence: %+v %v", out.d, out.err)
	}
	assertNoSlotReuseCredit(t, out.d)
	if got := slotReuseCharged(t, store); got != req.Resources {
		t.Fatalf("expired evidence changed charge: %v", got)
	}
}

func TestReleaseRetiredSlotRejectsFenceObservedBeforeCurrentBarrier(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := t.Context()
	store, req, epoch := slotReuseHold(t)
	var value string
	if err := store.db.QueryRowContext(ctx, "SELECT began_at FROM metadata WHERE id=1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	began, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	now := store.now()
	evidence := slotReuseFixtureEvidence(req, epoch, now)
	for _, observed := range []time.Time{began.Add(-time.Nanosecond), began} {
		before := evidence
		before.Fence.ObservedAt = observed
		before.Teardown.ObservedAt = observed
		if verdict := ClassifySlotReuse(req, before, now); verdict.Status != SlotReuseCandidateComplete {
			t.Fatalf("fixture must have otherwise complete current-epoch evidence: %+v", verdict)
		}
		decision, err := store.ReleaseRetiredSlot(ctx, req, before)
		if !errors.Is(err, ErrSlotReuseEvidence) || decision.Status != SlotReuseFenceUnproven {
			t.Fatalf("pre-barrier fence was not rejected: %+v %v", decision, err)
		}
		assertNoSlotReuseCredit(t, decision)
		if charged := slotReuseCharged(t, store); charged != req.Resources {
			t.Fatalf("pre-barrier fence changed the charge: %v", charged)
		}
		if _, err := store.LookupRelease(ctx, req.Key); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("pre-barrier fence created a release tombstone: %v", err)
		}
	}
	release, err := store.ReleaseRetiredSlot(ctx, req, evidence)
	if err != nil || release.Replay || release.Status != SlotReuseCandidateComplete || slotReuseCharged(t, store) != (Resources{}) {
		t.Fatalf("post-barrier fence failed to release the exact charge: %+v %v", release, err)
	}
}

func TestReleaseRetiredSlotUncertainCommitReportsNoCredit(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := context.Background()
	store, req, epoch := slotReuseHold(t)
	// The release callback succeeds. SQLite detects this deferred constraint
	// only at COMMIT, making the outcome uncertain to the store.
	_, err := store.db.Exec(`CREATE TABLE slot_reuse_test_guard (
		key TEXT REFERENCES reservations(request_key) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER slot_reuse_test_commit AFTER INSERT ON releases BEGIN
		INSERT INTO slot_reuse_test_guard VALUES ('missing'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.ReleaseRetiredSlot(ctx, req, slotReuseFixtureEvidence(req, epoch, store.now()))
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("commit failure: %+v %v", decision, err)
	}
	assertNoSlotReuseCredit(t, decision)
	reopened := reopenSlotReuseStore(t, store)
	defer reopened.Close()
	if got := slotReuseCharged(t, reopened); got != req.Resources {
		t.Fatalf("rolled back transaction changed charge: %v", got)
	}
	if _, err := reopened.LookupRelease(ctx, req.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected tombstone after rollback: %v", err)
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

func TestSlotReuseHoldsConflictingIncarnationAcrossHosts(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	for _, tc := range []struct {
		name               string
		orphan, otherRoute bool
	}{
		{"same ID linked", false, false},
		{"same ID orphan", true, false},
		{"different route linked", false, true},
		{"different route orphan", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, req, _ := slotReuseHold(t)
			second := req
			second.Key, second.Host, second.Pool = evidenceDigest([]byte("other repository job")), "b", "api"
			evidenceFor := func(target Request, epoch int64) SlotReuseEvidence {
				e := slotReuseFixtureEvidence(target, epoch, s.now())
				if tc.otherRoute && target == second {
					e.Identity.Route = strings.Repeat("d", 64)
					e.Identity.ResourceID = slotReuseResourceID(e.Identity)
					digest := slotReuseIdentityDigest(e.Identity)
					e.Settlement.IdentityDigest, e.Teardown.IdentityDigest, e.Fence.IdentityDigest = digest, digest, digest
				}
				return e
			}
			firstID, secondID := evidenceFor(req, 0).Identity.ResourceID, evidenceFor(second, 0).Identity.ResourceID
			if (firstID != secondID) != tc.otherRoute {
				t.Fatal("unexpected route-scoped ID match")
			}
			rows := []InventoryResource{
				{ID: firstID, Host: req.Host, Pool: req.Pool, ReservationKey: req.Key, Resources: req.Resources, State: "deleting"},
				{ID: secondID, Host: second.Host, Pool: second.Pool, Resources: second.Resources, State: "idle"},
			}
			if !tc.orphan {
				if _, err := s.Admit(ctx, second); err != nil {
					t.Fatal(err)
				}
				rows[1].ReservationKey = second.Key
			}
			reconcileSQLite(t, s, rows)
			if tc.orphan {
				// Omission leaves the other host's last observation charged as unknown.
				rows = rows[:1]
			} else {
				if _, err := s.MarkDeleting(ctx, second); err != nil {
					t.Fatal(err)
				}
				rows[1].State = "deleting"
			}
			fence := reconcileSQLite(t, s, rows)
			other, err := OpenSQLite(s.path, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			targets := []Request{req, second}
			if tc.orphan {
				targets = []Request{req, req}
			}
			decisions := make([]SlotReuseDecision, len(targets))
			errs := make([]error, len(targets))
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, handle := range []*SQLiteStore{s, other} {
				target := targets[i]
				e := evidenceFor(target, fence.Epoch)
				if ClassifySlotReuse(target, e, s.now()).Status != SlotReuseCandidateComplete {
					t.Fatal("incomplete candidate")
				}
				wg.Go(func() {
					<-start
					decisions[i], errs[i] = handle.ReleaseRetiredSlot(ctx, target, e)
				})
			}
			close(start)
			wg.Wait()
			for i, target := range targets {
				if !errors.Is(errs[i], ErrConflict) {
					t.Fatalf("request %s: %+v %v", target.Key, decisions[i], errs[i])
				}
				assertNoSlotReuseCredit(t, decisions[i])
				if _, err := s.LookupRelease(ctx, target.Key); !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(err)
				}
			}
			if got, want := slotReuseCharged(t, s), (Resources{CPU: 2 * req.Resources.CPU, RAM: 2 * req.Resources.RAM, Disk: 2 * req.Resources.Disk, Runners: 2}); got != want {
				t.Fatalf("conflicting pair charged %+v, want %+v", got, want)
			}
		})
	}
}

func TestSlotReuseTombstoneFollowsIncarnationAcrossRebindAndRestart(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := t.Context()
	s, req, epoch := slotReuseHold(t)
	first := slotReuseFixtureEvidence(req, epoch, s.now())
	if got, err := s.ReleaseRetiredSlot(ctx, req, first); err != nil || got.Replay {
		t.Fatalf("first release: %+v %v", got, err)
	}
	s = reopenSlotReuseStore(t, s)
	defer s.Close()
	// Release the independently identified new generation while it is the only inventory row.
	for i, sameGeneration := range []bool{false, true} {
		// A newly enrolled route and a different request and host give
		// the same physical instance a different scoped resource ID.
		next := req
		name := fmt.Sprintf("rebound-%d", i)
		next.Key, next.Pool, next.Host = evidenceDigest([]byte("controller-1\x00api\x00"+name)), "api", "b"
		if _, err := s.Admit(ctx, next); err != nil {
			t.Fatal(err)
		}
		build := func(epoch int64) SlotReuseEvidence {
			e := slotReuseFixtureEvidence(next, epoch, s.now())
			e.Identity.Route = strings.Repeat("d", 64)
			e.Identity.Daemon = strings.Repeat("e", 64)
			e.Identity.Project = strings.Repeat("f", 64)
			if !sameGeneration {
				e.Identity.Generation = "33333333-3333-4333-8333-333333333333"
			}
			e.Identity.ResourceID = slotReuseResourceID(e.Identity)
			digest := slotReuseIdentityDigest(e.Identity)
			e.Settlement.IdentityDigest, e.Teardown.IdentityDigest, e.Fence.IdentityDigest = digest, digest, digest
			return e
		}
		row := InventoryResource{ID: build(0).Identity.ResourceID, Host: next.Host, Pool: next.Pool, ReservationKey: next.Key, Resources: next.Resources, State: "idle"}
		reconcileSlotInventory(t, s, []InventoryResource{row}, map[string]slotInventoryFixture{row.ID: {build(0).Identity, name, 1}})
		if _, err := s.MarkDeleting(ctx, next); err != nil {
			t.Fatal(err)
		}
		row.State = "deleting"
		fence := reconcileSlotInventory(t, s, []InventoryResource{row}, map[string]slotInventoryFixture{row.ID: {build(0).Identity, name, 1}})
		e := build(fence.Epoch)
		decision, err := s.ReleaseRetiredSlot(ctx, next, e)
		if sameGeneration {
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("second request claimed original incarnation: %+v %v", decision, err)
			}
			assertNoSlotReuseCredit(t, decision)
		} else if err != nil || decision.Replay {
			t.Fatalf("distinct generation refused: %+v %v", decision, err)
		}
		var want Resources
		if sameGeneration {
			want = req.Resources
		}
		if got := slotReuseCharged(t, s); got != want {
			t.Fatalf("rebound charge: got %+v, want %+v", got, want)
		}
	}
	if replay, err := s.ReleaseRetiredSlot(ctx, req, first); err != nil || !replay.Replay {
		t.Fatalf("original replay: %+v %v", replay, err)
	}
}

func TestSlotReusePriorReleaseFormats(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	for _, tc := range []struct {
		name      string
		version   string
		different bool
		want      error
	}{
		{"opaque v1 has same incarnation", "slot-reuse-evidence/v1", false, ErrSlotReuseEvidence},
		{"different v1 generation", "slot-reuse-evidence/v1", true, ErrSlotReuseEvidence},
		{"old project absence has same UUID and generation", "", false, ErrSlotReuseEvidence},
		{"old project absence has different generation", "", true, ErrSlotReuseEvidence},
		{"unrecognized record", "future", true, ErrSlotReuseEvidence},
		{"unreadable blob", "unreadable", true, ErrSlotReuseEvidence},
		{"record no longer matches digest", "bad digest", true, ErrSlotReuseEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, req, epoch := slotReuseHold(t)
			priorReq := req
			priorReq.Key = evidenceDigest([]byte("old release"))
			candidate := slotReuseFixtureEvidence(req, epoch, s.now())
			prior := slotReuseFixtureEvidence(priorReq, 1, s.now())
			prior.Identity.ResourceID = "old-opaque-inventory-key"
			if tc.different {
				prior.Identity.Generation = "33333333-3333-4333-8333-333333333333"
			}
			prior.Version = tc.version
			var history any = prior
			if tc.version == "" {
				history = incusapi.ProjectAbsenceRecord{Identity: incusapi.InstanceIdentityObservation{
					Subject: incusapi.InstanceIdentitySubject{ReservationKey: priorReq.Key, UUID: prior.Identity.Instance, Generation: prior.Identity.Generation},
				}}
			}
			raw, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			if tc.version == "unreadable" {
				raw = []byte("unreadable")
			}
			digest := evidenceDigest(raw)
			if tc.version == "bad digest" {
				digest = evidenceDigest([]byte("different record"))
			}
			_, err = s.db.ExecContext(ctx, `INSERT INTO releases(request_key,pool,host,cpu,ram,disk,runners,absence_digest,record,released_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				priorReq.Key, priorReq.Pool, priorReq.Host, priorReq.Resources.CPU, priorReq.Resources.RAM, priorReq.Resources.Disk, priorReq.Resources.Runners, digest, raw, s.now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				t.Fatal(err)
			}
			decision, err := s.ReleaseRetiredSlot(ctx, req, candidate)
			if !errors.Is(err, tc.want) {
				t.Fatalf("historical record: %+v %v, want %v", decision, err, tc.want)
			}
			assertNoSlotReuseCredit(t, decision)
			if slotReuseCharged(t, s) != req.Resources {
				t.Fatal("unindexed historical record refunded charge")
			}
			if _, err := s.LookupRelease(ctx, req.Key); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
		})
	}
}

func TestSlotReuseInMultiRunnerInventory(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	for _, mode := range []string{"distinct active", "same pair across routes", "retained unknown", "opaque first observation", "same name rebound", "same pair different creation", "changed bounds", "present after teardown"} {
		t.Run(mode, func(t *testing.T) {
			s, req, _ := slotReuseHold(t)
			ctx := t.Context()
			first := slotReuseFixtureEvidence(req, 0, s.now()).Identity
			row := InventoryResource{ID: first.ResourceID, Pool: req.Pool, Host: req.Host, ReservationKey: req.Key, Resources: req.Resources, State: "deleting"}
			other := first
			other.Daemon, other.Project = slotFixtureBinding("b", s.now()).ServerKeyFingerprint, slotFixtureBinding("b", s.now()).ScopeFingerprint
			if mode != "same pair across routes" {
				other.Instance = evidenceOtherGeneration
			}
			// An unlinked physical row running another workload remains charged.
			peer := InventoryResource{ID: "other-runner", Pool: "api", Host: "b", Resources: Resources{CPU: 500, RAM: 512, Disk: 4096, Runners: 1}, State: "running"}
			rows := []InventoryResource{row, peer}
			ids := map[string]slotInventoryFixture{row.ID: {first, "runner-1", 1}, peer.ID: {other, peer.ID, 1}}
			if mode == "opaque first observation" {
				reconcileSlotInventory(t, s, rows, map[string]slotInventoryFixture{row.ID: {first, "runner-1", 1}})
			}
			fence := reconcileSlotInventory(t, s, rows, ids)
			if mode == "retained unknown" {
				fence = reconcileSlotInventory(t, s, rows[:1], ids)
			}
			if mode == "changed bounds" {
				rows[1].Resources.RAM++
				peer.Resources.RAM++
				fence = reconcileSlotInventory(t, s, rows, ids)
			}
			if mode == "same name rebound" || mode == "same pair different creation" {
				replacement := other
				birth := int64(1)
				if mode == "same name rebound" {
					replacement.Generation = "44444444-4444-4444-8444-444444444444"
				} else {
					birth = 2
				}
				ids[peer.ID] = slotInventoryFixture{replacement, peer.ID, birth}
				reconcileSlotInventory(t, s, rows, ids, ErrReconciliation)
				ids[peer.ID] = slotInventoryFixture{other, peer.ID, 1}
				fence = reconcileSlotInventory(t, s, rows, ids, ErrReconciliation)
			}
			s = reopenSlotReuseStore(t, s)
			defer s.Close()
			e := slotReuseFixtureEvidence(req, fence.Epoch, s.now())
			if mode == "present after teardown" {
				e.Teardown.ObservedAt = e.Settlement.ObservedAt
			}
			prior := sqliteSnapshot(t, s).Allocated
			decision, err := s.ReleaseRetiredSlot(ctx, req, e)
			after := sqliteSnapshot(t, s).Allocated
			if mode == "distinct active" {
				if err != nil || decision.Replay || after.Global != peer.Resources || after.Hosts[peer.Host] != peer.Resources ||
					after.Pools[peer.Pool] != peer.Resources || after.Hosts[row.Host] != (Resources{}) {
					t.Fatalf("independent charge: %+v %+v %v", decision, after, err)
				}
				replay, err := s.ReleaseRetiredSlot(ctx, req, e)
				if err != nil || !replay.Replay || slotReuseCharged(t, s) != after.Global {
					t.Fatalf("replay changed another runner's charge: %+v %v", replay, err)
				}
			} else {
				expected := ErrConflict
				if mode == "same name rebound" || mode == "same pair different creation" {
					expected = ErrReconciliation
					if sqliteSnapshot(t, s).Ready {
						t.Fatal("under-counted replacement opened admission")
					}
					if _, err := s.Admit(ctx, request("new-charge")); !errors.Is(err, ErrReconciliation) {
						t.Fatalf("replacement admitted new charge: %v", err)
					}
				}
				if !errors.Is(err, expected) || after.Global != prior.Global || after.Global.Runners != 2 ||
					after.Hosts[peer.Host] != peer.Resources || after.Hosts[row.Host] != req.Resources {
					t.Fatalf("conflicting charge: %+v %+v %v", decision, after, err)
				}
				assertNoSlotReuseCredit(t, decision)
			}
		})
	}
}

func TestSlotReuseMigrationPreservesUnknownHistoryAndFencesOldInventory(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := t.Context()
	s, req, epoch := slotReuseHold(t)
	first := slotReuseFixtureEvidence(req, epoch, s.now())
	d, err := s.ReleaseRetiredSlot(ctx, req, first)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild the v30 format with its immutable release, a charged orphan, and
	// an in-flight fence, before opening with the new writer.
	peer := InventoryResource{ID: "old-runner", Host: "a", Pool: "app", Resources: req.Resources, State: "idle"}
	reconcileSQLite(t, s, []InventoryResource{peer})
	old, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP INDEX slot_inventory_epoch; DROP INDEX slot_inventory_pair; DROP INDEX slot_inventory_id;
 DROP INDEX slot_inventory_conflicts; DROP INDEX slot_release_pair; DROP INDEX slot_release_id;
 ALTER TABLE inventory DROP COLUMN slot_subject; ALTER TABLE inventory DROP COLUMN physical_scope;
 ALTER TABLE inventory DROP COLUMN physical_pair; ALTER TABLE inventory DROP COLUMN physical_created_at; ALTER TABLE inventory DROP COLUMN identity_epoch;
 ALTER TABLE inventory DROP COLUMN last_seen_at; ALTER TABLE inventory DROP COLUMN incarnation_conflict;
 ALTER TABLE releases DROP COLUMN physical_pair; ALTER TABLE releases DROP COLUMN scoped_resource_id;
 PRAGMA user_version=30;`); err != nil {
		t.Fatal(err)
	}
	s = reopenSlotReuseStore(t, s)
	defer s.Close()
	if sqliteSnapshot(t, s).Ready || slotReuseCharged(t, s) != req.Resources {
		t.Fatal("upgrade opened admission or changed the preexisting charge")
	}
	if err := s.CompleteReconciliation(ctx, old, InventorySnapshot{}); !errors.Is(err, ErrFence) {
		t.Fatal(err)
	}
	replay, err := s.ReleaseRetiredSlot(ctx, req, first)
	if err != nil || !replay.Replay || replay.EvidenceDigest != d.EvidenceDigest {
		t.Fatalf("lost historical replay: %+v %v", replay, err)
	}
	var inventoryEpoch int64
	var historyPair string
	if err := s.db.QueryRow("SELECT identity_epoch FROM inventory WHERE resource_id=?", peer.ID).Scan(&inventoryEpoch); err != nil || inventoryEpoch != 0 {
		t.Fatal("upgraded legacy inventory became identified", err)
	}
	if err := s.db.QueryRow("SELECT physical_pair FROM releases WHERE request_key=?", req.Key).Scan(&historyPair); err != nil || historyPair != "" {
		t.Fatal("upgraded tombstone lost legacy hold", err)
	}
}

func TestSlotReuseFinalClockRollsBackEveryWrite(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	for _, tc := range []struct {
		name   string
		offset time.Duration
		want   error
	}{
		{"clock moved backward", -time.Nanosecond, ErrSlotReuseEvidence},
		{"inventory expired", 2 * time.Minute, ErrReconciliation},
		{"evidence expired", SlotReuseMaxEvidenceAge + time.Second, ErrSlotReuseEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, req, epoch := slotReuseHold(t)
			ctx := t.Context()
			clock := s.now
			at := clock()
			e := slotReuseFixtureEvidence(req, epoch, at)
			reads := 0
			s.now = func() time.Time {
				reads++
				if reads == 1 {
					return at
				}
				return at.Add(tc.offset)
			}
			d, err := s.ReleaseRetiredSlot(ctx, req, e)
			s.now = clock
			if !errors.Is(err, tc.want) || reads != 2 {
				t.Fatalf("final validity: %+v %v, clock reads %d", d, err, reads)
			}
			assertNoSlotReuseCredit(t, d)
			s = reopenSlotReuseStore(t, s)
			defer s.Close()
			state := sqliteSnapshot(t, s)
			if state.Allocated.Global != req.Resources || len(state.Inventory) != 1 {
				t.Fatal("lost charge on rollback")
			}
			for _, event := range state.Audit {
				if event.Kind == "slot_reuse_released" {
					t.Fatal("release audit survived rollback")
				}
			}
			if _, err := s.LookupRelease(ctx, req.Key); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("tombstone survived rollback", err)
			}
			if _, err := s.ReleaseRetiredSlot(ctx, req, e); err != nil {
				t.Fatal(err)
			}
			s.now = func() time.Time { t.Fatal("historical replay sampled the release clock"); return time.Time{} }
			if replay, err := s.ReleaseRetiredSlot(ctx, req, e); err != nil || !replay.Replay {
				t.Fatal(replay, err)
			}
		})
	}
}

func TestSlotReuseTwoLinkedIncarnationsRetireIndependently(t *testing.T) {
	registerSlotReuseFixtureSource(t)
	ctx := t.Context()
	s, first, _ := slotReuseHold(t)
	second := first
	second.Host, second.Pool = "b", "api"
	second.Key = evidenceDigest([]byte("controller-1\x00api\x00runner-2"))
	if _, err := s.Admit(ctx, second); err != nil {
		t.Fatal(err)
	}
	secondEvidence := func(epoch int64) SlotReuseEvidence {
		e := slotReuseFixtureEvidence(second, epoch, s.now())
		binding := slotFixtureBinding(second.Host, s.now())
		e.Identity.Daemon, e.Identity.Project = binding.ServerKeyFingerprint, binding.ScopeFingerprint
		e.Identity.Instance = evidenceOtherGeneration
		e.Identity.ResourceID = slotReuseResourceID(e.Identity)
		digest := slotReuseIdentityDigest(e.Identity)
		e.Fence.IdentityDigest, e.Teardown.IdentityDigest, e.Settlement.IdentityDigest = digest, digest, digest
		return e
	}
	one, two := slotReuseFixtureEvidence(first, 0, s.now()).Identity, secondEvidence(0).Identity
	rows := []InventoryResource{
		{ID: one.ResourceID, Host: first.Host, Pool: first.Pool, Resources: first.Resources, ReservationKey: first.Key, State: "deleting"},
		{ID: two.ResourceID, Host: second.Host, Pool: second.Pool, Resources: second.Resources, ReservationKey: second.Key, State: "running"},
	}
	ids := map[string]slotInventoryFixture{one.ResourceID: {one, "runner-1", 1}, two.ResourceID: {two, "runner-2", 1}}
	fence := reconcileSlotInventory(t, s, rows, ids)
	prior := slotReuseFixtureEvidence(first, fence.Epoch, s.now())
	if d, err := s.ReleaseRetiredSlot(ctx, first, prior); err != nil || d.Replay || slotReuseCharged(t, s) != second.Resources {
		t.Fatalf("retiring the first changed the second charge: %+v %v", d, err)
	}
	if _, err := s.MarkDeleting(ctx, second); err != nil {
		t.Fatal(err)
	}
	rows[1].State = "deleting"
	fence = reconcileSlotInventory(t, s, rows[1:], ids)
	s = reopenSlotReuseStore(t, s)
	defer s.Close()
	if d, err := s.ReleaseRetiredSlot(ctx, second, secondEvidence(fence.Epoch)); err != nil || d.Replay || slotReuseCharged(t, s) != (Resources{}) {
		t.Fatalf("independent retirement failed: %+v %v", d, err)
	}
	if replay, err := s.ReleaseRetiredSlot(ctx, first, prior); err != nil || !replay.Replay {
		t.Fatal(replay, err)
	}
}

func TestSlotReuseIndexedConflictAndHoldLookups(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	for _, tc := range []struct {
		sql  string
		args []any
	}{
		{"SELECT physical_scope FROM inventory WHERE reservation_key=?", []any{"key"}},
		{"DELETE FROM inventory WHERE reservation_key=?", []any{"key"}},
		{slotConflictQuery, []any{1, 1, "pair", "a", "id", "id", "a", "pair", "id"}},
	} {
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+tc.sql, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		var lookups int
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(detail, "SCAN inventory") || strings.Contains(detail, "SCAN releases") {
				t.Fatalf("critical section scans the ledger: %s", detail)
			}
			if strings.Contains(detail, "SEARCH inventory") || strings.Contains(detail, "SEARCH releases") {
				lookups++
				t.Log(detail)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if lookups == 0 {
			t.Fatal("no index lookup", tc.sql)
		}
	}
}

func TestSlotReuseReconciliationConsumesOnlyPostBarrierListedIdentity(t *testing.T) {
	ctx := t.Context()
	s, _, fake := releaseTestFixture(t)
	physical, err := fake.Client.PhysicalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := fake.Client.PinPhysicalIdentity(physical); err != nil {
		t.Fatal(err)
	}
	second := request(evidenceDigest([]byte("controller-1\x00app\x00runner-2")))
	if _, err := s.Admit(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Client.Create(ctx, incusapi.InstanceSpec{Name: "runner-2", Type: "container", Profiles: []string{"runner-base"}, ImageFingerprint: strings.Repeat("a", 64), CPUMillis: second.Resources.CPU, RAMMiB: second.Resources.RAM, UserData: "fixture", ControllerID: "controller-1", PoolID: second.Pool, ReservationKey: second.Key}); err != nil {
		t.Fatal(err)
	}
	collect := func(fence Fence) InventoryResource {
		list, err := fake.Client.Instances(incusapi.WithInventoryFence(ctx, fence.Nonce))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range list {
			if row.Name == "runner-2" {
				return (InventoryResource{ID: row.Name, Host: second.Host, Pool: second.Pool, ReservationKey: second.Key, Resources: second.Resources, State: "running"}).WithListedIdentity(row.ListedIdentity)
			}
		}
		t.Fatal("missing fixture runner")
		return InventoryResource{}
	}
	config := sqliteSnapshot(t, s).Config
	prior, err := s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old := collect(prior)
	// Deliberately regress the writer clock across Begin. Timestamp checks
	// alone now permit the old sample when the clock recovers.
	clock := s.now
	s.now = func() time.Time { return old.listed.StartedAt.Add(-time.Second) }
	fence, err := s.BeginReconciliation(ctx)
	s.now = clock
	if err != nil {
		t.Fatal(err)
	}
	snap := InventorySnapshot{Hosts: observations(config, s.now()), Resources: []InventoryResource{old}}
	if err := s.CompleteReconciliation(ctx, fence, snap); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("relabeled old physical observation: %v", err)
	}
	fence, err = s.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := collect(fence)
	if err := s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(config, s.now()), Resources: []InventoryResource{row}}); err != nil {
		t.Fatal(err)
	}
	var scope, pair string
	var epoch int64
	if err := s.db.QueryRow("SELECT physical_scope,physical_pair,identity_epoch FROM inventory WHERE reservation_key=?", second.Key).Scan(&scope, &pair, &epoch); err != nil {
		t.Fatal(err)
	}
	if !bindingFingerprint(scope) || epoch != fence.Epoch || pair != evidenceInstanceUUID+"/"+evidenceGeneration {
		t.Fatalf("lost pinned list identity: scope valid=%v pair=%s epoch=%d", bindingFingerprint(scope), pair, epoch)
	}
}
