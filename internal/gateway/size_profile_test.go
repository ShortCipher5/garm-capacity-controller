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
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

func sizeProfileStore(t *testing.T, budget admission.Resources) *admission.SQLiteStore {
	t.Helper()
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func sizeProfileFacade(t *testing.T, store *admission.SQLiteStore) (*Facade, *backendFixture) {
	t.Helper()
	identity := testsupport.Identity(t, "fixture-host-a")
	backend := &backendFixture{instances: map[string]incusapi.Instance{}, identity: identity.Client, identityConfiguration: identity.Configuration}
	policy := PoolPolicy{Host: "host-a", SizeProfile: "small", Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}
	f, err := New(context.Background(), store, "controller-a", map[string]PoolPolicy{"pool-a": policy}, map[string]Backend{"host-a": backend})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Reconcile(context.Background(), garmRows{}); err != nil {
		t.Fatal(err)
	}
	return f, backend
}

// Profile-sized admission must charge the profile's CPU/RAM/disk end to end:
// reservation, host/global/pool disk budgets, the Incus spec and allocation.
func TestSizeProfileAdmissionChargesDiskAcrossLedgerAndSpec(t *testing.T) {
	ctx := context.Background()
	store := sizeProfileStore(t, admission.Resources{CPU: 4000, RAM: 8192, Disk: 7680, Runners: 4})
	if err := store.ProfileUpsert(ctx, "small", 2000, 2048, 5120); err != nil {
		t.Fatal(err)
	}
	f, b := sizeProfileFacade(t, store)
	preflight(t, f, "runner-1", "pool-a")
	result, err := f.Create(ctx, bootstrap("runner-1", "pool-a"))
	if err != nil || result.Name != "runner-1" {
		t.Fatalf("create %+v %v", result, err)
	}
	if b.lastSpec.CPUMillis != 2000 || b.lastSpec.RAMMiB != 2048 || b.lastSpec.DiskMiB != 5120 {
		t.Fatalf("profile resources not dispatched: %+v", b.lastSpec)
	}
	state, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Allocated.Global.Disk != 5120 || state.Allocated.Hosts["host-a"].Disk != 5120 || state.Allocated.Pools["pool-a"].Disk != 5120 || state.Allocated.Global.CPU != 2000 {
		t.Fatalf("profile disk not charged: %+v", state.Allocated)
	}
	// CPU/RAM/runners still fit twice; only the disk budget defers the second.
	request := garmprotocol.Request{Protocol: garmprotocol.Version, Name: "runner-2", PoolID: "pool-a"}
	exit, data, err := f.Admit(ctx, request)
	if err != nil || exit != garmprotocol.DeferredExitCode {
		t.Fatalf("disk budget did not defer %d %s %v", exit, data, err)
	}
	if b.calls.Load() != 1 {
		t.Fatal("deferred admission dispatched")
	}
}

// Startup must refuse a SizeProfile that is unknown or spec-ineligible;
// after startup, deletion must fail closed at admit and dispatch while the
// existing charge stays durable.
func TestSizeProfileMissingFailsClosedAndRetainsCharge(t *testing.T) {
	ctx := context.Background()
	store := sizeProfileStore(t, admission.Resources{CPU: 4000, RAM: 8192, Disk: 10240, Runners: 4})
	if err := store.ProfileUpsert(ctx, "small", 1500, 2048, 5120); !errors.Is(err, admission.ErrDenied) {
		t.Fatalf("fractional vCPU profile accepted %v", err)
	}
	if err := store.ProfileUpsert(ctx, "small", 2000, 2048, 5120); err != nil {
		t.Fatal(err)
	}
	f, b := sizeProfileFacade(t, store)
	preflight(t, f, "runner-1", "pool-a")
	if err := store.ProfileDelete(ctx, "small"); err != nil {
		t.Fatal(err)
	}
	request := garmprotocol.Request{Protocol: garmprotocol.Version, Name: "runner-2", PoolID: "pool-a"}
	if _, _, err := f.Admit(ctx, request); !errors.Is(err, ErrPolicy) {
		t.Fatalf("deleted profile admitted %v", err)
	}
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("deleted profile dispatched %v", err)
	}
	if b.calls.Load() != 0 {
		t.Fatal("deleted profile reached backend")
	}
	state, err := store.Snapshot(ctx)
	if err != nil || state.Allocated.Global.Disk != 5120 || state.Allocated.Global.Runners != 1 {
		t.Fatalf("charge released on profile deletion %+v %v", state.Allocated, err)
	}
	reservation, ok := state.Reservations[key("controller-a", "pool-a", "runner-1")]
	if !ok || reservation.Request.Resources.Disk != 5120 {
		t.Fatalf("reservation lost its disk charge %+v", reservation)
	}
}

// Profile edits are read from the current store at admit/dispatch time, so
// they take effect without a facade restart and revoke in-flight plans.
func TestSizeProfileEditAppliesWithoutRestart(t *testing.T) {
	ctx := context.Background()
	store := sizeProfileStore(t, admission.Resources{CPU: 4000, RAM: 8192, Disk: 10240, Runners: 4})
	if err := store.ProfileUpsert(ctx, "small", 2000, 2048, 5120); err != nil {
		t.Fatal(err)
	}
	f, _ := sizeProfileFacade(t, store)
	preflight(t, f, "runner-1", "pool-a")
	if err := store.ProfileUpsert(ctx, "small", 1000, 1024, 2560); err != nil {
		t.Fatal(err)
	}
	preflight(t, f, "runner-2", "pool-a")
	state, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reservation := state.Reservations[key("controller-a", "pool-a", "runner-2")].Request
	if reservation.Resources != (admission.Resources{CPU: 1000, RAM: 1024, Disk: 2560, Runners: 1}) {
		t.Fatalf("edited profile not charged %+v", reservation.Resources)
	}
	// The pre-edit plan no longer matches current policy: dispatch fails closed
	// and the charge stays until authoritative release.
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale profile plan dispatched %v", err)
	}
}

// A facade must never start against a policy referencing a profile the store
// does not have: resolution can only fail closed on later edits, not on a
// facade that was born misconfigured.
func TestSizeProfileUnknownAtStartupFailsPolicy(t *testing.T) {
	store := sizeProfileStore(t, admission.Resources{CPU: 4000, RAM: 8192, Disk: 10240, Runners: 4})
	identity := testsupport.Identity(t, "fixture-host-b")
	backend := &backendFixture{instances: map[string]incusapi.Instance{}, identity: identity.Client, identityConfiguration: identity.Configuration}
	policy := PoolPolicy{Host: "host-a", SizeProfile: "ghost", Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}
	if _, err := New(context.Background(), store, "controller-a", map[string]PoolPolicy{"pool-a": policy}, map[string]Backend{"host-a": backend}); !errors.Is(err, ErrPolicy) {
		t.Fatalf("unknown size profile accepted %v", err)
	}
}
