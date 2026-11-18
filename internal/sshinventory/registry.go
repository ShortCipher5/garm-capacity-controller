package sshinventory

import (
	"context"
	"net/netip"
	"slices"
	"sort"
)

// Scope restricts the credential's destinations independently of browser input.
// A dedicated target-management role can approve exact tuples only inside it.
type Scope struct {
	Networks []string `json:"networks"`
	Users    []string `json:"users"`
	Ports    []uint16 `json:"ports"`
}
type TargetStore interface {
	RevokeSSHTarget(context.Context, string, string, string) error
	ListSSHTargets(context.Context) (map[string]Config, error)
	SaveSSHTarget(context.Context, string, string, Config, string, int) error
}
type Registry struct {
	scopes   map[string]Scope
	store    TargetStore
	resolve  KeyResolver
	reserved map[string]bool
}

func NewRegistry(scopes map[string]Scope, store TargetStore, resolve KeyResolver, reserved map[string]Config) (*Registry, error) {
	if len(scopes) == 0 || len(scopes) > 32 || store == nil || resolve == nil {
		return nil, ErrReview
	}
	r := &Registry{scopes: map[string]Scope{}, store: store, resolve: resolve, reserved: map[string]bool{}}
	for id := range reserved {
		r.reserved[id] = true
	}
	for alias, s := range scopes {
		if !targetID.MatchString(alias) || len(s.Networks) == 0 || len(s.Networks) > 32 || len(s.Users) == 0 || len(s.Users) > 16 || len(s.Ports) == 0 || len(s.Ports) > 16 {
			return nil, ErrReview
		}
		for _, n := range s.Networks {
			p, e := netip.ParsePrefix(n)
			if e != nil || p != p.Masked() || p.Addr().Is4In6() || (p.Addr().Is4() && p.Bits() < 24) || (p.Addr().Is6() && p.Bits() < 64) {
				return nil, ErrReview
			}
		}
		for _, u := range s.Users {
			if !username.MatchString(u) {
				return nil, ErrReview
			}
		}
		for _, p := range s.Ports {
			if p == 0 {
				return nil, ErrReview
			}
		}
		r.scopes[alias] = Scope{slices.Clone(s.Networks), slices.Clone(s.Users), slices.Clone(s.Ports)}
	}
	return r, nil
}
func (r *Registry) Scopes() map[string]Scope {
	out := map[string]Scope{}
	for alias, s := range r.scopes {
		out[alias] = Scope{slices.Clone(s.Networks), slices.Clone(s.Users), slices.Clone(s.Ports)}
	}
	return out
}
func (r *Registry) Review(id string, c Config) (Target, error) {
	if !targetID.MatchString(id) || r.reserved[id] {
		return Target{}, ErrReview
	}
	s, ok := r.scopes[c.CredentialRef]
	ip, e := netip.ParseAddr(c.Address)
	if !ok || e != nil || ip.Is4In6() || ip.String() != c.Address || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || !slices.Contains(s.Users, c.User) || !slices.Contains(s.Ports, c.Port) {
		return Target{}, ErrReview
	}
	allowed := false
	for _, n := range s.Networks {
		p, _ := netip.ParsePrefix(n)
		if p.Contains(ip) {
			allowed = true
		}
	}
	if !allowed {
		return Target{}, ErrReview
	}
	client, e := New(c, r.resolve)
	if e != nil {
		return Target{}, ErrReview
	}
	return describeTarget(id, c, client), nil
}
func (r *Registry) Save(ctx context.Context, request, id string, c Config, digest string) (Target, error) {
	t, e := r.Review(id, c)
	if e != nil || !requestID.MatchString(request) || digest != t.ReviewDigest {
		return Target{}, ErrReview
	}
	if e = r.store.SaveSSHTarget(ctx, request, id, c, digest, 32-len(r.reserved)); e != nil {
		return Target{}, ErrReview
	}
	return t, nil
}
func (r *Registry) targets(ctx context.Context) (map[string]Config, error) {
	configs, e := r.store.ListSSHTargets(ctx)
	if e != nil || len(configs) > 32 {
		return nil, ErrInventory
	}
	for id, c := range configs {
		if _, e = r.Review(id, c); e != nil {
			delete(configs, id)
		}
	} // revoked deployment scopes cannot authorize a probe
	return configs, nil
}

// ManagedTargets includes active durable records even when the current scope
// disallows probing, so an operator can still withdraw their local permission.
func (r *Registry) ManagedTargets(ctx context.Context) ([]Target, error) {
	configs, e := r.store.ListSSHTargets(ctx)
	if e != nil || len(configs) > 32 {
		return nil, ErrInventory
	}
	out := make([]Target, 0, len(configs))
	for id, c := range configs {
		client, e := New(c, r.resolve)
		if e != nil || !targetID.MatchString(id) {
			return nil, ErrInventory
		}
		out = append(out, describeTarget(id, c, client))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (r *Registry) Revoke(ctx context.Context, request, id, digest string) error {
	if !requestID.MatchString(request) || !targetID.MatchString(id) || r.reserved[id] || len(digest) != 64 {
		return ErrReview
	}
	if e := r.store.RevokeSSHTarget(ctx, request, id, digest); e != nil {
		return ErrReview
	}
	return nil
}
