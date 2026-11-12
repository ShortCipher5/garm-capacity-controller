package garmapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	ErrManagement         = errors.New("Garm management unavailable")
	ErrManagementDisabled = errors.New("Garm writes are disabled")
	ErrManagementConflict = errors.New("Garm settings changed; refresh before editing")
	ErrManagementUnsafe   = errors.New("unsupported or unsafe Garm settings change")
	// ErrManagementOutcomeUnknown means a write may have committed. Never retry it
	// automatically or report that it failed without first reading authoritative state.
	ErrManagementOutcomeUnknown = errors.New("Garm write outcome unknown; refresh and reconcile before another write")
)

// ManagementConfig is operator-only configuration, never a browser/job payload.
// Writes default off. v0.2.1 has NO atomic conditional updates: enabling writes
// requires external single-writer coordination, including the Garm UI and CLI.
// Pool and repository UUIDs are exact allowlists, independent of ledger pool IDs.
type ManagementConfig struct {
	Endpoint      string
	Token         string
	Trust         *tls.Config
	Providers     []string
	PoolIDs       []string
	RepositoryIDs []string
	AllowWrites   bool
}

type ManagementClient struct {
	client       *Client
	base         string
	pools        map[string]bool
	repositories map[string]bool
	writes       bool
	mu           sync.Mutex
}

// ManagedPool deliberately omits provider specs, instance/job payloads and secrets.
type ManagedPool struct {
	ID             string   `json:"id"`
	ProviderName   string   `json:"provider_name"`
	RepositoryID   string   `json:"repo_id,omitempty"`
	Tags           []string `json:"tags"`
	Priority       uint64   `json:"priority"`
	MaxRunners     uint64   `json:"max_runners"`
	MinIdleRunners uint64   `json:"min_idle_runners"`
	Enabled        bool     `json:"enabled"`
	Generation     uint64   `json:"generation"`
	Fingerprint    string   `json:"fingerprint"`
	protected      string
}

type ManagedRepository struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Balancer    string `json:"pool_balancing_type"`
	Fingerprint string `json:"fingerprint"`
	protected   string
}

// PoolUpdate is an explicit allowlist, not an arbitrary upstream JSON proxy.
// Tags affect future runners only. No image/flavor/specs/OS/template/shell/group
// updates are offered, because those can trigger runner-generation retirement.
// Decreasing min idle is blocked pending assignment-safe retirement qualification.
type PoolUpdate struct {
	Tags           *[]string `json:"tags,omitempty"`
	Priority       *uint64   `json:"priority,omitempty"`
	MaxRunners     *uint64   `json:"max_runners,omitempty"`
	MinIdleRunners *uint64   `json:"min_idle_runners,omitempty"`
	Enabled        *bool     `json:"enabled,omitempty"`
}

func managementID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && strings.ToLower(id) == id
}

func NewManagement(config ManagementConfig) (*ManagementClient, error) {
	if len(config.PoolIDs)+len(config.RepositoryIDs) == 0 || len(config.PoolIDs)+len(config.RepositoryIDs) > 1000 {
		return nil, ErrManagement
	}
	c, err := New(config.Endpoint, config.Token, config.Trust, config.Providers, nil)
	if err != nil {
		return nil, ErrManagement
	}
	m := &ManagementClient{client: c, base: strings.TrimSuffix(c.endpoint, "/instances"), pools: map[string]bool{}, repositories: map[string]bool{}, writes: config.AllowWrites}
	for _, pair := range []struct {
		ids   []string
		scope map[string]bool
	}{{config.PoolIDs, m.pools}, {config.RepositoryIDs, m.repositories}} {
		for _, id := range pair.ids {
			if !managementID(id) || pair.scope[id] {
				c.Close()
				return nil, ErrManagement
			}
			pair.scope[id] = true
		}
	}
	return m, nil
}
func (m *ManagementClient) Close() { m.client.Close() }

// exchange never redirects, uses ambient proxies, retries writes or returns raw
// server/network error details. Even a non-200 write may follow a partial commit.
func (m *ManagementClient) exchange(ctx context.Context, method, path string, value any) (map[string]json.RawMessage, error) {
	var body io.Reader
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, ErrManagementUnsafe
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, body)
	if err != nil {
		return nil, ErrManagement
	}
	req.Header.Set("Authorization", "Bearer "+m.client.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := m.client.http.Do(req)
	failure := ErrManagement
	if method != http.MethodGet {
		failure = ErrManagementOutcomeUnknown
	}
	if err != nil {
		return nil, failure
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, failure
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, failure
	}
	fields, err := row(data)
	if err != nil {
		return nil, failure
	}
	return fields, nil
}

// fingerprint detects stale edits before dispatch, not an upstream CAS. Exclude
// runtime children, retain all other settings (including fields not displayed).
func managementFingerprint(fields map[string]json.RawMessage) string {
	settings := map[string]json.RawMessage{}
	for k, v := range fields {
		if k != "instances" && k != "pool" && k != "events" {
			settings[k] = v
		}
	}
	encoded, _ := json.Marshal(settings)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func protectedFingerprint(fields map[string]json.RawMessage, excluded ...string) string {
	settings := map[string]json.RawMessage{}
	skip := map[string]bool{"updated_at": true}
	for _, key := range excluded {
		skip[key] = true
	}
	for key, value := range fields {
		if !skip[key] {
			settings[key] = value
		}
	}
	return managementFingerprint(settings)
}
func number(fields map[string]json.RawMessage, key string, required bool) (uint64, error) {
	raw, ok := fields[key]
	if !ok && !required {
		return 0, nil
	}
	var n uint64
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &n) != nil {
		return 0, ErrManagement
	}
	return n, nil
}
func (m *ManagementClient) pool(ctx context.Context, id string) (ManagedPool, error) {
	if !m.pools[id] {
		return ManagedPool{}, ErrManagementUnsafe
	}
	f, err := m.exchange(ctx, http.MethodGet, "/pools/"+url.PathEscape(id), nil)
	if err != nil {
		return ManagedPool{}, err
	}
	p, err := m.decodePool(f, id)
	return p, err
}
func boundedSettingText(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (m *ManagementClient) decodePool(f map[string]json.RawMessage, id string) (ManagedPool, error) {
	p := ManagedPool{Tags: []string{}}
	var err error
	p.ID, err = textField(f, "id", true)
	if err != nil || p.ID != id {
		return ManagedPool{}, ErrManagement
	}
	p.ProviderName, err = textField(f, "provider_name", true)
	if err != nil || !m.client.providers[p.ProviderName] || !boundedSettingText(p.ProviderName, 256) {
		return ManagedPool{}, ErrManagement
	}
	p.RepositoryID, err = textField(f, "repo_id", false)
	if err != nil || (p.RepositoryID != "" && !managementID(p.RepositoryID)) {
		return ManagedPool{}, ErrManagement
	}
	p.Generation, err = number(f, "generation", true)
	if err != nil {
		return ManagedPool{}, ErrManagement
	}
	for key, target := range map[string]*uint64{"priority": &p.Priority, "max_runners": &p.MaxRunners, "min_idle_runners": &p.MinIdleRunners} {
		*target, err = number(f, key, false)
		if err != nil {
			return ManagedPool{}, ErrManagement
		}
	}
	if p.MinIdleRunners > p.MaxRunners || p.MaxRunners > 1000000 || p.Priority > 1000000 || p.Generation > 9007199254740991 {
		return ManagedPool{}, ErrManagement
	}
	if raw, ok := f["enabled"]; ok {
		if string(raw) == "null" || json.Unmarshal(raw, &p.Enabled) != nil {
			return ManagedPool{}, ErrManagement
		}
	}
	if raw, ok := f["tags"]; ok {
		var tags []json.RawMessage
		if string(raw) == "null" || json.Unmarshal(raw, &tags) != nil || len(tags) > 100 {
			return ManagedPool{}, ErrManagement
		}
		seen := map[string]bool{}
		for _, tag := range tags {
			tf, e := row(tag)
			if e != nil {
				return ManagedPool{}, ErrManagement
			}
			name, e := textField(tf, "name", true)
			if e != nil || seen[name] || !boundedSettingText(name, 256) {
				return ManagedPool{}, ErrManagement
			}
			seen[name] = true
			p.Tags = append(p.Tags, name)
		}
	}
	sort.Strings(p.Tags)
	p.Fingerprint = managementFingerprint(f)
	p.protected = protectedFingerprint(f, "tags", "priority", "max_runners", "min_idle_runners", "enabled")
	return p, nil
}
func (m *ManagementClient) repository(ctx context.Context, id string) (ManagedRepository, error) {
	if !m.repositories[id] {
		return ManagedRepository{}, ErrManagementUnsafe
	}
	f, err := m.exchange(ctx, http.MethodGet, "/repositories/"+url.PathEscape(id), nil)
	if err != nil {
		return ManagedRepository{}, err
	}
	r := ManagedRepository{}
	r.ID, err = textField(f, "id", true)
	if err != nil || r.ID != id {
		return ManagedRepository{}, ErrManagement
	}
	r.Owner, err = textField(f, "owner", true)
	if err != nil || !boundedSettingText(r.Owner, 256) {
		return ManagedRepository{}, ErrManagement
	}
	r.Name, err = textField(f, "name", true)
	if err != nil || !boundedSettingText(r.Name, 256) {
		return ManagedRepository{}, ErrManagement
	}
	r.Balancer, err = textField(f, "pool_balancing_type", false)
	if err != nil || !balancer(r.Balancer) {
		return ManagedRepository{}, ErrManagement
	}
	r.Fingerprint = managementFingerprint(f)
	r.protected = protectedFingerprint(f, "pool_balancing_type")
	return r, nil
}
func balancer(value string) bool { return value == "" || value == "roundrobin" || value == "pack" }

func (m *ManagementClient) Pool(ctx context.Context, id string) (ManagedPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pool(ctx, id)
}
func (m *ManagementClient) Repository(ctx context.Context, id string) (ManagedRepository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.repository(ctx, id)
}

func (m *ManagementClient) UpdatePool(ctx context.Context, id, expected string, update PoolUpdate) (ManagedPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.writes {
		return ManagedPool{}, ErrManagementDisabled
	}
	before, err := m.pool(ctx, id)
	if err != nil {
		return ManagedPool{}, err
	}
	if expected == "" || expected != before.Fingerprint {
		return ManagedPool{}, ErrManagementConflict
	}
	if update.Tags == nil && update.Priority == nil && update.MaxRunners == nil && update.MinIdleRunners == nil && update.Enabled == nil {
		return ManagedPool{}, ErrManagementUnsafe
	}
	maximum, minimum := before.MaxRunners, before.MinIdleRunners
	if update.MaxRunners != nil {
		maximum = *update.MaxRunners
	}
	if update.MinIdleRunners != nil {
		minimum = *update.MinIdleRunners
	}
	if maximum == 0 || maximum > 1000000 || minimum > maximum || minimum < before.MinIdleRunners {
		return ManagedPool{}, ErrManagementUnsafe
	}
	if update.Priority != nil && *update.Priority > 1000000 {
		return ManagedPool{}, ErrManagementUnsafe
	}
	if update.Tags != nil {
		tags := append([]string{}, (*update.Tags)...)
		if len(tags) == 0 || len(tags) > 100 {
			return ManagedPool{}, ErrManagementUnsafe
		}
		seen := map[string]bool{}
		for _, tag := range tags {
			if strings.TrimSpace(tag) != tag || tag == "" || !boundedSettingText(tag, 256) || seen[tag] {
				return ManagedPool{}, ErrManagementUnsafe
			}
			seen[tag] = true
		}
		sort.Strings(tags)
		update.Tags = &tags
	}
	if _, err = m.exchange(ctx, http.MethodPut, "/pools/"+url.PathEscape(id), update); err != nil {
		return ManagedPool{}, err
	}
	after, err := m.pool(ctx, id)
	if err != nil {
		return ManagedPool{}, ErrManagementOutcomeUnknown
	}
	wanted := before
	wanted.MaxRunners = maximum
	wanted.MinIdleRunners = minimum
	if update.Tags != nil {
		wanted.Tags = *update.Tags
	}
	if update.Priority != nil {
		wanted.Priority = *update.Priority
	}
	if update.Enabled != nil {
		wanted.Enabled = *update.Enabled
	}
	fingerprint := after.Fingerprint
	after.Fingerprint = ""
	wanted.Fingerprint = ""
	a, _ := json.Marshal(after)
	b, _ := json.Marshal(wanted)
	if !bytes.Equal(a, b) || after.protected != before.protected {
		return ManagedPool{}, ErrManagementOutcomeUnknown
	}
	// Return the authoritative read's fingerprint, not a synthesized version.
	after.Fingerprint = fingerprint
	return after, nil
}

func (m *ManagementClient) UpdateRepositoryBalancer(ctx context.Context, id, expected, value string) (ManagedRepository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.writes {
		return ManagedRepository{}, ErrManagementDisabled
	}
	if value != "roundrobin" && value != "pack" {
		return ManagedRepository{}, ErrManagementUnsafe
	}
	before, err := m.repository(ctx, id)
	if err != nil {
		return ManagedRepository{}, err
	}
	if expected == "" || expected != before.Fingerprint {
		return ManagedRepository{}, ErrManagementConflict
	}
	if _, err = m.exchange(ctx, http.MethodPut, "/repositories/"+url.PathEscape(id), struct {
		Balancer string `json:"pool_balancer_type"`
	}{value}); err != nil {
		return ManagedRepository{}, err
	}
	after, err := m.repository(ctx, id)
	if err != nil || after.ID != before.ID || after.Owner != before.Owner || after.Name != before.Name || after.Balancer != value || after.protected != before.protected {
		return ManagedRepository{}, ErrManagementOutcomeUnknown
	}
	return after, nil
}

// ManagementSnapshot reads only exact configured IDs, not an unscoped collection.
// A failure returns no partial snapshot. All fingerprints are refresh preconditions,
// never an atomic upstream revision.
type ManagementSnapshot struct {
	Pools         []ManagedPool       `json:"pools"`
	Repositories  []ManagedRepository `json:"repositories"`
	WritesEnabled bool                `json:"writes_enabled"`
}

func (m *ManagementClient) Snapshot(ctx context.Context) (ManagementSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	payloadBytes := 0
	snapshot := ManagementSnapshot{Pools: []ManagedPool{}, Repositories: []ManagedRepository{}, WritesEnabled: m.writes}
	ids := []string{}
	for id := range m.pools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		pool, err := m.pool(ctx, id)
		if err != nil {
			return ManagementSnapshot{}, err
		}
		encoded, _ := json.Marshal(pool)
		payloadBytes += len(encoded)
		if payloadBytes > 1<<20 {
			return ManagementSnapshot{}, ErrManagement
		}
		snapshot.Pools = append(snapshot.Pools, pool)
	}
	ids = []string{}
	for id := range m.repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		repository, err := m.repository(ctx, id)
		if err != nil {
			return ManagementSnapshot{}, err
		}
		encoded, _ := json.Marshal(repository)
		payloadBytes += len(encoded)
		if payloadBytes > 1<<20 {
			return ManagementSnapshot{}, ErrManagement
		}
		snapshot.Repositories = append(snapshot.Repositories, repository)
	}
	return snapshot, nil
}
