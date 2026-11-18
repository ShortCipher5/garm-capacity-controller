//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecapsule"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"net/http"
	"time"
)

func (c *Client) domainID() string {
	if c.capsule != nil {
		return c.capsule.BirthID()
	}
	return c.domain.Birth().ID
}

// NewCapsule consumes the actual authenticated capsule and provisions only its
// exact disposable pool child and isolated bridge. Failure cannot be retried or
// adopted. This does not enable runtime allocation or qualify network isolation.
func NewCapsule(ctx context.Context, r *nativecapsule.Running, p Policy) (*Client, error) {
	if p.spec.EgressDigest != "" {
		return nil, ErrPolicy
	}
	return newCapsule(ctx, r, p, nil)
}
func NewCapsuleWithEgress(ctx context.Context, r *nativecapsule.Running, p Policy, network nativeegress.Birth) (*Client, error) {
	if ctx == nil || r == nil || r.CheckScope(ctx) != nil || p.spec.Version != PartitionPolicyVersion || p.spec.EgressDigest != network.Policy().Digest() || p.spec.EgressDigest != r.EgressDigest() || network.Record().BirthID != r.BirthID() || network.Recheck(ctx) != nil {
		return nil, ErrNative
	}
	return newCapsule(ctx, r, p, &network)
}
func newCapsule(ctx context.Context, r *nativecapsule.Running, p Policy, network *nativeegress.Birth) (*Client, error) {
	if ctx == nil || r == nil || !hash(p.Digest()) || r.CheckScope(ctx) != nil || r.BirthID() != p.spec.DomainID || r.NativeSHA256() != p.spec.NativeSHA256 || r.LauncherSHA256() != p.spec.LauncherSHA256 || p.spec.EgressDigest != r.EgressDigest() {
		return nil, ErrNative
	}
	rec := r.Storage()
	for _, pool := range p.spec.Pools {
		if pool.ZFSDataset != rec.Dataset+"/pool" {
			return nil, ErrPolicy
		}
	}
	var np *nativeegress.Policy
	if network != nil {
		v := network.Policy()
		np = &v
	}
	plan, e := p.planFreshResources(rec.Dataset, np)
	if e != nil {
		return nil, e
	}
	if !r.ClaimClient() {
		return nil, ErrNative
	}
	c := &Client{capsule: r, policy: p, http: &http.Client{Transport: r, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	identity, e := c.Identity(ctx)
	if e != nil {
		return nil, e
	}
	c.identity = identity
	guard := func(ctx context.Context) error {
		if r.CheckScope(ctx) != nil {
			return ErrNative
		}
		if network != nil {
			return network.Recheck(ctx)
		}
		return nil
	}
	e = resourceBootstrap(ctx, plan, c.http, guard, r.BeginProvisioning, r.FinishProvisioning)
	if e != nil {
		return nil, e
	}
	return c, nil
}
