//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
)

func (c *Client) ArtifactRoot(ctx context.Context) (nativezfs.ArtifactRootObservation, error) {
	if c == nil || c.capsule == nil || c.CheckBoundary(ctx) != nil {
		return nativezfs.ArtifactRootObservation{}, ErrNative
	}
	out, e := c.capsule.ArtifactRoot(ctx)
	if e != nil || c.CheckBoundary(ctx) != nil || !out.Fresh() {
		return nativezfs.ArtifactRootObservation{}, ErrNative
	}
	return out, nil
}
