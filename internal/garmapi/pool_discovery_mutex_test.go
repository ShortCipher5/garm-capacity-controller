package garmapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// An unrelated settings writer owns mu. A canceled read must not wait for it,
// nor dispatch a discovery request after the caller has already gone away.
func TestDiscoveryCancellationDoesNotWaitForWriterMutex(t *testing.T) {
	var calls atomic.Int32
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(`[]`)) }))
	m, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() { _, err := m.DiscoverPools(ctx); result <- err }()
	select {
	case err := <-result:
		m.mu.Unlock()
		if err == nil {
			t.Fatal("canceled discovery succeeded")
		}
	case <-time.After(300 * time.Millisecond):
		m.mu.Unlock()
		<-result
		t.Fatal("discovery waited for unrelated management mutex despite cancellation")
	}
	if calls.Load() != 0 {
		t.Fatal("canceled read reached upstream")
	}
}

// Prove dispatch independently of a short TLS-handshake budget. Cancellation
// starts only after the real server observes the request while m.mu is held.
func TestDiscoveryCancellationAfterDispatchWhileWriterMutexHeld(t *testing.T) {
	started := make(chan struct{}, 1)
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() }))
	m, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.mu.Lock()
	locked := true
	defer func() {
		if locked {
			m.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := m.DiscoverPools(ctx); result <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		m.mu.Unlock()
		locked = false
		<-result
		t.Fatal("discovery did not dispatch while writer mutex was held")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled discovery succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		m.mu.Unlock()
		locked = false
		<-result
		t.Fatal("cancelled discovery waited for writer mutex")
	}
	if len(m.pools) != 1 || !m.pools[managedPoolID] {
		t.Fatal("discovery mutated scope")
	}
}

func TestDiscoveryDeadlineWhileWriterMutexHeld(t *testing.T) {
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	m, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := m.DiscoverPools(ctx); result <- err }()
	select {
	case err := <-result:
		m.mu.Unlock()
		if err == nil {
			t.Fatal("expired discovery succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		m.mu.Unlock()
		<-result
		t.Fatal("read deadline waited for writer mutex")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal("discovery returned before its actual deadline")
	}
	if len(m.pools) != 1 || !m.pools[managedPoolID] {
		t.Fatal("discovery mutated scope")
	}
}

func TestDiscoveryGateKeepsOneRequestAndCancelsQueuedRead(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
			w.Write([]byte(`[]`))
		case <-r.Context().Done():
		}
	}))
	c, err := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first := make(chan error, 1)
	go func() { _, err := c.DiscoverPools(context.Background()); first <- err }()
	<-entered
	secondCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := c.DiscoverPools(secondCtx); second <- err }()
	select {
	case err := <-second:
		if err == nil {
			t.Error("queued expired read succeeded")
		}
	case <-time.After(time.Second):
		t.Error("queued read ignored deadline")
	}
	if calls.Load() != 1 {
		t.Error("parallel collection request dispatched")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if _, err := c.DiscoverPools(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatal("gate not released")
	}
}
