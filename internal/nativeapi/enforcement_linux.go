//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
)

// Enforcement refuses every unqualified boundary, including the current capsule
// path. The identity endpoint alone is never promoted to enforcement authority.
func (c *Client) Enforcement(ctx context.Context) (nativeenforcement.Descriptor, error) {
	if c == nil || c.capsule == nil || c.CheckBoundary(ctx) != nil {
		return nativeenforcement.Descriptor{}, ErrNative
	}
	d, e := c.capsule.EnforcementSnapshot(ctx)
	if e != nil || c.CheckBoundary(ctx) != nil {
		return nativeenforcement.Descriptor{}, ErrNative
	}
	return d, nil
}
