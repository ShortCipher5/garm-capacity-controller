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

func TestRetirementFreshnessIsProcessLocal(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	interval := 30 * time.Second
	window := interval + retirementCycleTimeout
	for _, c := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"no completed cycle after restart", time.Time{}, "no_cycle"},
		{"future timestamp", now.Add(time.Second), "clock_invalid"},
		{"finished just now", now, "within_cycle_window"},
		{"before expiry", now.Add(-window + time.Nanosecond), "within_cycle_window"},
		{"exact deadline", now.Add(-window), "stale"},
		{"old success or failure", now.Add(-time.Hour), "stale"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := retirementFreshness(c.at, now, interval); got != c.want {
				t.Fatalf("got %s; want %s", got, c.want)
			}
		})
	}
	status := RetirementStatus{At: time.Now().UTC().Add(-time.Hour), Incomplete: true, Result: RetirementCycle{Examined: 1, Unresolved: 1}}
	s := &RetirementService{status: status, interval: interval}
	got, freshness := s.LatestWithFreshness()
	if freshness != "stale" || got != status || got.Result.ReleaseAuthorized {
		t.Fatalf("freshness obscured unresolved cycle: %+v %s", got, freshness)
	}
}
