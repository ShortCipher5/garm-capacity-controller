package main

import (
	"context"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

// poolRouteSource is a typed trusted-server adapter only. There is no JSON proof
// decoder, HTTP route, startup wiring, catalog registration or activation here.
// The live manager derives scope from canonical confirmed journal history and a
// fresh exact source read; a caller cannot supply facts, JIT mode or actor grants.
type poolRouteSource struct {
	confirmed func(context.Context, string) (garmmanagement.DisabledPoolFacts, error)
}

var _ admission.DisabledPoolRegistrationSource = (*poolRouteSource)(nil)

// Construction has no IO. The caller must own a properly constructed live bound
// manager; this adapter neither creates/adopts a journal nor broadens pool scopes.
func newPoolRouteSource(manager *garmmanagement.Manager) (admission.DisabledPoolRegistrationSource, error) {
	if manager == nil {
		return nil, admission.ErrPoolRoute
	}
	return &poolRouteSource{confirmed: manager.ConfirmedDisabledPool}, nil
}
func (source *poolRouteSource) ConfirmedDisabledPool(ctx context.Context, requestID string) (admission.DisabledPoolFacts, error) {
	if source == nil || source.confirmed == nil {
		return admission.DisabledPoolFacts{}, admission.ErrPoolRoute
	}
	facts, err := source.confirmed(ctx, requestID)
	if err != nil || facts.RequestID != requestID {
		return admission.DisabledPoolFacts{}, admission.ErrPoolRoute
	}
	// Copy ALL typed facts without normalizing unsupported settings into safe
	// defaults or refreshing observation time. The registrar still applies its
	// fixed private references, age bounds and inactive-only transactional checks.
	return admission.DisabledPoolFacts{
		Protocol: facts.Protocol, ManagementIdentity: facts.ManagementIdentity, JournalLineage: facts.JournalLineage, RequestID: facts.RequestID, PoolID: facts.PoolID, RepositoryID: facts.RepositoryID, ProviderName: facts.ProviderName, ForgeEndpointName: facts.ForgeEndpointName, Generation: facts.Generation, SpecFingerprint: facts.SpecFingerprint, PoolFingerprint: facts.PoolFingerprint, Image: facts.Image, Flavor: facts.Flavor, OSType: facts.OSType, OSArch: facts.OSArch, TemplateID: facts.TemplateID, ShellEnabled: facts.ShellEnabled, ExtraSpecsEmpty: facts.ExtraSpecsEmpty, Enabled: facts.Enabled, MinIdleRunners: facts.MinIdleRunners, MaxRunners: facts.MaxRunners, ObservedAt: facts.ObservedAt,
	}, nil
}
