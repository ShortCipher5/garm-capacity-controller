package garmapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopedManagementReadsCancelWhileWriterHoldsLock(t *testing.T) {
	for name, read := range map[string]func(*ManagementClient, context.Context) error{
		"pool": func(m *ManagementClient, c context.Context) error { _, err := m.Pool(c, managedPoolID); return err },
		"repository": func(m *ManagementClient, c context.Context) error {
			_, err := m.Repository(c, managedRepoID)
			return err
		},
		"snapshot": func(m *ManagementClient, c context.Context) error { _, err := m.Snapshot(c); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
			m, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{managedRepoID}})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			m.mu.Lock()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- read(m, ctx) }()
			select {
			case err := <-done:
				m.mu.Unlock()
				if err == nil {
					t.Fatal("canceled queued read succeeded")
				}
			case <-time.After(150 * time.Millisecond):
				m.mu.Unlock()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("read did not finish after lock release")
				}
				t.Fatal("queued read ignored deadline")
			}
			if requests.Load() != 0 {
				t.Fatal("canceled queued read reached backend")
			}
		})
	}
}
func TestManagementLockCanceledWaiterCannotReleaseOwner(t *testing.T) {
	var lock managementMutex
	lock.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lock.lockContext(ctx) {
		t.Fatal("canceled waiter acquired")
	}
	another, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if lock.lockContext(another) {
		t.Fatal("waiter released current owner")
	}
	lock.Unlock()
	if lock.lockContext(ctx) {
		t.Fatal("canceled waiter acquired free lock")
	}
	if !lock.lockContext(context.Background()) {
		t.Fatal("canceled waiter leaked token")
	}
	lock.Unlock()
}
func TestManagementLockPreservesSingleSerializationDomain(t *testing.T) {
	var lock managementMutex
	var active, overlap atomic.Int32
	done := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			if i%2 == 0 {
				lock.Lock()
			} else if !lock.lockContext(context.Background()) {
				overlap.Add(1)
				done <- struct{}{}
				return
			}
			if active.Add(1) != 1 {
				overlap.Add(1)
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			lock.Unlock()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if overlap.Load() != 0 {
		t.Fatal("read/write serialization overlap")
	}
}
