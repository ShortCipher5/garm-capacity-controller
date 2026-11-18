//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecapsule"
	"time"
)

// GuestIncarnation binds authenticated native identity to retained kernel objects.
// It is process-local, cannot be restored from JSON and grants no release itself.
type GuestIncarnation struct {
	kernel                 nativecapsule.GuestKernel
	client                 *Client
	resource, name, policy string
}

func (g GuestIncarnation) ResourceID() string { return g.resource }
func (g GuestIncarnation) Close()             { g.kernel.Close() }
func (c *Client) guestPID(ctx context.Context, p Prepared) (int, error) {
	raw, e := c.read(ctx, "/1.0/instances/"+p.name+"/state")
	if e != nil {
		return 0, ErrNative
	}
	return parseGuestPID(raw)
}
func parseGuestPID(raw []byte) (int, error) {
	m, e := syncMetadata(raw)
	if e != nil {
		return 0, ErrNative
	}
	status, e := field[string](m, "status")
	code, ce := field[int](m, "status_code")
	pid, pe := field[int](m, "pid")
	if e != nil || ce != nil || pe != nil || status != "Running" || code != 103 || pid <= 1 {
		return 0, ErrNative
	}
	return pid, nil
}

// ObserveGuestIncarnation is called after the exact authorized START succeeds.
// UUID and namespace PID are read again after kernel observation, not trusted
// from the caller or a stale state response. A lost observation stays unknown.
func (c *Client) ObserveGuestIncarnation(ctx context.Context, p Prepared, resource string) (GuestIncarnation, error) {
	if c == nil || c.capsule == nil || !uuid(resource) || !c.matches(p) || c.CheckBoundary(ctx) != nil {
		return GuestIncarnation{}, ErrNative
	}
	before, e := c.ObserveResource(ctx, p, true)
	if e != nil || before != resource {
		return GuestIncarnation{}, ErrNative
	}
	pid, e := c.guestPID(ctx, p)
	if e != nil {
		return GuestIncarnation{}, ErrNative
	}
	kernel, e := c.capsule.ObserveGuestPID(ctx, pid, p.name, nativeboundary.Limits{MemoryBytes: p.allocation.RAM * (1 << 20), CPUMilli: p.allocation.CPU, Tasks: 4096})
	if e != nil {
		return GuestIncarnation{}, ErrNative
	}
	good := false
	defer func() {
		if !good {
			kernel.Close()
		}
	}()
	after, e := c.ObserveResource(ctx, p, true)
	if e != nil || after != resource {
		return GuestIncarnation{}, ErrNative
	}
	again, e := c.guestPID(ctx, p)
	if e != nil || again != pid || c.CheckBoundary(ctx) != nil {
		return GuestIncarnation{}, ErrNative
	}
	good = true
	return GuestIncarnation{kernel, c, resource, p.name, c.policy.Digest()}, nil
}

// ObserveGuestExit never substitutes an API404 for retained pidfd/cgroup proof.
// Disk destruction, canonical retirement claims and native deletion are separate.
type GuestExitObservation struct {
	kernel                 nativecapsule.GuestExit
	resource, name, policy string
	domain                 string
}

func (o GuestExitObservation) ResourceID() string    { return o.resource }
func (o GuestExitObservation) Name() string          { return o.name }
func (o GuestExitObservation) PolicyDigest() string  { return o.policy }
func (o GuestExitObservation) PID1Exited() bool      { return uuid(o.resource) && o.kernel.PID1Exited() }
func (o GuestExitObservation) CgroupEmpty() bool     { return uuid(o.resource) && o.kernel.CgroupEmpty() }
func (o GuestExitObservation) ObservedAt() time.Time { return o.kernel.ObservedAt() }
func (c *Client) ObserveGuestExit(ctx context.Context, p Prepared, g GuestIncarnation) (GuestExitObservation, error) {
	if c == nil || g.client != c || g.name != p.name || g.policy != c.PolicyDigest() || !uuid(g.resource) || !c.matches(p) || c.CheckBoundary(ctx) != nil {
		return GuestExitObservation{}, ErrNative
	}
	observation, e := g.kernel.ObserveExit(ctx)
	if e != nil {
		return GuestExitObservation{}, ErrNative
	}
	return GuestExitObservation{observation, g.resource, g.name, g.policy, g.DomainID()}, nil
}

func (g GuestIncarnation) DomainID() string {
	if g.client == nil {
		return ""
	}
	return g.client.policy.DomainID()
}

func (o GuestExitObservation) DomainID() string { return o.domain }
