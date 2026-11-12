package gateway

import (
	"context"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// Identity sources are supplied only by the credential-owning gateway, never by
// a provider request, browser JSON or a configured 'qualified' boolean.
type identityBackend interface {
	PhysicalIdentity(context.Context) (incusapi.PhysicalServerIdentity, error)
	PinPhysicalIdentity(incusapi.PhysicalServerIdentity) error
}

func observedBinding(host string, identity incusapi.PhysicalServerIdentity, at time.Time) (admission.HostBinding, error) {
	if !identity.Valid() {
		return admission.HostBinding{}, ErrUnavailable
	}
	return admission.HostBinding{Host: host, ServerKeyFingerprint: identity.ServerKeyFingerprint(), OriginFingerprint: identity.OriginFingerprint(), ScopeFingerprint: identity.ScopeFingerprint(), ServerName: identity.ServerName(), ServerVersion: identity.ServerVersion(), ObservedAt: at}, nil
}
func (f *Facade) bindBackends(ctx context.Context, fence admission.Fence, observedAt time.Time) error {
	bindings := make([]admission.HostBinding, 0, len(f.hosts))
	observations := map[string]incusapi.PhysicalServerIdentity{}
	sources := map[string]identityBackend{}
	for host, backend := range f.hosts {
		source, ok := backend.(identityBackend)
		if !ok {
			return ErrUnavailable
		}
		identity, err := source.PhysicalIdentity(ctx)
		if err != nil {
			return ErrUnavailable
		}
		binding, err := observedBinding(host, identity, observedAt)
		if err != nil {
			return err
		}
		bindings = append(bindings, binding)
		observations[host] = identity
		sources[host] = source
	}
	// The full set must pass durable uniqueness BEFORE inventory or any new claim.
	if err := f.store.BindHostIdentities(ctx, fence, bindings); err != nil {
		return ErrUnavailable
	}
	for host, source := range sources {
		if err := source.PinPhysicalIdentity(observations[host]); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}
func (f *Facade) boundBackend(ctx context.Context, host string) (admission.HostBinding, error) {
	backend, ok := f.hosts[host]
	if !ok {
		return admission.HostBinding{}, ErrPolicy
	}
	source, ok := backend.(identityBackend)
	if !ok {
		return admission.HostBinding{}, ErrUnavailable
	}
	identity, err := source.PhysicalIdentity(ctx)
	if err != nil {
		return admission.HostBinding{}, ErrUnavailable
	}
	observed, err := observedBinding(host, identity, time.Now())
	if err != nil {
		return admission.HostBinding{}, err
	}
	binding, err := f.store.LookupHostBinding(ctx, host)
	if err != nil || !binding.Matches(observed) {
		return admission.HostBinding{}, ErrUnavailable
	}
	if err := source.PinPhysicalIdentity(identity); err != nil {
		return admission.HostBinding{}, ErrUnavailable
	}
	return binding, nil
}
