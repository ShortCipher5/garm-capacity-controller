//go:build linux

package nativecontroller

import (
	"context"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeretained"
)

// ObserveRetirementDiagnostics reads fresh retained observations only after an
// exact canonical successful DELETE. It never changes lifecycle history or
// refunds capacity, and missing evidence cannot cause another native mutation.
func (c *Coordinator) ObserveRetirementDiagnostics(ctx context.Context, permit string) (nativeretained.Observation, error) {
	if c == nil || c.store == nil || ctx == nil || ctx.Err() != nil {
		return nativeretained.Observation{}, ErrConfiguration
	}
	if c.store.RuntimeLineageBound() {
		return nativeretained.Observation{}, ErrUnqualified
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	intent, _, e := c.store.LookupNativeRetirement(ctx, permit)
	if e != nil || intent.DomainID != c.owner.DomainID || intent.PolicyDigest != c.policy.Digest() {
		return nativeretained.Observation{}, ErrUnknown
	}
	claim, e := c.store.LookupNativeRetirementClaim(ctx, permit, admission.NativeRetireDelete)
	if e != nil {
		return nativeretained.Observation{}, ErrUnknown
	}
	receipt, e := c.store.LookupNativeRetirementReceipt(ctx, claim.ID, "succeeded")
	if e != nil || receipt.OwnerServerLeaf != c.owner.OwnerServerLeaf {
		return nativeretained.Observation{}, ErrUnknown
	}
	identity, e := nativeidentity.Observe(ctx, c.owner)
	if e != nil {
		return nativeretained.Observation{}, ErrUnknown
	}
	measured, e := admission.ObservedNativeHostBinding(c.binding.Spec.Host, identity)
	if e != nil {
		return nativeretained.Observation{}, ErrUnknown
	}
	binding, e := c.store.LookupHostBinding(ctx, c.binding.Spec.Host)
	if e != nil || binding.Sequence != c.binding.Spec.HostBindingSequence || !binding.Matches(measured) {
		return nativeretained.Observation{}, ErrUnknown
	}
	observation, e := nativeretained.Observe(ctx, nativeretained.Configuration{Endpoint: c.owner.Endpoint, TLS: c.owner.TLS, Expected: identity.Record(), Delete: receipt.Record})
	if e != nil || !observation.Valid() {
		return nativeretained.Observation{}, ErrUnknown
	}
	// Do not report an observation obtained across a canonical host rebind.
	current, e := c.store.LookupHostBinding(ctx, c.binding.Spec.Host)
	if e != nil || current.Sequence != binding.Sequence || !current.Matches(measured) || ctx.Err() != nil || !observation.Valid() {
		return nativeretained.Observation{}, ErrUnknown
	}
	return observation, nil
}
