package hostinventory

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirFilesystemCollectorDisabledDoesNotAdoptDescriptor(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	c, err := NewDirFilesystemCollector(nil, DirFilesystemCollectorInput{Directory: dir})
	if c != nil || err != nil || c.Close() != nil {
		t.Fatal("disabled constructor has effects or errors")
	}
	if got, ok := c.Collect(t.Context()); ok || got != (BoundDirFilesystemSample{}) {
		t.Fatal("disabled collector produced evidence")
	}
	startup, cancel := context.WithCancel(t.Context())
	cancel()
	scope, _, _ := dirFilesystemFixture()
	for _, in := range []DirFilesystemCollectorInput{
		{Enabled: true, Directory: dir},
		{Enabled: true, Directory: dir, Scope: scope, Binding: testDirFilesystemBinding(scope, 33, time.Now().Add(time.Minute))},
	} {
		if c, err := NewDirFilesystemCollector(startup, in); c != nil || err == nil {
			t.Fatal("invalid startup unexpectedly adopted descriptor")
		}
	}
	expired := testDirFilesystemBinding(scope, 33, time.Now().Add(-time.Second))
	if c, err := NewDirFilesystemCollector(t.Context(), DirFilesystemCollectorInput{true, dir, scope, expired}); c != nil || err == nil {
		t.Fatal("expired review adopted descriptor")
	}
	if _, err := dir.Stat(); err != nil {
		t.Fatal("refusal closed caller's descriptor")
	}
}

func TestDirFilesystemCollectorKeepsOriginalScopeAndEarliestDeadline(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	scope, counters, _ := dirFilesystemFixture()
	now := time.Now()
	ownerDeadline, callerDeadline := now.Add(1500*time.Millisecond), now.Add(time.Minute)
	origin, cancelOrigin := context.WithDeadline(t.Context(), ownerDeadline)
	defer cancelOrigin()
	caller, cancelCaller := context.WithDeadline(t.Context(), callerDeadline)
	defer cancelCaller()
	binding := testDirFilesystemBinding(scope, 33, now.Add(2*time.Minute))
	c, err := NewDirFilesystemCollector(origin, DirFilesystemCollectorInput{true, dir, scope, binding})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var calls atomic.Int32
	c.cell.sample = func(ctx context.Context, fd *os.File, wanted DirFilesystemScope, reviewed DirFilesystemFDBinding) (BoundDirFilesystemSample, bool) {
		d, ok := ctx.Deadline()
		if !ok || !d.Equal(ownerDeadline) || fd != dir || wanted != scope || reviewed != binding {
			t.Fatal("collector changed descriptor, scope or original bound")
		}
		calls.Add(1)
		facts, ok := NormalizeDirFilesystem(wanted, wanted, "ext4", time.Now(), &counters)
		return BoundDirFilesystemSample{facts, reviewed.DirectoryInode, reviewed.ReviewExpiresAt}, ok
	}
	sample, ok := c.Collect(caller)
	facts, _, _, valid := sample.Evidence()
	if !ok || !valid || facts.Scope != scope || calls.Load() != 1 {
		t.Fatal("exact owned sample unavailable")
	}
	alias := *c
	if err := alias.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("collector did not close transferred descriptor: %v", err)
	}
	if got, ok := c.Collect(caller); ok || got != (BoundDirFilesystemSample{}) || calls.Load() != 1 {
		t.Fatal("closed collector was revived")
	}
}

func TestDirFilesystemCollectorRejectsConcurrentAndLateEvidence(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	scope, counters, _ := dirFilesystemFixture()
	expiry := time.Now().Add(time.Minute)
	startup, cancel := context.WithCancel(t.Context())
	defer cancel()
	binding := testDirFilesystemBinding(scope, 33, expiry)
	c, err := NewDirFilesystemCollector(startup, DirFilesystemCollectorInput{true, dir, scope, binding})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	began := make(chan struct{})
	c.cell.sample = func(ctx context.Context, _ *os.File, _ DirFilesystemScope, _ DirFilesystemFDBinding) (BoundDirFilesystemSample, bool) {
		close(began)
		<-ctx.Done()
		facts, _ := NormalizeDirFilesystem(scope, scope, "ext4", time.Now(), &counters)
		return BoundDirFilesystemSample{facts, binding.DirectoryInode, expiry}, true
	}
	done := make(chan bool, 1)
	go func() {
		s, ok := c.Collect(t.Context())
		done <- !ok && s == (BoundDirFilesystemSample{})
	}()
	<-began
	if got, ok := c.Collect(t.Context()); ok || got != (BoundDirFilesystemSample{}) {
		t.Fatal("concurrent cycle returned evidence")
	}
	cancel()
	select {
	case clean := <-done:
		if !clean {
			t.Fatal("late sample escaped canceled lifetime")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled collector did not interrupt synthetic sampler")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
