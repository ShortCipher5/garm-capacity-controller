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

func TestReleaseConfirmedAbsenceFreesChargeAndTombstonesIdentity(t *testing.T) {
	ctx := context.Background()
	store, req, fake := releaseTestFixture(t)
	identity := releaseIdentity(t, fake, req)
	fake.SetGuestVisible(false)
	absence, err := fake.Client.ObserveProjectAbsence(ctx, identity)
	if err != nil || !absence.Valid() {
		t.Fatalf("absence %v %v", absence.Valid(), err)
	}
	before := sqliteSnapshot(t, store)
	if before.Allocated.Global.Runners != 1 || len(before.Inventory) != 1 {
		t.Fatalf("fixture not charged: %+v", before.Allocated)
	}
	if _, err := store.MarkDeleting(ctx, req); err != nil {
		t.Fatal(err)
	}
	release, err := store.ReleaseConfirmed(ctx, req, ReleaseEvidence{Absence: absence})
	if err != nil || release.ReleasedAt.IsZero() || !bindingFingerprint(release.AbsenceDigest) {
		t.Fatalf("release %+v %v", release, err)
	}
	after := sqliteSnapshot(t, store)
	if after.Allocated.Global.Runners != 0 || after.Allocated.Global.CPU != 0 || len(after.Inventory) != 0 {
		t.Fatalf("release did not free charge: %+v", after.Allocated)
	}
	if _, ok := after.Reservations[req.Key]; !ok {
		t.Fatal("reservation history lost")
	}
	for _, replay := range []func() error{
		func() error {
			_, err := store.ReleaseConfirmed(ctx, req, ReleaseEvidence{Absence: absence})
			return err
		},
		func() error { _, err := store.MarkDeleting(ctx, req); return err },
		func() error { _, err := store.Admit(ctx, req); return err },
	} {
		if !errors.Is(replay(), ErrDenied) {
			t.Fatal("released identity was not refused")
		}
	}
	got, err := store.LookupRelease(ctx, req.Key)
	if err != nil || got.Request != req || !got.ReleasedAt.Equal(release.ReleasedAt) || got.AbsenceDigest != release.AbsenceDigest {
		t.Fatalf("tombstone %+v %v", got, err)
	}
	var audits int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM audit WHERE kind='released_confirmed_absence'").Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("release audit %d %v", audits, err)
	}
	var record []byte
	if err := store.db.QueryRow("SELECT record FROM releases WHERE request_key=?", req.Key).Scan(&record); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-userdata", "user.user-data", "PRIVATE KEY", "https://"} {
		if strings.Contains(string(record), secret) {
			t.Fatal("release record leaked a secret")
		}
	}
}

func TestReleaseConfirmedFailsClosedWithoutDeletingMarkOrEvidence(t *testing.T) {
	ctx := context.Background()
	store, req, fake := releaseTestFixture(t)
	if _, err := store.ReleaseConfirmed(ctx, req, ReleaseEvidence{}); !errors.Is(err, ErrReleaseEvidence) {
		t.Fatalf("zero evidence accepted: %v", err)
	}
	identity := releaseIdentity(t, fake, req)
	fake.SetGuestVisible(false)
	absence, err := fake.Client.ObserveProjectAbsence(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	evidence := ReleaseEvidence{Absence: absence}
	if _, err := store.ReleaseConfirmed(ctx, req, evidence); !errors.Is(err, ErrDenied) {
		t.Fatalf("release without deleting mark: %v", err)
	}
	if marked, err := store.MarkDeleting(ctx, req); err != nil || marked.State != "deleting" {
		t.Fatalf("mark %+v %v", marked, err)
	}
	if again, err := store.MarkDeleting(ctx, req); err != nil || again.State != "deleting" {
		t.Fatalf("mark replay %+v %v", again, err)
	}
	var audits int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM audit WHERE kind='deleting_marked'").Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("mark replay audited %d %v", audits, err)
	}
	other := request(evidenceDigest([]byte("controller-1\x00app\x00runner-2")))
	if _, err := store.ReleaseConfirmed(ctx, other, evidence); !errors.Is(err, ErrReleaseEvidence) {
		t.Fatalf("foreign-bound evidence accepted: %v", err)
	}
	if _, err := store.ReleaseConfirmed(ctx, req, ReleaseEvidence{}); !errors.Is(err, ErrReleaseEvidence) {
		t.Fatalf("invalid evidence accepted after mark: %v", err)
	}
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseConfirmed(ctx, req, evidence); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("release while barrier open: %v", err)
	}
	state, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(state.Config, time.Now())}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseConfirmed(ctx, req, evidence); err != nil {
		t.Fatal(err)
	}
	after := sqliteSnapshot(t, store)
	if after.Allocated.Global.Runners != 0 {
		t.Fatalf("charge not freed: %+v", after.Allocated)
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
