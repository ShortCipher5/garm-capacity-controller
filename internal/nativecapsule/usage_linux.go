//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
)

// Usage observes the complete owned cgroup, including retained kernel charges.
// It must not be subtracted from a reservation or treated as a usage ceiling.
func (r *Running) Usage(ctx context.Context) (nativeboundary.Usage, error) {
	if r.CheckScope(ctx) != nil {
		return nativeboundary.Usage{}, ErrCapsule
	}
	u, e := nativeboundary.ObserveUsage(ctx, r.staged.limits)
	if e != nil || r.CheckScope(ctx) != nil || !u.Valid() {
		return nativeboundary.Usage{}, ErrCapsule
	}
	return u, nil
}
