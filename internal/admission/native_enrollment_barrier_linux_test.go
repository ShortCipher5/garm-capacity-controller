package admission

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInitialNativeEnrollmentRefusesInterveningLiveWriter(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	original, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	fence, values := bindingFixture(t, s)
	if e = s.BindHostIdentities(ctx, fence, values); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(original.Config, s.now())}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Admit(ctx, request("concurrent-live")); e != nil {
		t.Fatal(e)
	}
	before, e := s.ReadBrokerGrantState(ctx)
	if e != nil || !before.Ready {
		t.Fatal("live fixture", e)
	}
	for _, seq := range []int64{original.Sequence, before.Sequence} {
		got, e := s.BeginInitialNativeEnrollment(ctx, seq, []string{"a", "b"})
		if e == nil || got != (Fence{}) {
			t.Fatal("live writer was closed/adopted", e)
		}
	}
	after, e := s.ReadBrokerGrantState(ctx)
	if e != nil || !after.Ready || after.Sequence != before.Sequence || after.Epoch != before.Epoch || after.Allocated.Global != before.Allocated.Global {
		t.Fatal("refused initial attempt mutated live ledger", e)
	}
}
func TestInitialNativeEnrollmentConcurrentCASAndRollback(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), false)
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec("CREATE TRIGGER initial_audit_failure BEFORE INSERT ON audit WHEN NEW.kind='reconciliation_started' BEGIN SELECT RAISE(ABORT,'fault');END"); e != nil {
		t.Fatal(e)
	}
	if got, e := s.BeginInitialNativeEnrollment(ctx, state.Sequence, []string{"a", "b"}); e == nil || got != (Fence{}) {
		t.Fatal("failed transaction exposed fence")
	}
	if _, e = s.db.Exec("DROP TRIGGER initial_audit_failure"); e != nil {
		t.Fatal(e)
	}
	after, e := s.ReadBrokerGrantState(ctx)
	if e != nil || after.Epoch != state.Epoch || after.Sequence != state.Sequence {
		t.Fatal("audit failure changed barrier", e)
	}
	var count atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, e := s.BeginInitialNativeEnrollment(ctx, state.Sequence, []string{"a", "b"})
			if e == nil {
				if f.Nonce == "" || f.Epoch <= state.Epoch {
					t.Error("invalid successful fence")
				}
				count.Add(1)
			} else if f != (Fence{}) {
				t.Error("detached failed fence")
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("concurrent initial barriers", count.Load())
	}
}
