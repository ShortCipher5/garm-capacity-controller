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

func TestFacadeDeleteWithoutQualifiedAuthorityNeverDispatches(t *testing.T) {
	ctx := context.Background()
	fx := deleteTestFixture(t)
	for _, state := range []string{"idle", "busy", "deleting"} {
		t.Run(state, func(t *testing.T) {
			rows := garmRows{{Name: "runner-1", ProviderID: "runner-1", PoolID: "pool-a", State: state, Idle: state == "idle"}}
			if err := fx.facade.Reconcile(ctx, rows); err != nil {
				t.Fatal(err)
			}
			before, err := fx.store.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				receipt, err := fx.facade.Delete(ctx, "controller-a", "pool-a", "runner-1")
				if !errors.Is(err, ErrUnavailable) || receipt.Outcome != "" {
					t.Fatalf("unqualified delete %+v %v", receipt, err)
				}
			}
			after, err := fx.store.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if chargedRunners(t, fx.store) != 1 || fx.fake.Deletes.Load() != 0 || fx.fake.DeletePolls.Load() != 0 {
				t.Fatal("blocked retirement changed charge or touched backend")
			}
			if after.Reservations[key("controller-a", "pool-a", "runner-1")].State != before.Reservations[key("controller-a", "pool-a", "runner-1")].State {
				t.Fatal("blocked request mutated lifecycle")
			}
			if _, err := fx.store.LookupRelease(ctx, key("controller-a", "pool-a", "runner-1")); err == nil {
				t.Fatal("blocked retirement wrote release")
			}
		})
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
