//go:build linux

package nativecontroller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func TestRetirementServiceRuntimeRefusesBeforeOwnerContact(t *testing.T) {
	f := setup(t, "")
	snap, e := f.s.Snapshot(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	db, id := filepath.Join(dir, "runtime"), filepath.Join(dir, "identity")
	if e = admission.InitializeSQLite(db, id, snap.Config); e != nil {
		t.Fatal(e)
	}
	s, e := admission.OpenRuntimeSQLite(db, id)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	// Even malformed/missing owner credentials must not be evaluated first.
	if _, e = NewRetirementService(t.Context(), RetirementConfiguration{Store: s}); !errors.Is(e, ErrUnqualified) {
		t.Fatal(e)
	}
	if _, e := (&Coordinator{store: s}).ObserveRetirementDiagnostics(t.Context(), "absent"); !errors.Is(e, ErrUnqualified) {
		t.Fatal(e)
	}
	dormant := &RetirementService{store: s}
	if _, e = dormant.Cycle(t.Context()); !errors.Is(e, ErrUnqualified) {
		t.Fatal(e)
	}
	if e = dormant.Run(t.Context()); !errors.Is(e, ErrUnqualified) {
		t.Fatal(e)
	}
	after, e := s.Snapshot(t.Context())
	if e != nil || after.Ready || after.Allocated.Global != (admission.Resources{}) {
		t.Fatal("runtime altered", e)
	}
}
func TestRetirementServiceEmptyScanCancellationAndScopeValidation(t *testing.T) {
	f := setup(t, "")
	cfg := RetirementConfiguration{Store: f.s, Owners: []Configuration{f.config}, Interval: time.Second}
	s, e := NewRetirementService(t.Context(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.Cycle(t.Context())
	if e != nil || result != (RetirementCycle{}) || s.Latest().At.IsZero() {
		t.Fatal(result, e)
	}
	cfg.Owners = append(cfg.Owners, f.config)
	if _, e = NewRetirementService(t.Context(), cfg); e == nil {
		t.Fatal("duplicate owner accepted")
	}
	// A queued cycle must be cancellable instead of blocking behind an active
	// owner operation. Cancellation neither consumes claims nor releases debt.
	s.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := s.Cycle(ctx); done <- e }()
	cancel()
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("cancel ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("cycle wait ignored cancellation")
	}
	<-s.gate
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if e = s.Run(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	snap, e := f.s.Snapshot(t.Context())
	if e != nil || snap.Allocated.Global.Runners != 1 {
		t.Fatal("empty/cancelled scan changed charge", e, snap.Allocated)
	}
}

func TestRetainedDiagnosticsRequireCanonicalDeleteBeforeOwnerReads(t *testing.T) {
	f := setup(t, "")
	c, e := New(t.Context(), f.config)
	if e != nil {
		t.Fatal(e)
	}
	before, e := f.s.Snapshot(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	// Even a live authenticated owner cannot replace the missing canonical receipt.
	f.owner.mu.Lock()
	calls := f.owner.totalCalls
	f.owner.mu.Unlock()
	observation, e := c.ObserveRetirementDiagnostics(t.Context(), "00000000000000000000000000000000")
	if !errors.Is(e, ErrUnknown) || observation.Valid() {
		t.Fatal(e)
	}
	f.owner.mu.Lock()
	afterCalls := f.owner.totalCalls
	f.owner.mu.Unlock()
	if afterCalls != calls {
		t.Fatal("owner contacted without canonical DELETE", calls, afterCalls)
	}
	after, e := f.s.Snapshot(t.Context())
	if e != nil || after.Allocated.Global != before.Allocated.Global || len(after.Audit) != len(before.Audit) {
		t.Fatal("diagnostic changed canonical state", e)
	}
}
