package managedgarm

import (
	"context"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"runtime"
	"time"
)

// Only the retained pilot holder can supply the namespace. No address, FD,
// callback, or transport is accepted. Caller must bracket use with observation.
func (b *retainedStockRead) dialStockAPI(ctx context.Context) (net.Conn, error) {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrBoundary
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !stockReadNamespaceMatches(b.namespace, b.observed.samples[0]) {
		return nil, ErrBoundary
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		out := result{err: ErrBoundary}
		// Publish only after the later original.Close defer has completed.
		defer func() { done <- out }()
		runtime.LockOSThread()
		original, e := os.Open("/proc/thread-self/ns/net")
		if e != nil {
			runtime.UnlockOSThread()
			return
		}
		defer original.Close()
		if ctx.Err() != nil || unix.Setns(int(b.namespace.Fd()), unix.CLONE_NEWNET) != nil {
			runtime.UnlockOSThread()
			return
		}
		current, ce := os.Stat("/proc/thread-self/ns/net")
		wanted, we := b.namespace.Stat()
		var conn net.Conn
		if ce == nil && we == nil && os.SameFile(current, wanted) && ctx.Err() == nil {
			conn, e = (&net.Dialer{}).DialContext(ctx, "tcp4", "127.0.0.1:9998")
		} else {
			e = ErrBoundary
		}
		restored := unix.Setns(int(original.Fd()), unix.CLONE_NEWNET) == nil
		current, ce = os.Stat("/proc/thread-self/ns/net")
		wanted, we = original.Stat()
		if !restored || ce != nil || we != nil || !os.SameFile(current, wanted) {
			if conn != nil {
				conn.Close()
			}
			return // terminate locked OS thread
		}
		runtime.UnlockOSThread()
		if e != nil || ctx.Err() != nil {
			if conn != nil {
				conn.Close()
			}
			return
		}
		out = result{conn: conn}
	}()
	got := <-done // Never abandon namespace worker or release its held FD early.
	return got.conn, got.err
}
