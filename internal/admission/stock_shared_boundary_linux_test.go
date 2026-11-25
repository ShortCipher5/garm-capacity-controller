package admission

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// The already consumed stock grant and both competing entry points share one
// database. No callback, provider, allocator or settings writer is involved.
func TestStockGrantAndPlacementCompeteForLastHostSlot(t *testing.T) {
	ctx := t.Context()
	store := placementFixture(t)
	held, _ := stockPlan(t, store, "held")
	state, err := store.ReadBrokerGrantState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if held.Request.Host != "a" || state.Allocated.Global != held.Request.Resources {
		t.Fatal("unexpected initial accounting", state.Allocated)
	}

	other, err := OpenSQLite(store.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })

	authority := stockAuthority()
	authority.PoolID = "api"
	authority.ProviderName = "second-provider"
	execution := held.Execution
	execution.Name = ""
	batch := BrokerGrantBatch{
		ID: strings.Repeat("e", 64), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision,
		Grants: []BrokerGrantSpec{{Authority: authority, Host: "b", HostBindingSequence: state.HostBindings["b"].Sequence,
			Execution: execution, Resources: held.Request.Resources, ExpiresAt: state.Grants[0].Spec.ExpiresAt}},
	}
	placement := placementRequest("competing-placement")
	placement.Hosts = []string{"b"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	var grantErr, placementErr error
	wg.Go(func() { <-start; _, grantErr = store.IssueBrokerGrants(ctx, batch) })
	wg.Go(func() { <-start; _, placementErr = other.AdmitPlacement(ctx, placement) })
	close(start)
	wg.Wait()
	switch {
	case grantErr == nil && errors.Is(placementErr, ErrDenied):
	case placementErr == nil && errors.Is(grantErr, ErrBrokerConflict):
	default:
		t.Fatalf("expected one commitment, got grant=%v placement=%v", grantErr, placementErr)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(store.path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err = reopened.ReadBrokerGrantState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Allocated.Global != state.Config.Global || state.Allocated.Hosts["a"] != held.Request.Resources || state.Allocated.Hosts["b"] != held.Request.Resources {
		t.Fatalf("restart lost or duplicated a charge: %+v", state.Allocated)
	}
	if grantErr == nil {
		if len(state.Grants) != 2 || len(state.Reservations) != 1 || state.Allocated.Pools["api"] != held.Request.Resources {
			t.Fatalf("unused competing grant lost: %+v", state.Allocated)
		}
	} else if len(state.Grants) != 1 || len(state.Reservations) != 2 || state.Allocated.Pools["app"] != state.Config.Global {
		t.Fatalf("competing placement lost: %+v", state.Allocated)
	}
	replay, err := reopened.AdmitStockProviderPlacement(ctx, placementRequest("held"), stockAuthority(), stockDigest())
	if err != nil || !replay.Replay || replay.Request != held.Request {
		t.Fatalf("stock replay changed original placement: %+v %v", replay, err)
	}
	placement = placementRequest("extra")
	if _, err := reopened.AdmitPlacement(ctx, placement); !errors.Is(err, ErrDenied) {
		t.Fatalf("restart created headroom: %v", err)
	}
}
