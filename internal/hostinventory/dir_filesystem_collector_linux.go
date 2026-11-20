package hostinventory

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

const dirFilesystemCollectLimit = 2 * time.Second

var errDirFilesystemCollector = errors.New("reviewed directory collector unavailable")

// DirFilesystemCollectorInput is an in-process startup handoff, not a
// serialized configuration, transport request or source of authority. The
// actual opener and reviewer must be independent. An enabled and successfully
// constructed collector takes ownership of the already-held directory; not
// enabling or refusing does not read, adopt or close it.
type DirFilesystemCollectorInput struct {
	Enabled   bool
	Directory *os.File
	Scope     DirFilesystemScope
	Binding   DirFilesystemFDBinding
}

// DirFilesystemCollector samples one already-reviewed FD. There is no
// worker, polling, cache, remote adapter, path opening or authority here.
// A future existing owned lifecycle may schedule sampling. Statfs counters
// are relative to this descriptor (ext4 project quota may affect them), not
// proof of filesystem-wide headroom or which route uses it. Copies share
// the same closure and in-flight state; a zero value remains disabled.
type DirFilesystemCollector struct {
	cell *dirFilesystemCollectorCell
}

type dirFilesystemCollectorCell struct {
	mu           sync.Mutex
	dir          *os.File
	scope        DirFilesystemScope
	binding      DirFilesystemFDBinding
	lifetime     context.Context
	cancel       context.CancelFunc
	stopLifetime func() bool
	inFlight     bool
	claimed      bool
	closed       bool
	closeDone    chan struct{}
	closeErr     error
	sample       func(context.Context, *os.File, DirFilesystemScope, DirFilesystemFDBinding) (BoundDirFilesystemSample, bool)
}

func NewDirFilesystemCollector(startup context.Context, in DirFilesystemCollectorInput) (*DirFilesystemCollector, error) {
	if !in.Enabled {
		return nil, nil
	}
	if in.Directory == nil || *in.Directory == (os.File{}) || !in.Scope.Valid() || !in.Binding.matches(in.Scope) ||
		!dirFilesystemReadCurrent(startup, in.Binding, time.Now()) {
		return nil, errDirFilesystemCollector
	}
	lifetime, cancel := context.WithDeadline(startup, in.Binding.ReviewExpiresAt)
	cell := &dirFilesystemCollectorCell{
		dir: in.Directory, scope: in.Scope, binding: in.Binding, lifetime: lifetime, cancel: cancel,
		closeDone: make(chan struct{}), sample: SampleDirFilesystemFD,
	}
	c := &DirFilesystemCollector{cell: cell}
	// Ownership transfers here, including for a valid startup context
	// canceled concurrently: it will close the returned collector.
	cell.mu.Lock()
	cell.stopLifetime = context.AfterFunc(lifetime, func() { _ = c.Close() })
	cell.mu.Unlock()
	return c, nil
}

// ClaimReviewedLifetime matches the immutable startup scope, inode and review
// cutoff against an independent consumer review, transferring ownership once. The returned read-only
// lifetime closes on owner cancellation, expiry or Close. This does not
// establish the external route or expose a descriptor.
func (c *DirFilesystemCollector) ClaimReviewedLifetime(scope DirFilesystemScope, inode uint64, expiry time.Time) (context.Context, bool) {
	if c == nil || c.cell == nil {
		return nil, false
	}
	cell := c.cell
	cell.mu.Lock()
	defer cell.mu.Unlock()
	if cell.closed || cell.claimed || cell.inFlight || cell.scope != scope || cell.binding.DirectoryInode != inode ||
		!cell.binding.ReviewExpiresAt.UTC().Equal(expiry.UTC()) ||
		!dirFilesystemReadCurrent(cell.lifetime, cell.binding, time.Now()) {
		return nil, false
	}
	cell.claimed = true
	return cell.lifetime, true
}

// Collect performs one read without opening anything. A concurrent cycle,
// disabled collector, expired review or failed observation is unknown, never
// cached counters. The cutoff bounds acceptance, not the duration of a
// kernel syscall that Linux does not make context-interruptible.
func (c *DirFilesystemCollector) Collect(ctx context.Context) (BoundDirFilesystemSample, bool) {
	var none BoundDirFilesystemSample
	if c == nil || c.cell == nil || ctx == nil {
		return none, false
	}
	cell := c.cell
	cell.mu.Lock()
	start := time.Now()
	if cell.closed || cell.inFlight || !dirFilesystemReadCurrent(cell.lifetime, cell.binding, start) ||
		!dirFilesystemReadCurrent(ctx, cell.binding, start) {
		cell.mu.Unlock()
		return none, false
	}
	cell.inFlight = true
	cell.mu.Unlock()

	deadline := start.Add(dirFilesystemCollectLimit)
	if ownerDeadline, ok := cell.lifetime.Deadline(); ok && ownerDeadline.Before(deadline) {
		deadline = ownerDeadline
	}
	bound, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	stop := context.AfterFunc(cell.lifetime, cancel)
	defer stop()
	var sample BoundDirFilesystemSample
	ok := false
	if dirFilesystemReadCurrent(cell.lifetime, cell.binding, time.Now()) && dirFilesystemReadCurrent(bound, cell.binding, time.Now()) {
		sample, ok = cell.sample(bound, cell.dir, cell.scope, cell.binding)
	}

	cell.mu.Lock()
	defer cell.mu.Unlock()
	cell.inFlight = false
	now := time.Now()
	facts, inode, until, evidence := sample.Evidence()
	if cell.closed || !ok || !evidence || !dirFilesystemReadCurrent(cell.lifetime, cell.binding, now) ||
		!dirFilesystemReadCurrent(bound, cell.binding, now) || facts.Scope != cell.scope || inode != cell.binding.DirectoryInode ||
		!until.Equal(cell.binding.ReviewExpiresAt) || facts.ObservedAt.Before(start) || facts.ObservedAt.After(now) {
		return none, false
	}
	return sample, true
}

// Close prevents new reads immediately and releases the transferred FD.
// Concurrent calls share its outcome. Closing may wait for an already held
// kernel operation to finish; cancellation cannot recall such an operation.
func (c *DirFilesystemCollector) Close() error {
	if c == nil || c.cell == nil {
		return nil
	}
	cell := c.cell
	cell.mu.Lock()
	if cell.closed {
		done := cell.closeDone
		cell.mu.Unlock()
		<-done
		return cell.closeErr
	}
	cell.closed = true
	stop := cell.stopLifetime
	cell.mu.Unlock()
	stop()
	cell.cancel()
	err := cell.dir.Close()
	cell.mu.Lock()
	cell.closeErr = err
	close(cell.closeDone)
	cell.mu.Unlock()
	return err
}
