package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

func pendingPreparation(name string) GarmInstance {
	return GarmInstance{Name: name, PoolID: "pool-a", State: "pending_create", PreparationPending: true}
}
func TestGarmPreparationDuringJITDoesNotStallOriginalPlannedCreate(t *testing.T) {
	facade, store, backends, _ := multiHostFacade(t)
	ctx := context.Background()
	name := "preparing-jit"
	preflight(t, facade, name, "pool-a")
	plan, err := store.LookupExecutionPlan(ctx, key(facade.controller, "pool-a", name))
	if err != nil {
		t.Fatal(err)
	}
	if err = facade.Reconcile(ctx, garmRows{pendingPreparation(name)}); err != nil {
		t.Fatal(err)
	}
	state, err := store.ReadSnapshot(ctx)
	if err != nil || !state.Ready || len(state.Inventory) != 0 || state.Allocated.Global.Runners != 1 || state.Allocated.Global.CPU != 1000 {
		t.Fatal("Garm database preparation lost/doubled charge or became physical inventory")
	}
	if _, err = facade.Create(ctx, bootstrap(name, "pool-a")); err != nil {
		t.Fatalf("never-dispatched creation stalled %v", err)
	}
	if _, err = facade.Create(ctx, bootstrap(name, "pool-a")); err != nil {
		t.Fatalf("confirmed read replay %v", err)
	}
	if backends[plan.Request.Host].calls.Load() != 1 {
		t.Fatal("preparation proof allowed more than one create")
	}
	if err = facade.Reconcile(ctx, garmRows{pendingPreparation(name)}); err != nil {
		t.Fatal(err)
	}
	state, err = store.ReadSnapshot(ctx)
	if err != nil || len(state.Inventory) != 1 || state.Allocated.Global.Runners != 1 {
		t.Fatal("physical transition lost inventory or duplicated charge")
	}
}
func TestGarmCreatingStateAloneAndPriorInventoryCannotQualify(t *testing.T) {
	for _, mode := range []string{"creating-only", "flag-with-wrong-state", "linked-provider", "busy", "prior-inventory"} {
		t.Run(mode, func(t *testing.T) {
			facade, store, backends, _ := multiHostFacade(t)
			ctx := context.Background()
			name := "unqualified-prep"
			preflight(t, facade, name, "pool-a")
			row := pendingPreparation(name)
			switch mode {
			case "creating-only":
				row.State = "creating"
				row.PreparationPending = false
			case "flag-with-wrong-state":
				row.State = "creating"
			case "linked-provider":
				row.ProviderID = name
			case "busy":
				row.Busy = true
			case "prior-inventory":
				if err := facade.Reconcile(ctx, garmRows{{Name: name, PoolID: "pool-a", State: "creating"}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := facade.Reconcile(ctx, garmRows{row}); err != nil {
				t.Fatal(err)
			}
			state, err := store.ReadSnapshot(ctx)
			if err != nil || len(state.Inventory) != 1 || state.Allocated.Global.Runners != 1 {
				t.Fatal("unqualified history was dropped")
			}
			if _, err = facade.Create(ctx, bootstrap(name, "pool-a")); err == nil {
				t.Fatal("state-only placeholder exception dispatched")
			}
			for _, backend := range backends {
				if backend.calls.Load() != 0 {
					t.Fatal("unqualified row reached backend")
				}
			}
		})
	}
}
func TestGarmPreparationPhysicalResourceAndUnknownIntentRemainBlocking(t *testing.T) {
	for _, mode := range []string{"physical", "unknown-intent"} {
		t.Run(mode, func(t *testing.T) {
			facade, store, backends, _ := multiHostFacade(t)
			ctx := context.Background()
			name := "existing-preparation"
			preflight(t, facade, name, "pool-a")
			plan, err := store.LookupExecutionPlan(ctx, key(facade.controller, "pool-a", name))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "physical" {
				backend := backends[plan.Request.Host]
				backend.mu.Lock()
				backend.instances[name] = incusapi.Instance{Name: name, Status: 103, CPUMillis: plan.Request.Resources.CPU, RAMMiB: plan.Request.Resources.RAM, ControllerID: facade.controller, PoolID: "pool-a", ReservationKey: plan.Request.Key}
				backend.mu.Unlock()
			} else {
				binding, err := store.LookupHostBinding(ctx, plan.Request.Host)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.ExecutePlannedDispatch(ctx, plan, binding, func(context.Context, admission.DispatchPermit) (admission.DispatchReceipt, error) {
					return admission.DispatchReceipt{State: "unknown"}, errors.New("lost original fixture")
				}); !errors.Is(err, admission.ErrDispatchUnknown) {
					t.Fatal(err)
				}
			}
			if err = facade.Reconcile(ctx, garmRows{pendingPreparation(name)}); err != nil {
				t.Fatal(err)
			}
			state, err := store.ReadSnapshot(ctx)
			if err != nil || len(state.Inventory) != 1 || state.Allocated.Global.Runners != 1 {
				t.Fatal("physical/uncertain resource was reclassified as preparation")
			}
			if _, err = facade.Create(ctx, bootstrap(name, "pool-a")); err == nil {
				t.Fatal("original unknown/physical create retried")
			}
			for _, backend := range backends {
				if backend.calls.Load() != 0 {
					t.Fatal("second backend create")
				}
			}
		})
	}
}
func TestGarmPreparationNeedsCompletePhysicalAndOperationsInventory(t *testing.T) {
	for _, mode := range []string{"active-operation", "unavailable-host"} {
		t.Run(mode, func(t *testing.T) {
			facade, store, backends, _ := multiHostFacade(t)
			ctx := context.Background()
			name := "incomplete-preparation"
			preflight(t, facade, name, "pool-a")
			if mode == "active-operation" {
				backends["host-b"].operations = []incusapi.Operation{{ID: operation, Status: 103}}
			} else {
				backends["host-b"].readFail = true
			}
			if err := facade.Reconcile(ctx, garmRows{pendingPreparation(name)}); err == nil {
				t.Fatal("incomplete physical proof reopened readiness")
			}
			state, err := store.ReadSnapshot(ctx)
			if err != nil || state.Ready || state.Allocated.Global.Runners != 1 {
				t.Fatal("incomplete collection freed charge")
			}
			if _, err = facade.Create(ctx, bootstrap(name, "pool-a")); err == nil {
				t.Fatal("incomplete collection dispatched")
			}
			for _, backend := range backends {
				if backend.calls.Load() != 0 {
					t.Fatal("incomplete collection reached backend")
				}
			}
		})
	}
}
func TestGarmPreparationCancellationRemainsAnAtomicNoCreateTombstone(t *testing.T) {
	facade, store, backends, _ := multiHostFacade(t)
	ctx := context.Background()
	name := "cancelled-preparing"
	preflight(t, facade, name, "pool-a")
	if err := facade.Reconcile(ctx, garmRows{pendingPreparation(name)}); err != nil {
		t.Fatal(err)
	}
	if _, err := facade.CancelAdmission(ctx, garmprotocol.Request{Protocol: garmprotocol.Version, Name: name, PoolID: "pool-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := facade.Create(ctx, bootstrap(name, "pool-a")); err == nil {
		t.Fatal("cancelled preparation recreated")
	}
	state, err := store.ReadSnapshot(ctx)
	if err != nil || state.Allocated.Global.Runners != 0 || state.CancellationCount != 1 {
		t.Fatal("tombstone/charge contract changed")
	}
	for _, backend := range backends {
		if backend.calls.Load() != 0 {
			t.Fatal("cancelled preparation reached backend")
		}
	}
}
