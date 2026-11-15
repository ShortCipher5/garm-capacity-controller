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
	m.mu.Lock()
	defer m.mu.Unlock()
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
			for _, known := range []string{"id", "provider_name", "repo_id", "generation", "priority", "max_runners", "min_idle_runners", "enabled", "tags"} {
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
		pool, err := m.decodePool(fields, id)
		if err != nil {
			return PoolDiscovery{}, ErrManagement
		}
		// Do not expose hashes of opaque upstream provider specs/credentials through
		// discovery. Only allowlisted redacted fields contribute to this change token.
		pool.Fingerprint = ""
		pool.protected = ""
		canonical, err := json.Marshal(pool)
		if err != nil {
			return PoolDiscovery{}, ErrManagement
		}
		digest := sha256.Sum256(canonical)
		entry := DiscoveredPool{ManagedPool: pool, Configured: m.pools[id], DiscoveryFingerprint: hex.EncodeToString(digest[:])}
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
	management := &ManagementClient{client: client, base: strings.TrimSuffix(client.endpoint, "/instances"), pools: map[string]bool{}, repositories: map[string]bool{}}
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
