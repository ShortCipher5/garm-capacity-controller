package main

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"time"
)

type nativeRetirementConfiguration struct {
	Owners          []nativeStockHostConfiguration `json:"owners"`
	IntervalSeconds int                            `json:"interval_seconds"`
}

func validateNativeRetirement(c configuration) error {
	if c.NativeRetirement == nil {
		if c.EnableNativeRetirement {
			return nativecontroller.ErrConfiguration
		}
		return nil
	}
	v := c.NativeRetirement
	if !c.EnableNativeRetirement || c.Admin == nil || v.IntervalSeconds < 1 || v.IntervalSeconds > 3600 || len(v.Owners) == 0 || len(v.Owners) > 100 {
		return nativecontroller.ErrConfiguration
	}
	return nil
}

// Explicit retirement configuration does not qualify runtime authority. Refuse
// active stores before private key reads, owner contact or service construction.
func buildNativeRetirement(ctx context.Context, c configuration, s *admission.SQLiteStore) (*nativecontroller.RetirementService, error) {
	if err := validateNativeRetirement(c); err != nil {
		return nil, err
	}
	if c.NativeRetirement == nil {
		return nil, nil
	}
	if ctx == nil || ctx.Err() != nil || s == nil {
		return nil, nativecontroller.ErrConfiguration
	}
	if s.RuntimeLineageBound() {
		return nil, nativecontroller.ErrUnqualified
	}
	owners := []nativecontroller.Configuration{}
	for _, h := range c.NativeRetirement.Owners {
		policy, e := nativeapi.NewPolicy(h.Policy)
		if e != nil || policy.Controller() != c.Controller {
			return nil, nativecontroller.ErrConfiguration
		}
		trust, e := securefiles.LoadTLS(h.OwnerTLS)
		if e != nil {
			return nil, e
		}
		owners = append(owners, nativecontroller.Configuration{Store: s, Policy: policy, DomainBindingSequence: h.DomainBindingSequence, Owner: nativeidentity.Configuration{Endpoint: h.OwnerEndpoint, TLS: trust, DomainID: policy.DomainID(), PolicyDigest: policy.Digest(), OwnerClientLeaf: h.OwnerClientLeaf, OwnerServerLeaf: h.OwnerServerLeaf}})
	}
	return nativecontroller.NewRetirementService(ctx, nativecontroller.RetirementConfiguration{Store: s, Owners: owners, Interval: time.Duration(c.NativeRetirement.IntervalSeconds) * time.Second})
}
