//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
	"time"
)

// StorageReclamationTracker owns an actual before-START storage lifetime and
// full-pool reference baseline; it cannot be rebuilt from a persisted receipt.
type StorageReclamationTracker struct {
	tracker                        nativequota.ReclamationTracker
	client                         *Client
	domain, policy, name, resource string
}
type StorageReclamationObservation struct {
	accounting                     nativequota.ReclamationObservation
	domain, policy, name, resource string
}

func (o StorageReclamationObservation) DomainID() string     { return o.domain }
func (o StorageReclamationObservation) PolicyDigest() string { return o.policy }
func (o StorageReclamationObservation) Name() string         { return o.name }
func (o StorageReclamationObservation) ResourceID() string   { return o.resource }
func (o StorageReclamationObservation) Accounting() nativequota.ReclamationObservation {
	return o.accounting
}
func (o StorageReclamationObservation) ObservedAt() time.Time { return o.accounting.ObservedAt() }
func (c *Client) CaptureStorageReclamation(ctx context.Context, p Prepared, q StoppedQuota, reader *nativequota.Reader, pools *nativequota.PoolReader) (StorageReclamationTracker, error) {
	l, e := c.CaptureDatasetLifetime(ctx, p, q, reader)
	if e != nil {
		return StorageReclamationTracker{}, ErrNative
	}
	t, e := l.life.CaptureReclamation(ctx, pools)
	if e != nil || c.CheckBoundary(ctx) != nil {
		return StorageReclamationTracker{}, ErrNative
	}
	return StorageReclamationTracker{t, c, l.domain, l.policy, l.name, l.resource}, nil
}
func (c *Client) ObserveStorageReclamation(ctx context.Context, p Prepared, t StorageReclamationTracker) (StorageReclamationObservation, error) {
	if c == nil || t.client != c || t.domain != c.policy.DomainID() || t.policy != c.policy.Digest() || t.name != p.name || !uuid(t.resource) || !c.matches(p) || c.CheckBoundary(ctx) != nil {
		return StorageReclamationObservation{}, ErrNative
	}
	o, e := t.tracker.ObserveReclamation(ctx)
	if e != nil || c.CheckBoundary(ctx) != nil || !o.Fresh() {
		return StorageReclamationObservation{}, ErrNative
	}
	return StorageReclamationObservation{o, t.domain, t.policy, t.name, t.resource}, nil
}
