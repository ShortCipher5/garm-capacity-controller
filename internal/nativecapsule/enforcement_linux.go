//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
)

// EnforcementSnapshot reads actual retained construction and current kernel
// boundaries. It is not qualification or permission to activate a provider.
func (r *Running) EnforcementSnapshot(ctx context.Context) (nativeenforcement.Descriptor, error) {
	if r.CheckScope(ctx) != nil {
		return nativeenforcement.Descriptor{}, ErrCapsule
	}
	old := r.staged.limits
	now, err := nativeboundary.Observe(old.Limits())
	if err != nil || now.RootInode() != old.RootInode() || now.NamespaceInode() != old.NamespaceInode() {
		return nativeenforcement.Descriptor{}, ErrCapsule
	}
	limits := now.Limits()
	disk := r.storage.Record().QuotaBytes
	if disk <= 0 || r.staged.imageSHA == "" {
		return nativeenforcement.Descriptor{}, ErrCapsule
	}
	tuple := nativeenforcement.Limits{MemoryBytes: uint64(limits.MemoryBytes), CPUMilli: uint64(limits.CPUMilli), Tasks: uint64(limits.Tasks), DiskBytes: uint64(disk)}
	digest, err := nativeenforcement.DigestLimits(tuple)
	if err != nil || r.CheckScope(ctx) != nil {
		return nativeenforcement.Descriptor{}, ErrCapsule
	}
	return nativeenforcement.Descriptor{CapsuleBirthID: r.BirthID(), RootImageSHA256: r.staged.imageSHA, DeviceProgramSHA256: r.staged.devices.Record().ProgramSHA256, EgressPolicySHA256: r.EgressDigest(), LimitsDigest: digest, Limits: tuple}, nil
}
