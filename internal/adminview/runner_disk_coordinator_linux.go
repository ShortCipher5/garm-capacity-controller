package adminview

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

const runnerDiskCollectionInterval = 30 * time.Second

var errRunnerDiskCoordinator = errors.New("reviewed runner disk coordinator unavailable")

// RunnerDiskHealthCoordinatorInput is an in-process startup handoff. Supplying
// values does not prove any route, grant access or qualify physical capacity.
// Only an enabled construction that succeeds takes ownership of Collector.
type RunnerDiskHealthCoordinatorInput struct {
	Enabled   bool
	Collector *hostinventory.DirFilesystemCollector
	Binding   RunnerDiskHealthBinding
}

// RunnerDiskHealthCoordinator optionally publishes collected readings to the
// existing Servers display. It cannot reach remote sources, change paths or
// runner settings, provide scaling inputs, or configure/attach itself.
type RunnerDiskHealthCoordinator struct {
	cell *runnerDiskCoordinatorCell
}

type runnerDiskCoordinatorCell struct {
	lifetime context.Context
	binding  RunnerDiskHealthBinding
	cache    *RunnerDiskHealthCache
	collect  func(context.Context) (hostinventory.BoundDirFilesystemSample, bool)
	release  func() error
	mu       sync.Mutex
	started  bool
	stopped  chan struct{}
	done     chan struct{}
	closeErr error
}

func NewRunnerDiskHealthCoordinator(in RunnerDiskHealthCoordinatorInput) (*RunnerDiskHealthCoordinator, error) {
	if !in.Enabled {
		return nil, nil
	}
	b := in.Binding
	cache, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	if err != nil {
		return nil, errRunnerDiskCoordinator
	}
	lifetime, ok := in.Collector.ClaimReviewedLifetime(b.Scope, b.DirectoryInode, b.ReviewExpiresAt)
	if !ok {
		return nil, errRunnerDiskCoordinator
	}
	return &RunnerDiskHealthCoordinator{cell: &runnerDiskCoordinatorCell{
		lifetime: lifetime, binding: b, cache: cache, collect: in.Collector.Collect, release: in.Collector.Close,
		stopped: make(chan struct{}), done: make(chan struct{}),
	}}, nil
}

func (c *runnerDiskCoordinatorCell) isStopped() bool {
	select {
	case <-c.stopped:
		return true
	default:
		return false
	}
}

func (c *runnerDiskCoordinatorCell) current() bool {
	if c.isStopped() || c.lifetime.Err() != nil {
		return false
	}
	deadline, ok := c.lifetime.Deadline()
	return !ok || time.Now().UTC().Before(deadline.UTC())
}

func (c *runnerDiskCoordinatorCell) collectOnce() bool {
	if !c.current() {
		return false
	}
	attempt, ok := c.cache.Begin(c.binding)
	if !ok {
		return false
	}
	sample, ok := c.collect(c.lifetime)
	if !c.current() {
		return false
	}
	if !ok {
		sample = hostinventory.BoundDirFilesystemSample{}
	}
	// Complete validates the exact opaque observation, drops old numbers on
	// rejection, and cannot resurrect a concurrently withdrawn attempt.
	c.cache.Complete(attempt, sample)
	return true
}

// Run is single-use, starts only when explicitly called, and paces reads from
// each completed attempt (never catches up with a burst). Canceling the
// collector's existing lifetime withdraws the reading even while Run is held
// in an uninterruptible underlying syscall.
func (r *RunnerDiskHealthCoordinator) Run() (err error) {
	if r == nil || r.cell == nil {
		return nil
	}
	c := r.cell
	c.mu.Lock()
	if c.started || c.isStopped() {
		c.mu.Unlock()
		return errRunnerDiskCoordinator
	}
	c.started = true
	c.mu.Unlock()

	stopWatch := context.AfterFunc(c.lifetime, func() { _ = r.Close() })
	defer stopWatch()
	defer func() { err = r.Close() }()
	for c.collectOnce() {
		timer := time.NewTimer(runnerDiskCollectionInterval)
		select {
		case <-timer.C:
		case <-c.lifetime.Done():
			timer.Stop()
			return nil
		case <-c.stopped:
			timer.Stop()
			return nil
		}
	}
	return nil
}

// Close withdraws and fences the reading before potentially blocking on the
// already-owned collector. Concurrent Close callers see the same result.
func (r *RunnerDiskHealthCoordinator) Close() error {
	if r == nil || r.cell == nil {
		return nil
	}
	c := r.cell
	c.mu.Lock()
	if c.isStopped() {
		c.mu.Unlock()
		<-c.done
		return c.closeErr
	}
	c.cache.Withdraw(c.binding)
	close(c.stopped)
	c.mu.Unlock()
	err := c.release()
	c.mu.Lock()
	c.closeErr = err
	close(c.done)
	c.mu.Unlock()
	return err
}

// SnapshotRunnerDiskHealth can be passed to the existing read-only Setter.
// It only reads the memory cache and never schedules or awaits collection.
func (r *RunnerDiskHealthCoordinator) SnapshotRunnerDiskHealth() map[string][]RunnerDiskHealthReading {
	if r == nil || r.cell == nil || !r.cell.current() {
		return nil
	}
	out := r.cell.cache.SnapshotRunnerDiskHealth()
	if !r.cell.current() {
		return nil
	}
	return out
}
