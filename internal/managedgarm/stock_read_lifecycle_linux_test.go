package managedgarm

import (
	"context"
	"os"
	"sync"
	"testing"
)

// These tests never invoke successful observation, setns, bind, or Docker.
func TestStockReadLifecycleInvalidContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if got, err := newStockReadLifecycle(c); err == nil || got != nil {
			t.Fatal("invalid context accepted")
		}
	}
	var missing *stockReadLifecycle
	missing.close()
	if missing.recheck(ctx) == nil {
		t.Fatal("nil owner accepted")
	}
}
func TestStockReadLifecycleRevokesIncompleteOwner(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-namespace")
	if err != nil {
		t.Fatal(err)
	}
	owner := &stockReadLifecycle{binding: &retainedStockRead{namespace: f}, done: make(chan struct{})}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner.close()
			if owner.recheck(context.Background()) == nil {
				t.Error("closed owner accepted")
			}
		}()
	}
	wg.Wait()
	if _, err := f.Stat(); err == nil {
		t.Fatal("binding descriptor leaked")
	}
	select {
	case <-owner.done:
	default:
		t.Fatal("close not signaled")
	}
	if owner.binding != nil || owner.held != nil || !owner.closed {
		t.Fatal("owner not cleared")
	}
}
