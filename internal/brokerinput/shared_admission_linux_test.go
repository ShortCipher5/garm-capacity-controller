package brokerinput

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func sharedGrantBatch(t *testing.T, store *admission.SQLiteStore, id, pool string) admission.BrokerGrantBatch {
	t.Helper()
	state, err := store.ReadBrokerGrantState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(id))
	return admission.BrokerGrantBatch{ID: hex.EncodeToString(sum[:]), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision,
		Grants: []admission.BrokerGrantSpec{{Authority: scAuthority(pool), Host: "host", HostBindingSequence: state.HostBindings["host"].Sequence,
			Resources: scSize, Execution: scExecution(), ExpiresAt: time.Now().UTC().Add(time.Hour)}}}
}

func sharedPlacement(pool, name string) admission.PlacementRequest {
	e := scExecution()
	e.Name = name
	key := sha256.Sum256([]byte(e.Controller + "\x00" + pool + "\x00" + name))
	return admission.PlacementRequest{Pool: pool, Key: hex.EncodeToString(key[:]), Execution: e, Resources: scSize, Hosts: []string{"host"}, Strategy: "first_fit"}
}

func TestSharedAdmissionRepoCapAcrossPoolEntryPointsAndRestart(t *testing.T) {
	ctx := t.Context()
	pools := []scPool{{scPoolA, scRepoA, false}, {scPoolB, scRepoA, false}, {scPoolC, scRepoA, false}, {scPoolD, scRepoB, false}}
	stock, trust, _ := scStock(t, pools, nil, nil)
	store, path := enrolledSharedCapacityStore(t, stock, trust, pools, []string{scPoolB})
	policy, err := store.BrokerPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A's sibling B already has a charge. Removing B from the saved policy
	// leaves that charge against A's repo, alongside any new A or C work.
	policy.Repositories[0].MaxRunners = 2
	policy.Repositories[0].Pools = []admission.BrokerPoolPolicy{{ID: scPoolA, MaxRunners: 2}, {ID: scPoolC, MaxRunners: 2}}
	if err = store.ReplaceBrokerPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	other, err := admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a := sharedGrantBatch(t, store, "competitor", scPoolA)
	c := sharedPlacement(scPoolC, "competing-create")
	var wg sync.WaitGroup
	start := make(chan struct{})
	var grantErr, placementErr error
	wg.Go(func() { <-start; _, grantErr = store.IssueBrokerGrants(ctx, a) })
	wg.Go(func() { <-start; _, placementErr = other.AdmitPlacement(ctx, c) })
	close(start)
	wg.Wait()
	if !(grantErr == nil && errors.Is(placementErr, admission.ErrDenied) || errors.Is(grantErr, admission.ErrBrokerConflict) && placementErr == nil) {
		t.Fatalf("competing grants/placement: %v, %v", grantErr, placementErr)
	}
	// The losing writer refreshes its revision. There are still two free
	// physical slots, so the repository ceiling must independently deny it.
	if _, err = other.IssueBrokerGrants(ctx, sharedGrantBatch(t, other, "fresh", scPoolA)); !errors.Is(err, admission.ErrDenied) {
		t.Fatalf("fresh grant exceeded repository limit: %v", err)
	}
	if _, err = other.Admit(ctx, admission.Request{Key: "ordinary", Pool: scPoolC, Host: "host", Resources: scSize}); !errors.Is(err, admission.ErrDenied) {
		t.Fatalf("direct reservation exceeded repository limit: %v", err)
	}
	if _, err = other.AdmitPlacement(ctx, sharedPlacement(scPoolC, "later")); !errors.Is(err, admission.ErrDenied) {
		t.Fatalf("ordinary placement exceeded repository limit: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	d := sharedGrantBatch(t, reopened, "another-repository", scPoolD)
	if _, err = reopened.IssueBrokerGrants(ctx, d); err != nil {
		t.Fatal("a different repository should fit", err)
	}
	placement := sharedPlacement(scPoolD, "new-runner")
	for i := 0; i < 2; i++ {
		plan, err := reopened.AdmitStockProviderPlacement(ctx, placement, scAuthority(scPoolD), strings.Repeat("e", 64))
		if err != nil || plan.Replay != (i == 1) {
			t.Fatalf("stock redemption/replay %d: %+v %v", i, plan, err)
		}
	}
	state, err := reopened.ReadBrokerGrantState(ctx)
	want := admission.Resources{CPU: 3000, RAM: 3000, Disk: 3000, Runners: 3}
	if err != nil || state.Allocated.Global != want || state.Allocated.Hosts["host"] != want ||
		state.Allocated.Pools[scPoolB] != scSize || state.Allocated.Pools[scPoolD] != scSize ||
		state.Reservations[placement.Key].State != "creating" {
		t.Fatalf("pending or retained charge lost/doubled: %+v %v", state.Allocated, err)
	}
}

func TestSharedAdmissionCurrentScopeDiskAndEpoch(t *testing.T) {
	ctx := t.Context()
	pools := []scPool{{scPoolA, scRepoA, false}}
	stock, trust, _ := scStock(t, pools, nil, nil)
	store, _ := enrolledSharedCapacityStore(t, stock, trust, pools, nil)
	for _, tc := range []struct {
		name   string
		change func(*admission.BrokerGrantSpec)
	}{
		{"provider", func(g *admission.BrokerGrantSpec) { g.Authority.ProviderName = "other" }},
		{"leaf", func(g *admission.BrokerGrantSpec) { g.Authority.ClientFingerprint = strings.Repeat("0", 64) }},
		{"disk", func(g *admission.BrokerGrantSpec) { g.Resources.Disk-- }},
		{"execution", func(g *admission.BrokerGrantSpec) { g.Execution.Flavor = "different" }},
		{"binding", func(g *admission.BrokerGrantSpec) { g.HostBindingSequence++ }},
	} {
		batch := sharedGrantBatch(t, store, tc.name, scPoolA)
		batch.Grants = append(batch.Grants, batch.Grants[0])
		tc.change(&batch.Grants[1])
		if _, err := store.IssueBrokerGrants(ctx, batch); !errors.Is(err, admission.ErrDenied) {
			t.Fatalf("batch with incorrect %s partially accepted: %v", tc.name, err)
		}
	}
	policy, err := store.BrokerPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policy.Repositories[0].Pools[0].MaxRunners = 1
	if err = store.ReplaceBrokerPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	original := sharedGrantBatch(t, store, "good", scPoolA)
	if _, err = store.IssueBrokerGrants(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, err = store.IssueBrokerGrants(ctx, sharedGrantBatch(t, store, "extra", scPoolA)); !errors.Is(err, admission.ErrDenied) {
		t.Fatalf("ignored pool runner ceiling: %v", err)
	}
	// The new bounded snapshot can re-open admission but cannot make an
	// unconsumed grant from the old epoch into current execution authority.
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteReconciliation(ctx, fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host", Healthy: true, Bounded: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, ObservedAt: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	place := sharedPlacement(scPoolA, "old-grant")
	if _, err = store.AdmitStockProviderPlacement(ctx, place, scAuthority(scPoolA), strings.Repeat("a", 64)); !errors.Is(err, admission.ErrBrokerGrant) {
		t.Fatalf("redeemed a grant from the prior epoch: %v", err)
	}
	replay, err := store.IssueBrokerGrants(ctx, original)
	if err != nil || len(replay) != 1 || replay[0].State != "unused" {
		t.Fatalf("historical replay: %+v %v", replay, err)
	}
	state, err := store.ReadBrokerGrantState(ctx)
	if err != nil || len(state.Grants) != 1 || len(state.Reservations) != 0 || !state.Grants[0].Superseded() || state.Allocated.Global != scSize {
		t.Fatalf("stale or partially invalid batch changed charges: %+v %v", state.Allocated, err)
	}
	policy = state.Policy
	policy.Repositories[0].ID = scRepoB
	if err = store.ReplaceBrokerPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if _, err = store.IssueBrokerGrants(ctx, sharedGrantBatch(t, store, "wrong-owner", scPoolA)); !errors.Is(err, admission.ErrDenied) {
		t.Fatalf("accepted wrong pool-to-repository scope: %v", err)
	}
}

func TestSharedAdmissionHostDiskLeavesOtherDimensionsFree(t *testing.T) {
	ctx := t.Context()
	pools := []scPool{{scPoolA, scRepoA, false}}
	stock, trust, _ := scStock(t, pools, nil, nil)
	store, _ := enrolledSharedCapacityStore(t, stock, trust, pools, nil)
	for _, disk := range []int64{0, scSize.Disk - 1, scSize.Disk} {
		state, err := store.ReadBrokerGrantState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		budget := state.Config.Hosts["host"].Budget
		budget.Disk = disk
		err = store.ApplyPolicyChange(ctx, admission.PolicyChange{Action: "host_budget", Target: "host", Budget: &budget, Reason: "fixture disk limit", Revision: admission.PolicyRevision(state.Config)})
		if err != nil {
			t.Fatal(err)
		}
		fence, err := store.BeginReconciliation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.CompleteReconciliation(ctx, fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host", ObservedAt: time.Now().UTC(), Bounded: true, Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true}}}); err != nil {
			t.Fatal(err)
		}
		batch := sharedGrantBatch(t, store, "host-disk", scPoolA)
		_, err = store.IssueBrokerGrants(ctx, batch)
		if disk == scSize.Disk && err != nil || disk < scSize.Disk && !errors.Is(err, admission.ErrDenied) {
			t.Fatalf("host disk %d grant result: %v", disk, err)
		}
	}
	state, err := store.ReadBrokerGrantState(ctx)
	if err != nil || state.Allocated.Hosts["host"] != scSize || state.Allocated.Global != scSize || len(state.Grants) != 1 {
		t.Fatalf("expected precisely one exact-fit commitment: %+v %v", state.Allocated, err)
	}
}
