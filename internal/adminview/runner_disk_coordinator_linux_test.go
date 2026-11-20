package adminview

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

func TestRunnerDiskCoordinatorClaimsExactActiveStartupCollectorOnlyOnce(t *testing.T) {
	b, _ := runnerDiskAcceptanceFixture(t)
	b.ReviewExpiresAt = time.Now().Add(time.Minute)
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	scope := b.Scope
	source, err := hostinventory.NewDirFilesystemCollector(t.Context(), hostinventory.DirFilesystemCollectorInput{
		Enabled: true, Directory: dir, Scope: scope, Binding: hostinventory.DirFilesystemFDBinding{
			HostFingerprint: scope.HostFingerprint, StoragePool: scope.StoragePool, BootID: scope.BootID, MountNamespace: scope.MountNamespace, FilesystemType: "ext4",
			DirectoryInode: b.DirectoryInode, ReviewExpiresAt: b.ReviewExpiresAt,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	in := RunnerDiskHealthCoordinatorInput{Collector: source, Binding: b}
	if disabled, err := NewRunnerDiskHealthCoordinator(in); err != nil || disabled != nil {
		t.Fatal("not opt-in", err)
	}
	in.Enabled = true
	for _, mutate := range []func(*RunnerDiskHealthBinding){
		func(x *RunnerDiskHealthBinding) { x.DirectoryInode++ },
		func(x *RunnerDiskHealthBinding) { x.Scope.MountID++ },
		func(x *RunnerDiskHealthBinding) { x.ReviewExpiresAt = x.ReviewExpiresAt.Add(time.Nanosecond) },
	} {
		bad := in
		mutate(&bad.Binding)
		if wrong, err := NewRunnerDiskHealthCoordinator(bad); err == nil || wrong != nil {
			t.Fatal("unmatched startup binding accepted")
		}
	}
	if _, err := dir.Stat(); err != nil {
		t.Fatal("refused startup closed caller-owned producer", err)
	}
	attached, err := NewRunnerDiskHealthCoordinator(in)
	if err != nil || attached == nil {
		t.Fatal(err)
	}
	if duplicate, err := NewRunnerDiskHealthCoordinator(in); err == nil || duplicate != nil {
		t.Fatal("collector was claimed twice")
	}
	if len(attached.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("startup promoted route to measurement")
	}
	if err := attached.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("accepted collector was not closed", err)
	}
}

func testRunnerDiskCoordinator(t *testing.T, lifetime context.Context,
	collect func(context.Context) (hostinventory.BoundDirFilesystemSample, bool), release func() error) *RunnerDiskHealthCoordinator {
	t.Helper()
	b, sample := runnerDiskAcceptanceFixture(t)
	b.ReviewExpiresAt = time.Now().Add(time.Minute)
	cache, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := cache.Begin(b)
	if !ok {
		t.Fatal("fixture could not start")
	}
	sample.ObservedAt = time.Now()
	if !completeRunnerDiskFixture(cache, a, sample, b.DirectoryInode, b.ReviewExpiresAt) {
		t.Fatal("fixture could not seed prior good reading")
	}
	return &RunnerDiskHealthCoordinator{cell: &runnerDiskCoordinatorCell{
		lifetime: lifetime, binding: b, cache: cache, collect: collect, release: release, stopped: make(chan struct{}), done: make(chan struct{}),
	}}
}

func TestRunnerDiskCoordinatorWithdrawsBeforeBlockingCloseAndFencesRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, finishRead := make(chan struct{}), make(chan struct{})
	closing, finishClose := make(chan struct{}), make(chan struct{})
	defer func() { close(finishRead); close(finishClose) }()
	r := testRunnerDiskCoordinator(t, ctx, func(ctx context.Context) (hostinventory.BoundDirFilesystemSample, bool) {
		close(entered)
		<-finishRead
		return hostinventory.BoundDirFilesystemSample{}, false
	}, func() error {
		close(closing)
		<-finishClose
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- r.Run() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("did not reach sample")
	}
	if len(r.SnapshotRunnerDiskHealth()) != 1 {
		t.Fatal("fresh fixture reading was unexpectedly missing")
	}
	if !errors.Is(r.Run(), errRunnerDiskCoordinator) {
		t.Fatal("second Run started")
	}
	cancel()
	// The cancellation hook withdraws independently of the blocked sample
	// and before entering a potentially blocking producer Close.
	select {
	case <-closing:
	case <-time.After(time.Second):
		t.Fatal("blocked sample prevented shutdown")
	}
	if len(r.cell.cache.SnapshotRunnerDiskHealth()) != 0 || len(r.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("old display remained through blocked Close")
	}
	if _, ok := r.cell.cache.Begin(r.cell.binding); ok {
		t.Fatal("shutdown did not revoke the route")
	}
	select {
	case <-done:
		t.Fatal("Run completed while sample was blocked")
	default:
	}
}

func TestRunnerDiskCoordinatorTransientFailureWithdrawsExistingReading(t *testing.T) {
	r := testRunnerDiskCoordinator(t, t.Context(), func(context.Context) (hostinventory.BoundDirFilesystemSample, bool) {
		return hostinventory.BoundDirFilesystemSample{}, false
	}, func() error { return nil })
	defer r.Close()
	if !r.cell.collectOnce() || len(r.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("failed attempt retained old statistics")
	}
	if _, ok := r.cell.cache.Begin(r.cell.binding); !ok {
		t.Fatal("transient failure unexpectedly revoked route")
	}
}
