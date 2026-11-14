package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

var errServiceShutdown = errors.New("service shutdown was not graceful")

// One private latch covers BOTH listeners, including authentication/body reads.
// It is request-lifetime bookkeeping only, not a remote writer/job-safety fence.
// Add and stop share the mutex, so waiting after stop cannot race a new Add.
type serviceIngress struct {
	mu       sync.Mutex
	stopping bool
	accepted sync.WaitGroup
}

func newServiceIngress() *serviceIngress { return &serviceIngress{} }

func (g *serviceIngress) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		if g.stopping {
			g.mu.Unlock()
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		g.accepted.Add(1)
		g.mu.Unlock()
		defer g.accepted.Done()
		next.ServeHTTP(w, r)
	})
}

func (g *serviceIngress) stop() {
	g.mu.Lock()
	g.stopping = true
	g.mu.Unlock()
}

// Stop BOTH ingress paths before starting concurrent HTTP drains. A timeout
// forces the affected sockets closed, but Close is NOT a handler join. We still
// wait for every admitted handler to return before the caller can close its
// ledger/journal/clients. A handler ignoring cancellation can delay this join;
// no bounded complete drain, physical absence or safe capacity release is claimed.
func stopAndJoinService(g *serviceIngress, timeout time.Duration, servers ...*http.Server) error {
	if g == nil || timeout <= 0 {
		return errServiceShutdown
	}
	g.stop()
	results := make(chan error, len(servers))
	count := 0
	for _, server := range servers {
		if server == nil {
			continue
		}
		count++
		go func(s *http.Server) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			err := s.Shutdown(ctx)
			if err != nil {
				// The original failure remains a failure even if Close succeeds.
				_ = s.Close()
			}
			results <- err
		}(server)
	}
	failed := false
	for i := 0; i < count; i++ {
		if <-results != nil {
			failed = true
		}
	}
	g.accepted.Wait()
	if failed {
		return errServiceShutdown
	}
	return nil
}
