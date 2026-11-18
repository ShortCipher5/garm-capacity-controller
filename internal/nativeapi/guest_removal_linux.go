//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecapsule"
	"time"
)

// GuestCgroupRemovalObservation is exact-kernel-object disappearance, not a
// whole guest absence certificate, native DELETE receipt or disk release proof.
type GuestCgroupRemovalObservation struct {
	kernel                         nativecapsule.GuestCgroupRemoval
	domain, policy, name, resource string
}

func (o GuestCgroupRemovalObservation) Removed() bool         { return uuid(o.resource) && o.kernel.Removed() }
func (o GuestCgroupRemovalObservation) DomainID() string      { return o.domain }
func (o GuestCgroupRemovalObservation) PolicyDigest() string  { return o.policy }
func (o GuestCgroupRemovalObservation) Name() string          { return o.name }
func (o GuestCgroupRemovalObservation) ResourceID() string    { return o.resource }
func (o GuestCgroupRemovalObservation) ObservedAt() time.Time { return o.kernel.ObservedAt() }
func (c *Client) ObserveGuestCgroupRemoval(ctx context.Context, p Prepared, g GuestIncarnation) (GuestCgroupRemovalObservation, error) {
	if c == nil || g.client != c || g.name != p.name || g.policy != c.PolicyDigest() || !uuid(g.resource) || !c.matches(p) || c.CheckBoundary(ctx) != nil {
		return GuestCgroupRemovalObservation{}, ErrNative
	}
	o, e := g.kernel.ObserveCgroupRemoval(ctx)
	if e != nil {
		return GuestCgroupRemovalObservation{}, ErrNative
	}
	return GuestCgroupRemovalObservation{o, g.DomainID(), g.policy, g.name, g.resource}, nil
}
