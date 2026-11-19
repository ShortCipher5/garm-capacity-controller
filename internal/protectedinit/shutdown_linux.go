//go:build linux

package protectedinit

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// shutdownGate never forwards signals to the runner. A halt request can only
// finish PID1 after the caller has durably recorded reap+network closure. Early
// halt requests are remembered, not acted on; jobs are never interrupted here.
type shutdownGate struct {
	mu                sync.Mutex
	fenced, requested bool
	done              chan struct{}
	once              sync.Once
}

func newShutdownGate() *shutdownGate { return &shutdownGate{done: make(chan struct{})} }
func (g *shutdownGate) receive(s os.Signal) {
	if s != syscall.SIGPWR {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requested = true
	g.finishLocked()
}
func (g *shutdownGate) fenceCommitted() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fenced = true
	g.finishLocked()
}
func (g *shutdownGate) finishLocked() {
	if g.fenced && g.requested {
		g.once.Do(func() { close(g.done) })
	}
}
func watchShutdown() (*shutdownGate, func()) {
	g := newShutdownGate()
	signals := make(chan os.Signal, 16)
	stop := make(chan struct{})
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGPWR)
	go func() {
		for {
			select {
			case s := <-signals:
				g.receive(s)
			case <-stop:
				return
			}
		}
	}()
	return g, func() { signal.Stop(signals); close(stop) }
}
