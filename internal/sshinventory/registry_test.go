package sshinventory

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"testing"
)

type memoryTargets struct {
	values map[string]Config
	fail   bool
	saves  int
}

func (m *memoryTargets) RevokeSSHTarget(_ context.Context, _ string, id, digest string) error {
	c, ok := m.values[id]
	if m.fail || !ok || ReviewDigest(id, c) != digest {
		return ErrReview
	}
	delete(m.values, id)
	return nil
}
func (m *memoryTargets) ListSSHTargets(context.Context) (map[string]Config, error) {
	if m.fail {
		return nil, ErrInventory
	}
	out := map[string]Config{}
	for id, c := range m.values {
		out[id] = c
	}
	return out, nil
}
func (m *memoryTargets) SaveSSHTarget(_ context.Context, _ string, id string, c Config, _ string, _ int) error {
	if m.fail {
		return ErrInventory
	}
	m.saves++
	m.values[id] = c
	return nil
}
func TestRegistrationNeverUsesCredentialsAndRevalidatesPolicy(t *testing.T) {
	c, _, _ := server(t, sample)
	c.Address = "192.0.2.20"
	c.Port = 22
	c.User = "inventory"
	c.CredentialRef = "key"
	store := &memoryTargets{values: map[string]Config{}}
	calls := 0
	resolve := func(context.Context, string) (ssh.Signer, error) { calls++; return nil, ErrInventory }
	scopes := map[string]Scope{"key": {Networks: []string{"192.0.2.0/24"}, Users: []string{"inventory"}, Ports: []uint16{22}}}
	r, e := NewRegistry(scopes, store, resolve, nil)
	if e != nil {
		t.Fatal(e)
	}
	scopes["key"].Networks[0] = "198.51.100.0/24" // caller cannot expand/change policy after construction
	target, e := r.Review("candidate", c)
	if e != nil {
		t.Fatal(e)
	}
	changed := c
	changed.Address = "192.0.2.21"
	request := "11111111-1111-4111-8111-111111111111"
	if _, e = r.Save(context.Background(), request, "candidate", changed, target.ReviewDigest); e == nil || store.saves != 0 {
		t.Fatal("changed review saved")
	}
	if _, e = r.Save(context.Background(), request, "candidate", c, target.ReviewDigest); e != nil || calls != 0 {
		t.Fatal("save resolved credentials", e, calls)
	}
	service, _ := NewService(nil, resolve, func(context.Context, string, string, string, string, bool) error { return nil })
	service.SetRegistry(r)
	list, e := service.ListTargets(context.Background())
	if e != nil || len(list) != 1 || list[0] != target {
		t.Fatal(list, e)
	}
	changedScope := map[string]Scope{"key": {Networks: []string{"198.51.100.0/24"}, Users: []string{"inventory"}, Ports: []uint16{22}}}
	revoked, _ := NewRegistry(changedScope, store, resolve, nil)
	service.SetRegistry(revoked)
	list, e = service.ListTargets(context.Background())
	if e != nil || len(list) != 0 {
		t.Fatal("revoked target remained visible")
	}
	if _, e = service.Probe(context.Background(), target.ID, target.ReviewDigest, request); !errors.Is(e, ErrReview) || calls != 0 {
		t.Fatal("revoked target reached credentials")
	}
	store.fail = true
	if _, e = service.ListTargets(context.Background()); e == nil {
		t.Fatal("failed registry appeared empty")
	}
}
func TestRegistryRejectsOutOfScopeAndSpecialDestinations(t *testing.T) {
	c, resolve, _ := server(t, sample)
	c.Address = "192.0.2.20"
	c.Port = 22
	c.User = "inventory"
	c.CredentialRef = "key"
	scope := Scope{Networks: []string{"192.0.2.0/24"}, Users: []string{"inventory"}, Ports: []uint16{22}}
	r, _ := NewRegistry(map[string]Scope{"key": scope}, &memoryTargets{}, resolve, map[string]Config{"reserved": c})
	for _, address := range []string{"198.51.100.2", "127.0.0.1", "169.254.169.254", "::ffff:192.0.2.20", "example.invalid"} {
		bad := c
		bad.Address = address
		if _, e := r.Review("new", bad); e == nil {
			t.Fatal(address)
		}
	}
	for _, change := range []func(*Config){func(c *Config) { c.User = "root" }, func(c *Config) { c.Port = 2222 }, func(c *Config) { c.CredentialRef = "other" }, func(c *Config) { c.HostKey = "bad" }} {
		bad := c
		change(&bad)
		if _, e := r.Review("new", bad); e == nil {
			t.Fatal("scope escaped")
		}
	}
	if _, e := r.Review("reserved", c); e == nil {
		t.Fatal("static target overwritten")
	}
	for _, network := range []string{"0.0.0.0/0", "192.0.2.1/24", "::/0", "192.0.0.0/16"} {
		bad := scope
		bad.Networks = []string{network}
		if _, e := NewRegistry(map[string]Scope{"key": bad}, &memoryTargets{}, resolve, nil); e == nil {
			t.Fatal(network)
		}
	}
}

func TestRevocationRejectsPreviouslyReviewedProbeWithoutCredentialUse(t *testing.T) {
	c, _, _ := server(t, sample)
	c.Address = "192.0.2.20"
	c.Port = 22
	c.User = "inventory"
	c.CredentialRef = "key"
	store := &memoryTargets{values: map[string]Config{"candidate": c}}
	calls := 0
	resolve := func(context.Context, string) (ssh.Signer, error) { calls++; return nil, ErrInventory }
	registry, e := NewRegistry(map[string]Scope{"key": {Networks: []string{"192.0.2.0/24"}, Users: []string{"inventory"}, Ports: []uint16{22}}}, store, resolve, nil)
	if e != nil {
		t.Fatal(e)
	}
	service, _ := NewService(nil, resolve, func(context.Context, string, string, string, string, bool) error { return nil })
	service.SetRegistry(registry)
	targets, e := service.ListTargets(context.Background())
	if e != nil || len(targets) != 1 {
		t.Fatal(targets, e)
	}
	target := targets[0]
	request := "11111111-1111-4111-8111-111111111111"
	if e = registry.Revoke(context.Background(), request, target.ID, target.ReviewDigest); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Probe(context.Background(), target.ID, target.ReviewDigest, request); !errors.Is(e, ErrReview) || calls != 0 {
		t.Fatal("stale target reached credential")
	}
	targets, e = registry.ManagedTargets(context.Background())
	if e != nil || len(targets) != 0 {
		t.Fatal("revoked record remained active")
	}
}
