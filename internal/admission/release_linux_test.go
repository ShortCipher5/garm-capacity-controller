package admission

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func releaseTestFixture(t *testing.T) (*SQLiteStore, Request, *testsupport.FakeIncus) {
	t.Helper()
	req := request(evidenceDigest([]byte("controller-1\x00app\x00runner-1")))
	config := fixture(time.Now())
	delete(config.Hosts, "b")
	for id, pool := range config.Pools {
		pool.Hosts = []string{"a"}
		config.Pools[id] = pool
	}
	store := sqliteFixtureStore(t, config, false)
	fake := testsupport.NewFakeIncus(t, "release-daemon")
	ctx := context.Background()
	physical, err := fake.Client.PhysicalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding := HostBinding{Host: "a", ServerKeyFingerprint: physical.ServerKeyFingerprint(), OriginFingerprint: physical.OriginFingerprint(), ScopeFingerprint: physical.ScopeFingerprint(), ServerName: physical.ServerName(), ServerVersion: physical.ServerVersion(), ObservedAt: time.Now()}
	if err := store.BindHostIdentities(ctx, fence, []HostBinding{binding}); err != nil {
		t.Fatal(err)
	}
	binding, err = store.LookupHostBinding(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(config, time.Now())}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(ctx, req); err != nil {
		t.Fatal(err)
	}
	permit, err := store.ClaimDispatchBound(ctx, req, binding)
	if err != nil {
		t.Fatal(err)
	}
	created, err := fake.Client.Create(ctx, incusapi.InstanceSpec{Name: "runner-1", Type: "container", Profiles: []string{"runner-base"}, ImageFingerprint: strings.Repeat("a", 64), CPUMillis: 1000, RAMMiB: 2048, UserData: "#cloud-config\nfixture-userdata", ControllerID: "controller-1", PoolID: req.Pool, ReservationKey: req.Key})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDispatchOutcome(ctx, *permit, DispatchReceipt{State: "confirmed", OperationID: created.OperationID, ResourceID: created.ResourceID}); err != nil {
		t.Fatal(err)
	}
	reconcileSQLite(t, store, []InventoryResource{{ID: "runner-1", Host: "a", Pool: "app", ReservationKey: req.Key, Resources: req.Resources, State: "idle"}})
	return store, req, fake
}

func releaseIdentity(t *testing.T, fake *testsupport.FakeIncus, req Request) incusapi.VerifiedInstanceIdentity {
	t.Helper()
	identity, err := fake.Client.ObserveInstanceIdentity(context.Background(), incusapi.IdentityExpectation{Name: "runner-1", ControllerID: "controller-1", PoolID: req.Pool, ReservationKey: req.Key, CPUMillis: 1000, RAMMiB: 2048})
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestProjectAbsenceCannotAuthorizeReleaseEvenAfterRestart(t *testing.T) {
	ctx := context.Background()
	store, req, fake := releaseTestFixture(t)
	identity := releaseIdentity(t, fake, req)
	fake.SetGuestVisible(false)
	absence, err := fake.Client.ObserveProjectAbsence(ctx, identity)
	if err != nil || !absence.Valid() || len(absence.Blockers()) == 0 {
		t.Fatalf("fixture observation %v %v", absence.Valid(), err)
	}
	if _, err := store.MarkDeleting(ctx, req); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := store.ReleaseConfirmed(ctx, req, ReleaseEvidence{Absence: absence}); !errors.Is(err, ErrReleaseEvidence) {
			t.Fatalf("unqualified observation released: %v", err)
		}
		if _, err := store.ReleaseConfirmed(ctx, req, ReleaseEvidence{}); !errors.Is(err, ErrReleaseEvidence) {
			t.Fatalf("empty observation released: %v", err)
		}
	}
	after := sqliteSnapshot(t, store)
	if after.Allocated.Global.Runners != 1 || len(after.Inventory) != 1 || after.Reservations[req.Key].State != "deleting" {
		t.Fatalf("blocked release changed accounting: %+v", after)
	}
	if _, err := store.LookupRelease(ctx, req.Key); err == nil {
		t.Fatal("unqualified release persisted")
	}
	// Closing/reopening is not authority to free an uncertain deletion.
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(path, after.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ReleaseConfirmed(ctx, req, ReleaseEvidence{Absence: absence}); !errors.Is(err, ErrReleaseEvidence) {
		t.Fatalf("restart authorized release: %v", err)
	}
	if got := sqliteSnapshot(t, reopened); got.Allocated.Global.Runners != 1 {
		t.Fatal("restart lost charge")
	}
}

func TestMarkDeletingRefusesUnlinkedOrUnknownReservations(t *testing.T) {
	ctx := context.Background()
	store := sqliteFixtureStore(t, fixture(time.Now()), true)
	if _, err := store.MarkDeleting(ctx, request("missing")); !errors.Is(err, ErrDenied) {
		t.Fatalf("unknown reservation marked: %v", err)
	}
	req := request(evidenceDigest([]byte("controller-1\x00app\x00runner-1")))
	if _, err := store.Admit(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDeleting(ctx, req); !errors.Is(err, ErrDenied) {
		t.Fatalf("unlinked reservation marked: %v", err)
	}
	if _, err := store.CancelUndispatched(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDeleting(ctx, req); !errors.Is(err, ErrDenied) {
		t.Fatalf("cancelled reservation marked: %v", err)
	}
}
