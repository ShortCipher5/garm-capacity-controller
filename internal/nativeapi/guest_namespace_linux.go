//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecapsule"
	"time"
)

type PIDNamespaceTerminationObservation struct {
	kernel                         nativecapsule.PIDNamespaceTermination
	domain, policy, name, resource string
}

func (o PIDNamespaceTerminationObservation) Terminated() bool {
	return uuid(o.resource) && o.kernel.Terminated()
}
func (o PIDNamespaceTerminationObservation) DomainID() string       { return o.domain }
func (o PIDNamespaceTerminationObservation) PolicyDigest() string   { return o.policy }
func (o PIDNamespaceTerminationObservation) Name() string           { return o.name }
func (o PIDNamespaceTerminationObservation) ResourceID() string     { return o.resource }
func (o PIDNamespaceTerminationObservation) ObservedAt() time.Time  { return o.kernel.ObservedAt() }
func (o PIDNamespaceTerminationObservation) NamespaceInode() uint64 { return o.kernel.NamespaceInode() }
func (c *Client) ObservePIDNamespaceTermination(ctx context.Context, p Prepared, g GuestIncarnation) (PIDNamespaceTerminationObservation, error) {
	if c == nil || g.client != c || g.name != p.name || g.policy != c.PolicyDigest() || !uuid(g.resource) || !c.matches(p) || c.CheckBoundary(ctx) != nil {
		return PIDNamespaceTerminationObservation{}, ErrNative
	}
	o, e := g.kernel.ObservePIDNamespaceTermination(ctx)
	if e != nil {
		return PIDNamespaceTerminationObservation{}, ErrNative
	}
	return PIDNamespaceTerminationObservation{o, g.DomainID(), g.policy, g.name, g.resource}, nil
}
