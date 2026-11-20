package adminview

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"github.com/ShortCipher5/garm-capacity-controller/internal/remotedisktelemetry"
)

const (
	maxRemoteRunnerDiskRoutes = 8
	remoteRunnerDiskPollDelay = 15 * time.Second
	remoteRunnerDiskPollLimit = 10 * time.Second
)

var errRemoteRunnerDiskCoordinator = errors.New("reviewed remote runner disk coordinator unavailable")

// RemoteRunnerDiskHealthRoute carries three independently established inputs.
// Matching them does not prove that the observer is installed on the reviewed
// host; provisioning and route trust remain the startup owner's responsibility.
type RemoteRunnerDiskHealthRoute struct {
	Binding RunnerDiskHealthBinding
	Review  remotedisktelemetry.Review
	Reader  *remotedisktelemetry.HTTPReader
}

type RemoteRunnerDiskHealthInput struct {
	Enabled bool
	Routes  []RemoteRunnerDiskHealthRoute
}

type RemoteRunnerDiskHealthCoordinator struct{ cell *remoteRunnerDiskCell }
type remoteRunnerDiskRead struct {
	binding RunnerDiskHealthBinding
	review  remotedisktelemetry.Review
	read    func(context.Context, remotedisktelemetry.Review) (remotedisktelemetry.DisplayReading, error)
}
type remoteRunnerDiskCell struct {
	cache    *RunnerDiskHealthCache
	routes   []remoteRunnerDiskRead
	mu       sync.Mutex
	lifetime context.Context
	cancel   context.CancelFunc
	stopped  bool
}

// NewRemoteRunnerDiskHealthCoordinator only freezes startup data. It starts no
// goroutine or HTTP and does not load credentials, attach a handler, or add an
// allocation input. One runner-disk source may be installed on the handler.
func NewRemoteRunnerDiskHealthCoordinator(in RemoteRunnerDiskHealthInput) (*RemoteRunnerDiskHealthCoordinator, error) {
	if !in.Enabled {
		return nil, nil
	}
	if len(in.Routes) == 0 || len(in.Routes) > maxRemoteRunnerDiskRoutes {
		return nil, errRemoteRunnerDiskCoordinator
	}
	routes := make([]remoteRunnerDiskRead, 0, len(in.Routes))
	bindings := make([]RunnerDiskHealthBinding, 0, len(in.Routes))
	seenReader := make(map[*remotedisktelemetry.HTTPReader]bool, len(in.Routes))
	seenScope := make(map[hostinventory.DirFilesystemScope]bool, len(in.Routes))
	for _, x := range in.Routes {
		b, r := x.Binding, x.Review
		b.ReviewExpiresAt, r.ExpiresAt = b.ReviewExpiresAt.UTC(), r.ExpiresAt.UTC()
		if b.Scope != r.Scope || b.DirectoryInode != r.DirectoryInode || !b.ReviewExpiresAt.Equal(r.ExpiresAt) ||
			!runnerDiskWireTime(r.ExpiresAt) || !time.Now().UTC().Before(r.ExpiresAt) ||
			!x.Reader.MatchesReview(r) || seenReader[x.Reader] || seenScope[r.Scope] {
			return nil, errRemoteRunnerDiskCoordinator
		}
		seenReader[x.Reader], seenScope[r.Scope] = true, true
		bindings = append(bindings, b)
		routes = append(routes, remoteRunnerDiskRead{b, r, x.Reader.Read})
	}
	cache, err := NewRunnerDiskHealthCache(bindings)
	if err != nil {
		return nil, errRemoteRunnerDiskCoordinator
	}
	return &RemoteRunnerDiskHealthCoordinator{cell: &remoteRunnerDiskCell{cache: cache, routes: routes}}, nil
}

func remoteRunnerDiskCurrent(ctx context.Context, wall time.Time) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	return !ok || wall.Before(deadline.UTC())
}

func (c *remoteRunnerDiskCell) poll(ctx context.Context, route remoteRunnerDiskRead) {
	attempt, ok := c.cache.Begin(route.binding)
	if !ok {
		return
	}
	var result remotedisktelemetry.DisplayReading
	err := errRemoteRunnerDiskCoordinator
	if remoteRunnerDiskCurrent(ctx, c.cache.wallNow()) {
		result, err = route.read(ctx, route.review)
	}
	c.cache.completeLatest(attempt, func(s *runnerDiskSnapshotSlot, now time.Time) (RunnerDiskHealthReading, bool) {
		if err != nil || !remoteRunnerDiskCurrent(ctx, now) || !sameRunnerDiskBinding(route.binding, s.binding) ||
			!now.Before(result.ExpiresAt.UTC()) || result.ExpiresAt.UTC().After(route.review.ExpiresAt) {
			return RunnerDiskHealthReading{}, false
		}
		out := RunnerDiskHealthReading{
			StoragePool: route.binding.Scope.StoragePool, ObservedAt: result.ObservedAt.UTC(), ExpiresAt: result.ExpiresAt.UTC(),
			TotalBytes: result.TotalBytes, AvailableBytes: result.AvailableBytes,
			TotalInodes: result.TotalInodes, AvailableInodes: result.AvailableInodes,
		}
		return out, len(projectRunnerDiskHealth([]RunnerDiskHealthReading{out}, now)) == 1
	})
}

// Run polls only when the owner explicitly calls it. A cycle has one fixed
// deadline and at most one simultaneous read per route; the next cycle waits
// from completion (no overlap, catch-up or retries). Expected route failures
// withdraw just that row; a later successful fresh read may restore it.
func (r *RemoteRunnerDiskHealthCoordinator) Run(ctx context.Context) error {
	if r == nil || r.cell == nil {
		return nil
	}
	if !remoteRunnerDiskCurrent(ctx, time.Now().UTC()) {
		return errRemoteRunnerDiskCoordinator
	}
	c := r.cell
	c.mu.Lock()
	if c.stopped || c.lifetime != nil {
		c.mu.Unlock()
		return errRemoteRunnerDiskCoordinator
	}
	life, cancel := context.WithCancel(ctx)
	c.lifetime, c.cancel = life, cancel
	c.mu.Unlock()
	defer r.Close()

	for remoteRunnerDiskCurrent(life, c.cache.wallNow()) {
		cycle, stop := context.WithTimeout(life, remoteRunnerDiskPollLimit)
		var workers sync.WaitGroup
		for _, route := range c.routes {
			workers.Go(func() { c.poll(cycle, route) })
		}
		workers.Wait()
		stop()
		timer := time.NewTimer(remoteRunnerDiskPollDelay)
		select {
		case <-life.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

// Close fences publication, cancels outstanding requests and withdraws every
// route. Concurrent calls do not close a shared client or reopen a stopped run.
func (r *RemoteRunnerDiskHealthCoordinator) Close() {
	if r == nil || r.cell == nil {
		return
	}
	c := r.cell
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	c.stopped = true
	if c.cancel != nil {
		c.cancel()
	}
	for _, route := range c.routes {
		c.cache.Withdraw(route.binding)
	}
}

func (r *RemoteRunnerDiskHealthCoordinator) SnapshotRunnerDiskHealth() map[string][]RunnerDiskHealthReading {
	if r == nil || r.cell == nil {
		return nil
	}
	c := r.cell
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || !remoteRunnerDiskCurrent(c.lifetime, c.cache.wallNow()) {
		return nil
	}
	out := c.cache.SnapshotRunnerDiskHealth()
	if !remoteRunnerDiskCurrent(c.lifetime, c.cache.wallNow()) {
		return nil
	}
	return out
}
