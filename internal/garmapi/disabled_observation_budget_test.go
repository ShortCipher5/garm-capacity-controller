package garmapi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDisabledPoolReadsCancelBeforeLockAcquisition(t *testing.T) {
	for _, kind := range []string{"observe", "verify"} {
		t.Run(kind, func(t *testing.T) {
			m, f := managementServer(t, false)
			defer m.Close()
			m.mu.Lock()
			locked := true
			defer func() {
				if locked {
					m.mu.Unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if kind == "observe" {
					_, e := m.ObserveDisabledRepoPool(ctx, managedRepoID, managedPoolID, DisabledPoolSpec{})
					done <- e
					return
				}
				_, e := m.VerifyDisabledRepoPool(ctx, DisabledPoolReceipt{}, DisabledPoolSpec{})
				done <- e
			}()
			select {
			case err := <-done:
				want := ErrManagement
				if kind == "verify" {
					want = ErrManagementOutcomeUnknown
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v", err)
				}
			case <-time.After(time.Second):
				m.mu.Unlock()
				locked = false
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				t.Fatal("read ignored deadline while waiting for lock")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.gets != 0 || f.writes != 0 {
				t.Fatalf("unexpected upstream calls: %d/%d", f.gets, f.writes)
			}
			m.mu.Unlock()
			locked = false
			live, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if !m.mu.lockContext(live) {
				t.Fatal("cancelled reader leaked gate")
			}
			m.mu.Unlock()
		})
	}
}
