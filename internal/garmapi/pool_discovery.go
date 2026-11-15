package garmapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

// DiscoveredPool is observation only. Configured means its UUID was already in
// the management allowlist; discovery never adds permissions. The embedded
// Fingerprint is empty: DiscoveryFingerprint covers only redacted fields and is
// NOT a write precondition. Fetch Pool separately before an authorized edit.
type DiscoveredPool struct {
	ManagedPool
	OwnerKind            string `json:"owner_kind"`
	OwnerID              string `json:"owner_id,omitempty"`
	Configured           bool   `json:"configured"`
	DiscoveryFingerprint string `json:"discovery_fingerprint"`
}
type PoolDiscovery struct {
	Pools []DiscoveredPool `json:"pools"`
	// Missing means absent from this provider-scoped complete observation. It is
	// not proof of deletion: a pool may have moved to another provider. No pool
	// or runner is recreated, deleted, reconciled or released from this result.
	MissingConfiguredPoolIDs []string `json:"missing_configured_pool_ids"`
}

// DiscoverPools observes existing v0.2.1 /pools across the exact configured
// providers, including pools created directly in GARM since startup. Reads are
// fresh, not a cached or atomic multi-object revision. Failures return no partial
// discovery. No setting, route, allowlist or capacity-accounting state changes.
func (m *ManagementClient) DiscoverPools(ctx context.Context) (PoolDiscovery, error) {
	// Scope maps/base are constructor-owned and immutable. Collection reads do
	// not need the mutation mutex (and do not promise atomic upstream state).
	// Keep one client-side discovery attempt active without uninterruptible
	// queuing. Cancellation does not prove upstream processing has stopped.
	if m == nil || ctx == nil || ctx.Err() != nil || m.discoveryGate == nil {
		return PoolDiscovery{}, ErrManagement
	}
	select {
	case m.discoveryGate <- struct{}{}:
		defer func() { <-m.discoveryGate }()
	case <-ctx.Done():
		return PoolDiscovery{}, ErrManagement
	}
	if ctx.Err() != nil {
		return PoolDiscovery{}, ErrManagement
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/pools", nil)
	if err != nil {
		return PoolDiscovery{}, ErrManagement
	}
	req.Header.Set("Authorization", "Bearer "+m.client.token)
	req.Header.Set("Accept", "application/json")
	res, err := m.client.http.Do(req)
	if err != nil {
		return PoolDiscovery{}, ErrManagement
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return PoolDiscovery{}, ErrManagement
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || ctx.Err() != nil || len(raw) > 8<<20 || !utf8.Valid(raw) {
		return PoolDiscovery{}, ErrManagement
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return PoolDiscovery{}, ErrManagement
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || len(rows) > 10000 {
		return PoolDiscovery{}, ErrManagement
	}
	out := PoolDiscovery{Pools: []DiscoveredPool{}, MissingConfiguredPoolIDs: []string{}}
	seen := map[string]bool{}
	observed := map[string]bool{}
	size := 0
	for _, rawRow := range rows {
		fields, err := row(rawRow)
		if err != nil {
			return PoolDiscovery{}, ErrManagement
		}
		// JSON names are case-sensitive. Refuse decoder-like aliases rather than
		// presenting an upstream Enabled/Max_Runners value as omitted false/zero.
		for field := range fields {
			for _, known := range []string{"id", "provider_name", "repo_id", "org_id", "enterprise_id", "generation", "priority", "max_runners", "min_idle_runners", "enabled", "tags"} {
				if field != known && strings.EqualFold(field, known) {
					return PoolDiscovery{}, ErrManagement
				}
			}
		}
		id, err := textField(fields, "id", true)
		if err != nil || !managementID(id) || seen[id] {
			return PoolDiscovery{}, ErrManagement
		}
		seen[id] = true
		provider, err := textField(fields, "provider_name", true)
		if err != nil || !boundedSettingText(provider, 256) {
			return PoolDiscovery{}, ErrManagement
		}
		if !m.client.providers[provider] {
			continue
		}
		// Tag labels are shown as observed settings. A second case-folded name
		// must not be silently ignored in favour of the canonical spelling.
		if rawTags, ok := fields["tags"]; ok {
			var tags []json.RawMessage
			if json.Unmarshal(rawTags, &tags) != nil || len(tags) > 100 {
				return PoolDiscovery{}, ErrManagement
			}
			for _, tag := range tags {
				label, err := row(tag)
				if err != nil {
					return PoolDiscovery{}, ErrManagement
				}
				for key := range label {
					if key != "name" && strings.EqualFold(key, "name") {
						return PoolDiscovery{}, ErrManagement
					}
				}
			}
		}
		pool, err := m.decodePool(fields, id)
		if err != nil {
			return PoolDiscovery{}, ErrManagement
		}
		// Do not expose hashes of opaque upstream provider specs/credentials through
		// discovery. Only allowlisted redacted fields contribute to this change token.
		pool.Fingerprint = ""
		pool.protected = ""
		ownerKind, ownerID, err := discoveryPoolOwner(fields)
		if err != nil {
			return PoolDiscovery{}, ErrManagement
		}
		canonical, err := json.Marshal(struct {
			Pool               ManagedPool
			OwnerKind, OwnerID string
		}{pool, ownerKind, ownerID})
		if err != nil {
			return PoolDiscovery{}, ErrManagement
		}
		digest := sha256.Sum256(canonical)
		entry := DiscoveredPool{ManagedPool: pool, OwnerKind: ownerKind, OwnerID: ownerID, Configured: m.pools[id], DiscoveryFingerprint: hex.EncodeToString(digest[:])}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return PoolDiscovery{}, ErrManagement
		}
		size += len(encoded)
		if size > 1<<20 {
			return PoolDiscovery{}, ErrManagement
		}
		out.Pools = append(out.Pools, entry)
		observed[id] = true
	}
	for id := range m.pools {
		if !observed[id] {
			out.MissingConfiguredPoolIDs = append(out.MissingConfiguredPoolIDs, id)
		}
	}
	sort.Slice(out.Pools, func(i, j int) bool { return out.Pools[i].ID < out.Pools[j].ID })
	sort.Strings(out.MissingConfiguredPoolIDs)
	if ctx.Err() != nil {
		return PoolDiscovery{}, ErrManagement
	}
	return out, nil
}

// PoolDiscoveryClient exposes collection observation only: no Pool update,
// repository update, disabled-pool creation or enrollment API is available.
// It supports onboarding an existing GARM installation with zero configured
// pool UUIDs; provider names remain explicit operator scope.
type PoolDiscoveryClient struct{ management *ManagementClient }

func NewPoolDiscovery(endpoint, token string, trust *tls.Config, providers []string) (*PoolDiscoveryClient, error) {
	if len(providers) == 0 || len(providers) > 1000 {
		return nil, ErrManagement
	}
	for _, provider := range providers {
		if !boundedSettingText(provider, 256) || strings.TrimSpace(provider) != provider {
			return nil, ErrManagement
		}
	}
	client, err := New(endpoint, token, trust, providers, nil)
	if err != nil {
		return nil, ErrManagement
	}
	management := &ManagementClient{client: client, base: strings.TrimSuffix(client.endpoint, "/instances"), pools: map[string]bool{}, repositories: map[string]bool{}, discoveryGate: make(chan struct{}, 1)}
	return &PoolDiscoveryClient{management: management}, nil
}
func (c *PoolDiscoveryClient) Close() {
	if c != nil && c.management != nil {
		c.management.Close()
	}
}
func (c *PoolDiscoveryClient) DiscoverPools(ctx context.Context) (PoolDiscovery, error) {
	if c == nil || c.management == nil {
		return PoolDiscovery{}, ErrManagement
	}
	return c.management.DiscoverPools(ctx)
}

// Ownership is upstream metadata, not authorization. Missing ownership stays
// explicitly unknown for legacy observations. Never guess from provider/name.
func discoveryPoolOwner(fields map[string]json.RawMessage) (string, string, error) {
	kind, id := "unknown", ""
	for _, owner := range []struct{ field, kind string }{{"repo_id", "repository"}, {"org_id", "organization"}, {"enterprise_id", "enterprise"}} {
		value, err := textField(fields, owner.field, false)
		if err != nil || (value != "" && !managementID(value)) {
			return "", "", ErrManagement
		}
		if value == "" {
			continue
		}
		if id != "" {
			return "", "", ErrManagement
		}
		kind, id = owner.kind, value
	}
	return kind, id, nil
}
