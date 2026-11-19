package managedgarm

import (
	"context"
	"net"
	"os"
	"runtime"
	"sync"

	"golang.org/x/sys/unix"
)

// Owns only an unserved fixed socket and retained identity. No socket/FD accessor,
// generic callback, Accept, credential, session or production consumer exists.
type stockReadLifecycle struct {
	mu      sync.Mutex
	binding *retainedStockRead
	held    *stockReadListener
	done    chan struct{}
	closed  bool
}

func newStockReadLifecycle(ctx context.Context) (*stockReadLifecycle, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrBoundary
	}
	binding, err := retainStockReadOnly(ctx)
	if err != nil {
		return nil, ErrBoundary
	}
	owner := &stockReadLifecycle{binding: binding, done: make(chan struct{})}
	if binding.recheck(ctx) != nil {
		owner.close()
		return nil, ErrBoundary
	}
	// The holder cannot be closed while the dedicated thread uses its FD.
	binding.mu.Lock()
	type result struct {
		listener *net.TCPListener
		err      error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		original, e := os.Open("/proc/thread-self/ns/net")
		if e != nil {
			runtime.UnlockOSThread()
			done <- result{err: ErrBoundary}
			return
		}
		defer original.Close()
		if ctx.Err() != nil || !stockReadNamespaceMatches(binding.namespace, binding.observed.samples[0]) {
			runtime.UnlockOSThread()
			done <- result{err: ErrBoundary}
			return
		}
		if unix.Setns(int(binding.namespace.Fd()), unix.CLONE_NEWNET) != nil {
			runtime.UnlockOSThread()
			done <- result{err: ErrBoundary}
			return
		}
		// Fixed numeric IPv4 only; no resolution, caller-selected address or options.
		var listener *net.TCPListener
		entered, enterErr := os.Stat("/proc/thread-self/ns/net")
		expected, expectedErr := binding.namespace.Stat()
		if ctx.Err() == nil && enterErr == nil && expectedErr == nil && os.SameFile(entered, expected) {
			var raw net.Listener
			raw, e = (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:9445")
			if e == nil {
				var ok bool
				listener, ok = raw.(*net.TCPListener)
				if !ok {
					raw.Close()
					e = ErrBoundary
				}
			}
		} else {
			e = ErrBoundary
		}
		restored := unix.Setns(int(original.Fd()), unix.CLONE_NEWNET) == nil
		current, currentErr := os.Stat("/proc/thread-self/ns/net")
		prior, priorErr := original.Stat()
		restored = restored && currentErr == nil && priorErr == nil && os.SameFile(current, prior)
		if !restored {
			if listener != nil {
				listener.Close()
			}
			// Returning while locked terminates this OS thread; never put it back in
			// the Go pool with the wrong namespace, including on restoration failure.
			done <- result{err: ErrBoundary}
			return
		}
		runtime.UnlockOSThread()
		if e != nil || ctx.Err() != nil {
			if listener != nil {
				listener.Close()
			}
			done <- result{err: ErrBoundary}
			return
		}
		done <- result{listener: listener}
	}()
	// Always join before releasing/closing namespace. Cancellation is not a
	// license to abandon a worker that may still hold a socket or changed thread.
	got := <-done
	binding.mu.Unlock()
	if got.err != nil {
		owner.close()
		return nil, ErrBoundary
	}
	owner.held, err = retainStockReadListener(got.listener)
	if err != nil {
		got.listener.Close()
		owner.close()
		return nil, ErrBoundary
	}
	if owner.recheck(ctx) != nil {
		return nil, ErrBoundary
	}
	go func() {
		select {
		case <-ctx.Done():
			owner.close()
		case <-owner.done:
		}
	}()
	if ctx.Err() != nil {
		owner.close()
		return nil, ErrBoundary
	}
	return owner, nil
}

func (o *stockReadLifecycle) closeLocked() {
	if o.closed {
		return
	}
	o.closed = true
	if o.held != nil {
		o.held.listener.Close()
		o.held = nil
	}
	if o.binding != nil {
		o.binding.close()
		o.binding = nil
	}
	if o.done != nil {
		close(o.done)
	}
}
func (o *stockReadLifecycle) close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closeLocked()
}
func (o *stockReadLifecycle) recheck(ctx context.Context) error {
	if o == nil {
		return ErrBoundary
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || ctx == nil || ctx.Err() != nil || o.binding == nil || o.held == nil {
		o.closeLocked()
		return ErrBoundary
	}
	b := o.binding
	b.mu.Lock()
	now, err := observeStockReadPhase(ctx, o.held)
	valid := err == nil && ctx.Err() == nil && stockReadSameLifetime(b.observed, now) && stockReadNamespaceMatches(b.namespace, now.samples[0])
	if valid {
		b.observed = now
	}
	b.mu.Unlock()
	if !valid {
		o.closeLocked()
		return ErrBoundary
	}
	return nil
}
