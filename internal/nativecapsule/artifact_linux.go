//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedevice"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
)

// StartWithArtifacts derives image and shared UID/journal paths only from the
// actual host-only quota root. Caller directory strings cannot supply a proof.
func StartWithArtifacts(ctx context.Context, c Configuration, limits nativeboundary.Observation, devices nativedevice.Boundary, budgets capsulepeer.PartitionSpec, artifacts nativezfs.HostArtifactRoot) (*Staged, error) {
	paths, e := artifacts.Paths(ctx)
	if e != nil || artifacts.ImageSHA256() != c.ImageSHA256 {
		return nil, ErrCapsule
	}
	c.Image = paths.Image
	c.Registry = paths.IDs
	c.StateParent = paths.State
	return startWithArtifacts(ctx, c, limits, devices, budgets, artifacts)
}

// ArtifactRoot observes the same retained construction root, never one selected
// by request or reconstructed from a descriptor. It grants no release authority.
func (r *Running) ArtifactRoot(ctx context.Context) (nativezfs.ArtifactRootObservation, error) {
	if r == nil || r.CheckScope(ctx) != nil {
		return nativezfs.ArtifactRootObservation{}, ErrCapsule
	}
	out, e := r.staged.artifacts.Observe(ctx)
	if e != nil || r.CheckScope(ctx) != nil || !out.Fresh() {
		return nativezfs.ArtifactRootObservation{}, ErrCapsule
	}
	return out, nil
}
