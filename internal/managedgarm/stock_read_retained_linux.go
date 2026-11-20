package managedgarm

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// A retained preboot namespace identity, never a connection or admission permit.
// No FD accessor, namespace operation, listener, credentials or public API exists.
type retainedStockRead struct {
	mu        sync.Mutex
	namespace *os.File
	observed  stockReadObservation
}

func stockReadSameLifetime(a, b stockReadObservation) bool {
	if a.pids[0] <= 1 || a.samples[0].start == 0 || !validStockReadProfile(a.profile) {
		return false
	}
	return a.profile == b.profile && a.pids == b.pids && a.samples == b.samples
}

func stockReadNamespaceMatches(f *os.File, sample stockReadSample) bool {
	if f == nil || sample.start == 0 {
		return false
	}
	var fs syscall.Statfs_t
	var st syscall.Stat_t
	// nsfs magic. The exact observed net:[inode] associates this nsfs inode
	// with the sampled network namespace, rather than another namespace kind.
	if syscall.Fstatfs(int(f.Fd()), &fs) != nil || fs.Type != 0x6e736673 || syscall.Fstat(int(f.Fd()), &st) != nil {
		return false
	}
	return st.Ino != 0 && sample.net == fmt.Sprintf("net:[%d]", st.Ino)
}

func retainStockReadOnly(ctx context.Context) (*retainedStockRead, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrBoundary
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	before, err := observeStockReadOnly(ctx)
	if err != nil {
		return nil, ErrBoundary
	}
	// Only the PID minted by the actual protected observer supplies this path.
	f, err := os.OpenFile(fmt.Sprintf("/proc/%d/ns/net", before.pids[0]), os.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrBoundary
	}
	keep := false
	defer func() {
		if !keep {
			f.Close()
		}
	}()
	if !stockReadNamespaceMatches(f, before.samples[0]) {
		return nil, ErrBoundary
	}
	after, err := observeStockReadOnly(ctx)
	if err != nil || !stockReadSameLifetime(before, after) || !stockReadNamespaceMatches(f, after.samples[0]) {
		return nil, ErrBoundary
	}
	keep = true
	return &retainedStockRead{namespace: f, observed: after}, nil
}

func (r *retainedStockRead) closeLocked() {
	if r.namespace != nil {
		r.namespace.Close()
		r.namespace = nil
	}
	r.observed = stockReadObservation{}
}
func (r *retainedStockRead) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked()
}
func (r *retainedStockRead) recheck(ctx context.Context) error {
	if r == nil {
		return ErrBoundary
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || !stockReadNamespaceMatches(r.namespace, r.observed.samples[0]) {
		r.closeLocked()
		return ErrBoundary
	}
	now, err := observeStockReadOnly(ctx)
	if err != nil || !stockReadSameLifetime(r.observed, now) || !stockReadNamespaceMatches(r.namespace, now.samples[0]) {
		r.closeLocked()
		return ErrBoundary
	}
	r.observed = now
	return nil
}
