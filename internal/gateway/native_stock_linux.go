//go:build linux

package gateway

import (
	"context"
	"reflect"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
	common "github.com/cloudbase/garm-provider-common/params"
)

// NativeStockPool is server-owned configuration. Neither metadata origins,
// native policy, owner credentials nor image descriptors come from provider JSON.
type NativeStockPool struct {
	Metadata  *stockjit.Client
	Ownership stockidentity.Configuration
	Hosts     map[string]nativecontroller.Configuration
}
type nativeStockHost struct {
	coordinator *nativecontroller.Coordinator
	policy      nativeapi.Policy
	binding     int64
}
type nativeStockPool struct {
	metadata  *stockjit.Client
	ownership stockidentity.Configuration
	authority admission.StockProviderAuthority
	hosts     map[string]nativeStockHost
}

// NativeStock is a concrete create-only bridge, separate from Facade's prepared
// and direct-Incus contracts. It cannot authorize native deletion or release.
type NativeStock struct {
	store      *admission.SQLiteStore
	controller string
	pools      map[string]nativeStockPool
}

func NewNativeStock(ctx context.Context, store *admission.SQLiteStore, controller string, pools map[string]NativeStockPool) (*NativeStock, error) {
	if store == nil || controller == "" || len(pools) == 0 || store.RuntimeLineageBound() || store.CheckOfflineBroker(ctx) != nil {
		return nil, ErrPolicy
	}
	profiles, e := store.CurrentBrokerPoolProfiles(ctx)
	if e != nil {
		return nil, ErrUnavailable
	}
	f := &NativeStock{store: store, controller: controller, pools: map[string]nativeStockPool{}}
	for id, config := range pools {
		profile, ok := profiles[id]
		if !ok || profile.Authority.Controller != controller || config.Metadata == nil || !config.Metadata.AuthorizesControllerPool(controller, id) || config.Metadata.ConfiguredSourceIdentity() != profile.SourceIdentity || len(config.Hosts) != len(profile.Domains) {
			return nil, ErrPolicy
		}
		if config.Ownership.Pool != id || config.Ownership.Provider != profile.Authority.ProviderName || config.Ownership.TLS == nil || config.Ownership.TLS.RootCAs == nil {
			return nil, ErrPolicy
		}
		managed := config.Ownership.ManagedServerKeyPin != ""
		if managed {
			if config.Ownership.Token != "" || config.Ownership.ManagedControllerID != controller || len(config.Ownership.TLS.Certificates) != 1 {
				return nil, ErrPolicy
			}
		} else if config.Ownership.ManagedControllerID != "" || len(config.Ownership.TLS.Certificates) != 0 {
			return nil, ErrPolicy
		}
		observed, e := stockidentity.Observe(ctx, config.Ownership)
		if e != nil || observed.SourceIdentity() != profile.SourceIdentity || observed.Facts().Repository != profile.Repository || observed.Facts().Fingerprint() != profile.PoolFingerprint {
			return nil, ErrPolicy
		}
		ownership := config.Ownership
		ownership.TLS = config.Ownership.TLS.Clone()
		ownership.TLS.RootCAs = config.Ownership.TLS.RootCAs.Clone()
		p := nativeStockPool{metadata: config.Metadata, ownership: ownership, authority: profile.Authority, hosts: map[string]nativeStockHost{}}
		for _, domain := range profile.Domains {
			host, ok := config.Hosts[domain.Host]
			if !ok || host.DomainBindingSequence != domain.Sequence || host.Policy.Digest() != domain.PolicyDigest || host.Policy.DomainID() != domain.DomainID || !host.Policy.Protected() {
				return nil, ErrPolicy
			}
			if _, ok := host.Policy.ProtectedImage(id); !ok {
				return nil, ErrPolicy
			}
			if host.Store != nil && host.Store != store {
				return nil, ErrPolicy
			}
			host.Store = store
			c, e := nativecontroller.New(ctx, host)
			if e != nil {
				return nil, ErrUnavailable
			}
			p.hosts[domain.Host] = nativeStockHost{c, host.Policy, domain.Sequence}
		}
		f.pools[id] = p
	}
	return f, nil
}
func (f *NativeStock) AuthorizesStockScope(controller, pool string) bool {
	if f == nil {
		return false
	}
	_, ok := f.pools[pool]
	return controller == f.controller && ok
}
func (f *NativeStock) RuntimeOriginalPreparationRequired() bool {
	return f == nil || f.store == nil || f.store.RuntimeLineageBound()
}
func (f *NativeStock) CreateStockProvider(ctx context.Context, b common.BootstrapInstance, authority admission.StockProviderAuthority) (common.ProviderInstance, error) {
	empty := common.ProviderInstance{}
	if f.RuntimeOriginalPreparationRequired() || f.store.CheckOfflineBroker(ctx) != nil {
		return empty, ErrUnavailable
	}
	route, ok := f.pools[b.PoolID]
	if !ok || authority != route.authority || authority.Controller != f.controller || b.OSType != common.Linux || !b.JitConfigEnabled || b.UserDataOptions.EnableBootDebug || len(b.UserDataOptions.ExtraPackages) != 0 || len(b.SSHKeys) != 0 {
		return empty, ErrPolicy
	}
	digest, e := StockCommandDigest(f.controller, authority.ProviderName, b)
	if e != nil {
		return empty, ErrPolicy
	}
	state, e := f.store.ReadBrokerGrantState(ctx)
	if e != nil {
		return empty, ErrUnavailable
	}
	profile, ok := state.PoolProfiles[b.PoolID]
	if !ok || profile.Authority != authority || profile.SourceIdentity != route.metadata.ConfiguredSourceIdentity() || profile.Execution.Flavor != b.Flavor || profile.Execution.Image != b.Image || profile.Execution.Arch != string(b.OSArch) || len(profile.Domains) != len(route.hosts) {
		return empty, ErrPolicy
	}
	hosts := make([]string, 0, len(profile.Domains))
	var imageSet bool
	var imagePolicy nativeapi.Policy
	for _, domain := range profile.Domains {
		host, ok := route.hosts[domain.Host]
		if !ok || host.binding != domain.Sequence || host.policy.Digest() != domain.PolicyDigest || host.policy.DomainID() != domain.DomainID {
			return empty, ErrPolicy
		}
		image, ok := host.policy.ProtectedImage(b.PoolID)
		if !ok {
			return empty, ErrPolicy
		}
		if imageSet {
			first, _ := imagePolicy.ProtectedImage(b.PoolID)
			if !reflect.DeepEqual(first, image) {
				return empty, ErrPolicy
			}
		} else {
			imagePolicy = host.policy
			imageSet = true
		}
		hosts = append(hosts, domain.Host)
	}
	image, ok := imagePolicy.ProtectedImage(b.PoolID)
	if !ok {
		return empty, ErrPolicy
	}
	// Fetch authentic credentials before charging/dispatch. Failure does not create
	// a stopped guest with a fabricated or unusable protected bootstrap payload.
	source, e := route.metadata.FetchProtected(ctx, f.controller, b, image, route.ownership)
	if e != nil || !source.Valid() || source.Instance().SourceIdentity() != profile.SourceIdentity || source.Instance().Facts().Repository != profile.Repository || source.Instance().Facts().Provider != authority.ProviderName {
		return empty, ErrUnavailable
	}
	execution := profile.Execution
	execution.Name = b.Name
	plan, e := f.store.AdmitStockProviderPlacement(ctx, admission.PlacementRequest{Key: key(f.controller, b.PoolID, b.Name), Pool: b.PoolID, Resources: profile.Resources, Execution: execution, Hosts: hosts, Strategy: "first_fit"}, authority, digest)
	if e != nil {
		return empty, ErrUnavailable
	}
	binding, e := f.store.LookupHostBinding(ctx, plan.Request.Host)
	if e != nil {
		return empty, ErrUnavailable
	}
	host, ok := route.hosts[plan.Request.Host]
	if !ok {
		return empty, ErrUnavailable
	}
	result, e := host.coordinator.ExecuteProtected(ctx, plan, binding, authority, digest, source)
	if e != nil || result.State != "confirmed" || result.ResourceID == "" {
		return empty, ErrUnavailable
	}
	// This is the first confirmed response only. Repeated Create is not translated
	// into a new mutation or an assertion that an old instance is still running.
	return common.ProviderInstance{ProviderID: b.Name, Name: b.Name, OSType: common.Linux, OSArch: b.OSArch, Status: common.InstanceRunning}, nil
}

func (f *NativeStock) AuthorizesStockAuthority(authority admission.StockProviderAuthority) bool {
	if f == nil {
		return false
	}
	route, ok := f.pools[authority.PoolID]
	return ok && route.authority == authority
}
