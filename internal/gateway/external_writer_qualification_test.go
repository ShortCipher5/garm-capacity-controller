//go:build external_writer_counterexample

package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

// These opt-in tests demonstrate CURRENT gaps with local fixtures. Their passing
// result confirms the counterexample, not safe deployment. No native daemon,
// infrastructure, credential revocation or physical hardware is accessed.
func externalWriterFixture(t *testing.T, backend Backend, controller string) (*Facade, *admission.SQLiteStore) {
	t.Helper()
	unit := admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}
	config := admission.Config{Global: unit, Hosts: map[string]admission.Host{"host-a": {Budget: unit, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: unit, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	stateDir, identityDir := t.TempDir(), t.TempDir()
	for _, dir := range []string{stateDir, identityDir} {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(stateDir, "ledger.db")
	identity := filepath.Join(identityDir, "identity")
	if err := admission.InitializeSQLite(path, identity, config); err != nil {
		t.Fatal(err)
	}
	store, err := admission.OpenRuntimeSQLite(path, identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policy := PoolPolicy{Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}
	f, err := New(context.Background(), store, controller, map[string]PoolPolicy{"pool-a": policy}, map[string]Backend{"host-a": backend})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Reconcile(context.Background(), garmRows{}); err != nil {
		t.Fatal(err)
	}
	return f, store
}

func TestCounterexampleSeparateRuntimeLedgersCanExceedOneSharedPhysicalBudget(t *testing.T) {
	identity := testsupport.Identity(t, "one-physical-daemon")
	shared := &backendFixture{instances: map[string]incusapi.Instance{}, identity: identity.Client, identityConfiguration: identity.Configuration}
	first, firstStore := externalWriterFixture(t, shared, "controller-a")
	second, secondStore := externalWriterFixture(t, shared, "controller-b")
	preflight(t, first, "runner-first", "pool-a")
	preflight(t, second, "runner-second", "pool-a")
	if _, err := first.Create(context.Background(), bootstrap("runner-first", "pool-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Create(context.Background(), bootstrap("runner-second", "pool-a")); err != nil {
		t.Fatal(err)
	}
	for _, store := range []*admission.SQLiteStore{firstStore, secondStore} {
		state, err := store.Snapshot(context.Background())
		if err != nil || state.Allocated.Global.Runners != 1 {
			t.Fatal("fixture did not respect each ledger's own budget", err)
		}
	}
	rows, err := shared.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || shared.calls.Load() != 2 {
		t.Fatal("counterexample changed: replace this diagnostic with strict shared-authority qualification", len(rows), shared.calls.Load())
	}
	t.Log("CURRENT GAP: two valid independent runtime identities/bindings each admitted one runner; shared fixture has two against one physical allowance")
}

type pausedExternalWriter struct {
	*backendFixture
	entered chan struct{}
	resume  chan struct{}
}

func (b *pausedExternalWriter) Create(ctx context.Context, spec incusapi.InstanceSpec) (incusapi.Created, error) {
	close(b.entered)
	select {
	case <-b.resume:
		return b.backendFixture.Create(ctx, spec)
	case <-ctx.Done():
		return incusapi.Created{}, ctx.Err()
	}
}

func TestCounterexampleLocalBarrierDoesNotFenceAlreadyDeliveredDispatch(t *testing.T) {
	identity := testsupport.Identity(t, "paused-writer-daemon")
	backend := &pausedExternalWriter{backendFixture: &backendFixture{instances: map[string]incusapi.Instance{}, identity: identity.Client, identityConfiguration: identity.Configuration}, entered: make(chan struct{}), resume: make(chan struct{})}
	f, store := externalWriterFixture(t, backend, "controller-a")
	preflight(t, f, "runner-paused", "pool-a")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.Create(ctx, bootstrap("runner-paused", "pool-a")); done <- err }()
	select {
	case <-backend.entered:
	case <-ctx.Done():
		t.Fatal("fixture never delivered its permit")
	}
	if _, err := store.BeginReconciliation(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(backend.resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Ready || state.Allocated.Global.Runners != 1 || backend.calls.Load() != 1 {
		t.Fatal("counterexample fixture changed", state.Ready, backend.calls.Load())
	}
	t.Log("CURRENT GAP: a local durable barrier stopped new claims but could not revoke an already delivered callback; old create remained charged")
}

// This wrapper models a successful, complete list for only the configured
// project. It intentionally cannot see a known allocation in a foreign project;
// no transport failure or malformed response is converted into an empty list.
type projectOnlyExternalBackend struct{ *backendFixture }

func (b *projectOnlyExternalBackend) Instances(ctx context.Context) ([]incusapi.Instance, error) {
	rows, err := b.backendFixture.Instances(ctx)
	if err != nil {
		return nil, err
	}
	visible := []incusapi.Instance{}
	for _, row := range rows {
		if row.ControllerID == "controller-a" {
			visible = append(visible, row)
		}
	}
	return visible, nil
}
func TestCounterexampleProjectCompleteInventoryDoesNotBoundWholePhysicalAsset(t *testing.T) {
	identity := testsupport.Identity(t, "project-only-daemon")
	physical := &backendFixture{instances: map[string]incusapi.Instance{"foreign-guest": {Name: "foreign-guest", Status: 103, ControllerID: "foreign-controller", PoolID: "foreign-pool", CPUMillis: 1000, RAMMiB: 1024}}, identity: identity.Client, identityConfiguration: identity.Configuration}
	backend := &projectOnlyExternalBackend{backendFixture: physical}
	f, store := externalWriterFixture(t, backend, "controller-a")
	preflight(t, f, "managed-guest", "pool-a")
	if _, err := f.Create(context.Background(), bootstrap("managed-guest", "pool-a")); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot(context.Background())
	if err != nil || state.Allocated.Global.Runners != 1 {
		t.Fatal("selected project accounting changed", err)
	}
	all, err := physical.Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || physical.calls.Load() != 1 {
		t.Fatal("counterexample changed: replace with strict whole-asset qualification", len(all), physical.calls.Load())
	}
	t.Log("CURRENT GAP: configured project was complete and empty, while a known hidden allocation already consumed the physical allowance")
}
