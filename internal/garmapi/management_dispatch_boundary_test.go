package garmapi

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueuedSettingsWriteCancellationIsNotDispatched(t *testing.T) {
	for _, repo := range []bool{false, true} {
		t.Run(map[bool]string{false: "pool", true: "repository"}[repo], func(t *testing.T) {
			client, f := managementServer(t, true)
			defer client.Close()
			client.mu.Lock()
			defer client.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if repo {
					_, e := client.UpdateRepositoryBalancer(ctx, managedRepoID, "expected", "pack")
					done <- e
				} else {
					v := uint64(2)
					_, e := client.UpdatePool(ctx, managedPoolID, "expected", PoolUpdate{MaxRunners: &v})
					done <- e
				}
			}()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, ErrManagementNotDispatched) || errors.Is(err, ErrManagementOutcomeUnknown) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled write remained blocked behind mutex")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.gets != 0 || f.writes != 0 {
				t.Fatal("cancelled queued write contacted upstream")
			}
		})
	}
}
func TestSettingsGETFailureProvesNoPUTButPUTFailureRemainsUnknown(t *testing.T) {
	var puts atomic.Int32
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			puts.Add(1)
		}
		w.WriteHeader(503)
	}))
	client, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{managedRepoID}, AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	max := uint64(2)
	if _, err = client.UpdatePool(context.Background(), managedPoolID, "expected", PoolUpdate{MaxRunners: &max}); !errors.Is(err, ErrManagementNotDispatched) {
		t.Fatal(err)
	}
	if _, err = client.UpdateRepositoryBalancer(context.Background(), managedRepoID, "expected", "pack"); !errors.Is(err, ErrManagementNotDispatched) {
		t.Fatal(err)
	}
	if puts.Load() != 0 {
		t.Fatal("failed pre-read dispatched PUT")
	}
	live, f := managementServer(t, true)
	defer live.Close()
	before, err := live.Pool(context.Background(), managedPoolID)
	if err != nil {
		t.Fatal(err)
	}
	f.failWrite = 503
	_, err = live.UpdatePool(context.Background(), managedPoolID, before.Fingerprint, PoolUpdate{MaxRunners: &max})
	if !errors.Is(err, ErrManagementOutcomeUnknown) || errors.Is(err, ErrManagementNotDispatched) {
		t.Fatalf("possibly sent PUT misclassified: %v", err)
	}
	if f.writes != 1 {
		t.Fatal("fixture did not dispatch")
	}
}
