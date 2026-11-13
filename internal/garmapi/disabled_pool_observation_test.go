package garmapi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDisabledPoolFreshObservationNeedsScopeNotPOSTReceiptAndNeverWrites(t *testing.T) {
	client, fixture := disabledPoolServer(t, false)
	client.writes = false // Same immutable mode selected by AllowWrites:false.
	fixture.pool["endpoint"] = map[string]any{"name": "disposable-forge", "api_base_url": "https://not-read.invalid/private"}
	start := time.Now()
	result, err := client.ObserveDisabledRepoPool(context.Background(), managedRepoID, otherID, disabledSpec())
	if err != nil || result.Pool.ID != otherID || result.Pool.Enabled || result.Pool.MinIdleRunners != 0 || result.ForgeEndpointName != "disposable-forge" || result.ObservedAt.Before(start) || result.ObservedAt.After(time.Now()) {
		t.Fatal("restricted fresh observation unavailable", err)
	}
	if fixture.posts != 0 || fixture.gets != 1 || client.pools[otherID] {
		t.Fatal("read became creation or scope expansion")
	}
	for _, scope := range []struct{ repo, pool string }{{otherID, otherID}, {managedRepoID, "https://not-approved.invalid"}} {
		if _, err := client.ObserveDisabledRepoPool(context.Background(), scope.repo, scope.pool, disabledSpec()); !errors.Is(err, ErrManagementUnsafe) {
			t.Fatal("unscoped read accepted", err)
		}
	}
	if fixture.posts != 0 || fixture.gets != 1 {
		t.Fatal("rejected read caused IO")
	}
}
func TestDisabledPoolFreshObservationRejectsDriftAndAmbiguousForgeAlias(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"missing-endpoint":      func(pool map[string]any) { delete(pool, "endpoint") },
		"endpoint-case":         func(pool map[string]any) { pool["Endpoint"] = pool["endpoint"]; delete(pool, "endpoint") },
		"alias-case":            func(pool map[string]any) { pool["endpoint"] = map[string]any{"Name": "forge"} },
		"alias-duplicate-case":  func(pool map[string]any) { pool["endpoint"] = map[string]any{"name": "forge", "Name": "other"} },
		"alias-private-control": func(pool map[string]any) { pool["endpoint"] = map[string]any{"name": "private\nsecret"} },
		"enabled":               func(pool map[string]any) { pool["enabled"] = true },
		"legacy-template":       func(pool map[string]any) { pool["template_id"] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			client, fixture := disabledPoolServer(t, false)
			fixture.pool["endpoint"] = map[string]any{"name": "forge"}
			edit(fixture.pool)
			if _, err := client.ObserveDisabledRepoPool(context.Background(), managedRepoID, otherID, disabledSpec()); !errors.Is(err, ErrManagement) {
				t.Fatal("unsafe source read accepted", err)
			}
			if fixture.posts != 0 {
				t.Fatal("read wrote upstream")
			}
		})
	}
}

func TestDisabledPoolObservationUsesReadStartNotDelayedResponseCompletion(t *testing.T) {
	client, fixture := disabledPoolServer(t, false)
	fixture.pool["endpoint"] = map[string]any{"name": "forge"}
	snapshot := make(chan time.Time, 1)
	release := make(chan struct{})
	fixture.beforeGet = func() { snapshot <- time.Now().UTC(); <-release }
	type outcome struct {
		value DisabledPoolObservation
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := client.ObserveDisabledRepoPool(context.Background(), managedRepoID, otherID, disabledSpec())
		done <- outcome{value, err}
	}()
	var snapshotAt time.Time
	select {
	case snapshotAt = <-snapshot:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("source read did not enter")
	}
	time.Sleep(30 * time.Millisecond)
	close(release)
	select {
	case result := <-done:
		if result.err != nil || result.value.ObservedAt.After(snapshotAt) {
			t.Fatalf("delayed response became fresh: read=%v source=%v err=%v", result.value.ObservedAt, snapshotAt, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("source observation stuck")
	}
}
