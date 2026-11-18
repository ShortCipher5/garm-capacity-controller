//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedomain"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
)

type enforcedBoundary struct {
	cgroup  nativeboundary.Observation
	storage nativeboundary.StorageObservation
	reader  *nativequota.Reader
}

// NewBounded binds actual pre-birth observations to the fresh daemon's immutable
// directory and namespace, then re-reads enforcement. No bool/path-only adapter.
// Failure consumes that domain's client slot; it cannot fall back to unbounded.
func NewBounded(ctx context.Context, d *nativedomain.Domain, p Policy, cg nativeboundary.Observation, disk nativeboundary.StorageObservation, r *nativequota.Reader) (*Client, error) {
	if d == nil || !cg.Valid() || !disk.Valid() || r == nil || d.Directory() != disk.DataDirectory() || d.CgroupNamespace() != cg.NamespaceInode() {
		return nil, ErrNative
	}
	c, e := New(ctx, d, p)
	if e != nil {
		return nil, e
	}
	c.boundary = &enforcedBoundary{cg, disk, r}
	if c.CheckBoundary(ctx) != nil {
		return nil, ErrNative
	}
	return c, nil
}

// CheckBoundary is actual readback, not a stored readiness flag. It never repairs
// or relaxes limits and cannot authorize allocation or retirement on its own.
func (c *Client) CheckBoundary(ctx context.Context) error {
	if c == nil || c.domain == nil || c.boundary == nil {
		return ErrNative
	}
	b := c.boundary
	cg, e := nativeboundary.Observe(b.cgroup.Limits())
	if e != nil || cg.NamespaceInode() != b.cgroup.NamespaceInode() || cg.RootInode() != b.cgroup.RootInode() || c.domain.CgroupNamespace() != cg.NamespaceInode() || c.domain.Directory() != b.storage.DataDirectory() {
		return ErrNative
	}
	if nativeboundary.RecheckStorage(ctx, b.storage, b.reader) != nil {
		return ErrNative
	}
	return nil
}
