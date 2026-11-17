package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

type deleteFixture struct {
	facade *Facade
	store  *admission.SQLiteStore
	fake   *testsupport.FakeIncus
}

func deleteTestFixture(t *testing.T) *deleteFixture {
	t.Helper()
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}, "pool-b": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fake := testsupport.NewFakeIncus(t, "delete-daemon")
	policy := PoolPolicy{Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}
	f, err := New(context.Background(), store, "controller-a", map[string]PoolPolicy{"pool-a": policy, "pool-b": policy}, map[string]Backend{"host-a": fake.Client})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Reconcile(context.Background(), garmRows{}); err != nil {
		t.Fatal(err)
	}
	preflight(t, f, "runner-1", "pool-a")
	if _, err := f.Create(context.Background(), bootstrap("runner-1", "pool-a")); err != nil {
		t.Fatal(err)
	}
	rows := garmRows{{Name: "runner-1", ProviderID: "runner-1", PoolID: "pool-a", State: "running", Idle: true}}
	if err := f.Reconcile(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	return &deleteFixture{facade: f, store: store, fake: fake}
}

func chargedRunners(t *testing.T, store *admission.SQLiteStore) int64 {
	t.Helper()
	state, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state.Allocated.Global.Runners
}

func TestFacadeDeleteConfirmedAbsenceReleasesBudgetAndReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fx := deleteTestFixture(t)
	if charged := chargedRunners(t, fx.store); charged != 1 {
		t.Fatalf("fixture charge %d", charged)
	}
	receipt, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-1")
	if err != nil || receipt.Outcome != DeletionReleased || receipt.ReleasedAt.IsZero() {
		t.Fatalf("delete %+v %v", receipt, err)
	}
	if charged := chargedRunners(t, fx.store); charged != 0 {
		t.Fatalf("charge not released %d", charged)
	}
	state, err := fx.store.Snapshot(ctx)
	if err != nil || len(state.Inventory) != 0 {
		t.Fatalf("inventory retained %+v %v", state.Inventory, err)
	}
	if _, ok := state.Reservations[key("controller-a", "pool-a", "runner-1")]; !ok {
		t.Fatal("reservation history lost")
	}
	if release, err := fx.store.LookupRelease(ctx, key("controller-a", "pool-a", "runner-1")); err != nil || release.Request.Pool != "pool-a" {
		t.Fatalf("release tombstone %+v %v", release, err)
	}
	if fx.fake.Deletes.Load() != 1 || fx.fake.DeletePolls.Load() != 2 {
		t.Fatalf("unexpected backend calls deletes=%d polls=%d", fx.fake.Deletes.Load(), fx.fake.DeletePolls.Load())
	}
	replay, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-1")
	if err != nil || replay.Outcome != DeletionAlreadyReleased || !replay.ReleasedAt.Equal(receipt.ReleasedAt) {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if fx.fake.Deletes.Load() != 1 || chargedRunners(t, fx.store) != 0 {
		t.Fatal("replay repeated the backend delete or re-charged")
	}
}

func TestFacadeDeleteTransportFailureKeepsChargeAndDeletingState(t *testing.T) {
	ctx := context.Background()
	fx := deleteTestFixture(t)
	fx.fake.SetDeleteFaulty(true)
	receipt, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-1")
	if err != nil || receipt.Outcome != DeletionStillCharged {
		t.Fatalf("delete failure %+v %v", receipt, err)
	}
	if charged := chargedRunners(t, fx.store); charged != 1 {
		t.Fatalf("failed delete released charge %d", charged)
	}
	state, err := fx.store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reservation := state.Reservations[key("controller-a", "pool-a", "runner-1")]; reservation.State != "deleting" {
		t.Fatalf("state %q", reservation.State)
	}
	if fx.fake.Deletes.Load() != 0 {
		t.Fatal("faulty delete counted as accepted")
	}
	// Operator re-dispatch after the fault clears: the mark replays and the
	// whole flow completes without a second durable mark.
	fx.fake.SetDeleteFaulty(false)
	receipt, err = fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-1")
	if err != nil || receipt.Outcome != DeletionReleased {
		t.Fatalf("re-dispatch %+v %v", receipt, err)
	}
	if charged := chargedRunners(t, fx.store); charged != 0 || fx.fake.Deletes.Load() != 1 {
		t.Fatalf("re-dispatch outcome charge=%d deletes=%d", charged, fx.fake.Deletes.Load())
	}
}

func TestFacadeDeleteAbsenceNotConfirmedKeepsCharge(t *testing.T) {
	ctx := context.Background()
	fx := deleteTestFixture(t)
	fx.fake.SetAbsenceLying(true)
	receipt, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-1")
	if err != nil || receipt.Outcome != DeletionStillCharged {
		t.Fatalf("lying absence %+v %v", receipt, err)
	}
	if charged := chargedRunners(t, fx.store); charged != 1 {
		t.Fatalf("unconfirmed absence released charge %d", charged)
	}
	state, err := fx.store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reservation := state.Reservations[key("controller-a", "pool-a", "runner-1")]; reservation.State != "deleting" {
		t.Fatalf("state %q", reservation.State)
	}
	if _, err := fx.store.LookupRelease(ctx, key("controller-a", "pool-a", "runner-1")); err == nil {
		t.Fatal("release tombstone written without confirmed absence")
	}
}

func TestFacadeDeleteRestartDuringDeletingKeepsChargeAndRecoversByRedispatch(t *testing.T) {
	ctx := context.Background()
	fx := deleteTestFixture(t)
	fx.fake.SetWaitFaulty(true)
	receipt, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-1")
	if err != nil || receipt.Outcome != DeletionStillCharged {
		t.Fatalf("wait failure %+v %v", receipt, err)
	}
	// Restart: a brand new Facade over the same store must keep the charge.
	policy := PoolPolicy{Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}
	restarted, err := New(ctx, fx.store, "controller-a", map[string]PoolPolicy{"pool-a": policy, "pool-b": policy}, map[string]Backend{"host-a": fx.fake.Client})
	if err != nil {
		t.Fatal(err)
	}
	rows := garmRows{{Name: "runner-1", ProviderID: "runner-1", PoolID: "pool-a", State: "deleting"}}
	if err := restarted.Reconcile(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if charged := chargedRunners(t, fx.store); charged != 1 {
		t.Fatalf("restart released deleting charge %d", charged)
	}
	state, err := fx.store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reservation := state.Reservations[key("controller-a", "pool-a", "runner-1")]; reservation.State != "deleting" {
		t.Fatalf("restart state %q", reservation.State)
	}
	fx.fake.SetWaitFaulty(false)
	receipt, err = restarted.Delete(ctx, "controller-a", "pool-a", "runner-1")
	if err != nil || receipt.Outcome != DeletionReleased {
		t.Fatalf("post-restart re-dispatch %+v %v", receipt, err)
	}
	if charged := chargedRunners(t, fx.store); charged != 0 {
		t.Fatalf("post-restart release failed charge=%d", charged)
	}
}

func TestFacadeDeleteRefusesWrongIdentityUnknownUnlinkedCancelledAndDrift(t *testing.T) {
	ctx := context.Background()
	fx := deleteTestFixture(t)
	if _, err := fx.facade.Delete(ctx, "controller-b", "pool-a", "runner-1"); !errors.Is(err, ErrPolicy) {
		t.Fatalf("foreign controller %v", err)
	}
	if _, err := fx.facade.Delete(ctx, "controller-a", "pool-nope", "runner-1"); !errors.Is(err, ErrPolicy) {
		t.Fatalf("unknown pool %v", err)
	}
	if _, err := fx.facade.Delete(ctx, "controller-a", "pool-b", "runner-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong pool %v", err)
	}
	if _, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown runner %v", err)
	}
	preflight(t, fx.facade, "runner-2", "pool-a")
	if _, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlinked runner %v", err)
	}
	preflight(t, fx.facade, "runner-3", "pool-a")
	cancelled := garmprotocol.Request{Protocol: garmprotocol.Version, Name: "runner-3", PoolID: "pool-a"}
	if _, err := fx.facade.CancelAdmission(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	receipt, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-3")
	if err != nil || receipt.Outcome != DeletionAlreadyReleased {
		t.Fatalf("cancelled identity %+v %v", receipt, err)
	}
	fx.fake.SetIncarnationDrift(true)
	if _, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("incarnation drift accepted %v", err)
	}
	// runner-1 and the still-admitted runner-2 remain charged; every refusal
	// above made no backend delete call.
	if charged := chargedRunners(t, fx.store); charged != 2 || fx.fake.Deletes.Load() != 0 {
		t.Fatalf("refusals mutated charge/deletes charge=%d deletes=%d", charged, fx.fake.Deletes.Load())
	}
}

func TestFacadeDeleteRefusesBackendWithoutVerifiedObservationCapability(t *testing.T) {
	ctx := context.Background()
	f, store, _, _ := facadeFixture(t)
	preflight(t, f, "runner-1", "pool-a")
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err != nil {
		t.Fatal(err)
	}
	rows := garmRows{{Name: "runner-1", ProviderID: "runner-1", PoolID: "pool-a", State: "running", Idle: true}}
	if err := f.Reconcile(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Delete(ctx, "controller-a", "pool-a", "runner-1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unevidenced backend delete %v", err)
	}
	if charged := chargedRunners(t, store); charged != 1 {
		t.Fatalf("capability refusal released charge %d", charged)
	}
}
