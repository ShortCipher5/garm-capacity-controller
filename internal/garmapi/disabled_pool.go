package garmapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// DisabledPoolSpec is trusted server configuration, never arbitrary browser/job
// JSON. This experimental transport requires the separately qualified source8 native
// template-zero protocol. An opt-in is intent, not proof of installed support.
// It cannot activate a pool or assert a durable admission route exists.
type DisabledPoolSpec struct {
	ProviderName string   `json:"provider_name"`
	MaxRunners   uint64   `json:"max_runners"`
	Image        string   `json:"image"`
	Flavor       string   `json:"flavor"`
	Arch         string   `json:"os_arch"`
	Tags         []string `json:"tags"`
}

func CanonicalDisabledPoolSpec(spec DisabledPoolSpec) (DisabledPoolSpec, error) {
	if !boundedSettingText(spec.ProviderName, 256) || !boundedSettingText(spec.Image, 256) || !boundedSettingText(spec.Flavor, 256) || spec.MaxRunners < 1 || spec.MaxRunners > 1000000 || (spec.Arch != "amd64" && spec.Arch != "arm64") || len(spec.Tags) < 1 || len(spec.Tags) > 100 {
		return DisabledPoolSpec{}, ErrManagementUnsafe
	}
	spec.Tags = append([]string(nil), spec.Tags...)
	sort.Strings(spec.Tags)
	for i, tag := range spec.Tags {
		if !boundedSettingText(tag, 256) || (i > 0 && tag == spec.Tags[i-1]) {
			return DisabledPoolSpec{}, ErrManagementUnsafe
		}
	}
	return spec, nil
}
func DisabledPoolSpecFingerprint(spec DisabledPoolSpec) string {
	canonical, err := CanonicalDisabledPoolSpec(spec)
	if err != nil {
		return ""
	}
	raw, _ := json.Marshal(struct {
		Protocol string           `json:"protocol"`
		Spec     DisabledPoolSpec `json:"spec"`
	}{"native-disabled-repository-pool-v1", canonical})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// DisabledPoolReceipt is an opaque, client-bound POST acknowledgement. A UUID
// alone is not a verified disabled pool, durable route, or activation permit.
// The journal owner must save the UUID BEFORE requesting readback. A lost ACK
// cannot be reconstructed by matching rows or automatically repeating the POST.
type DisabledPoolReceipt struct {
	client                *ManagementClient
	id, repo, fingerprint string
	postValidated         bool
}

func (r DisabledPoolReceipt) PoolID() string          { return r.id }
func (r DisabledPoolReceipt) RepositoryID() string    { return r.repo }
func (r DisabledPoolReceipt) SpecFingerprint() string { return r.fingerprint }

func (m *ManagementClient) DisabledPoolCreationEnabled() bool {
	return m.writes && m.disabledPoolCreation
}

// PostDisabledRepoPool is deliberately separate from readback so the caller can
// durably capture a returned UUID first. It does not add it to writable scopes.
func (m *ManagementClient) PostDisabledRepoPool(ctx context.Context, repo, expected string, spec DisabledPoolSpec) (DisabledPoolReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.writes || !m.disabledPoolCreation {
		return DisabledPoolReceipt{}, ErrManagementDisabled
	}
	canonical, err := CanonicalDisabledPoolSpec(spec)
	if err != nil || !m.repositories[repo] || !m.client.providers[spec.ProviderName] {
		return DisabledPoolReceipt{}, ErrManagementUnsafe
	}
	before, err := m.repository(ctx, repo)
	if err != nil {
		return DisabledPoolReceipt{}, err
	}
	if before.Fingerprint != expected {
		return DisabledPoolReceipt{}, ErrManagementConflict
	}
	body := struct {
		DisabledPoolSpec
		MinIdleRunners uint64          `json:"min_idle_runners"`
		Enabled        bool            `json:"enabled"`
		OSType         string          `json:"os_type"`
		ExtraSpecs     json.RawMessage `json:"extra_specs"`
		EnableShell    bool            `json:"enable_shell"`
		TemplateID     uint64          `json:"template_id"`
	}{canonical, 0, false, "linux", json.RawMessage(`{}`), false, 0}
	fields, err := m.exchange(ctx, http.MethodPost, "/repositories/"+url.PathEscape(repo)+"/pools", body)
	if err != nil {
		return DisabledPoolReceipt{}, ErrManagementOutcomeUnknown
	}
	id, err := textField(fields, "id", true)
	if err != nil || !managementID(id) {
		return DisabledPoolReceipt{}, ErrManagementOutcomeUnknown
	}
	receipt := DisabledPoolReceipt{client: m, id: id, repo: repo, fingerprint: DisabledPoolSpecFingerprint(canonical)}
	if _, err = m.verifyDisabledPool(fields, receipt, canonical); err != nil {
		return receipt, ErrManagementOutcomeUnknown
	}
	receipt.postValidated = true
	return receipt, nil
}
func (m *ManagementClient) verifyDisabledPool(fields map[string]json.RawMessage, receipt DisabledPoolReceipt, spec DisabledPoolSpec) (ManagedPool, error) {
	for name := range fields {
		for _, exact := range []string{"id", "provider_name", "repo_id", "org_id", "enterprise_id", "max_runners", "min_idle_runners", "enabled", "generation", "tags", "image", "flavor", "os_type", "os_arch", "template_id", "enable_shell", "extra_specs", "instances"} {
			if strings.EqualFold(name, exact) && name != exact {
				return ManagedPool{}, ErrManagementOutcomeUnknown
			}
		}
	}
	if raw, ok := fields["instances"]; ok {
		var children []json.RawMessage
		if string(raw) == "null" || json.Unmarshal(raw, &children) != nil || len(children) != 0 {
			return ManagedPool{}, ErrManagementOutcomeUnknown
		}
	}
	pool, err := m.decodePool(fields, receipt.id)
	if err != nil || pool.RepositoryID != receipt.repo || pool.ProviderName != spec.ProviderName || pool.Enabled || pool.MinIdleRunners != 0 || pool.MaxRunners != spec.MaxRunners || len(pool.Tags) != len(spec.Tags) {
		return ManagedPool{}, ErrManagementOutcomeUnknown
	}
	for i, tag := range pool.Tags {
		if tag != spec.Tags[i] {
			return ManagedPool{}, ErrManagementOutcomeUnknown
		}
	}
	for name, expected := range map[string]string{"image": spec.Image, "flavor": spec.Flavor, "os_type": "linux", "os_arch": spec.Arch} {
		actual, e := textField(fields, name, true)
		if e != nil || actual != expected {
			return ManagedPool{}, ErrManagementOutcomeUnknown
		}
	}
	template, e := number(fields, "template_id", false)
	if e != nil || template != 0 {
		return ManagedPool{}, ErrManagementOutcomeUnknown
	}
	var shell bool
	raw, ok := fields["enable_shell"]
	if !ok || string(raw) == "null" || json.Unmarshal(raw, &shell) != nil || shell {
		return ManagedPool{}, ErrManagementOutcomeUnknown
	}
	if raw, ok := fields["extra_specs"]; ok {
		var extra map[string]json.RawMessage
		if string(raw) == "null" || json.Unmarshal(raw, &extra) != nil || len(extra) != 0 {
			return ManagedPool{}, ErrManagementOutcomeUnknown
		}
	}
	// New repository pools must not be associated with a different forge scope.
	for _, name := range []string{"org_id", "enterprise_id", "github-runner-group"} {
		if raw, ok := fields[name]; ok && string(raw) != `""` {
			return ManagedPool{}, ErrManagementOutcomeUnknown
		}
	}
	return pool, nil
}
func (m *ManagementClient) VerifyDisabledRepoPool(ctx context.Context, receipt DisabledPoolReceipt, spec DisabledPoolSpec) (ManagedPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	canonical, err := CanonicalDisabledPoolSpec(spec)
	if err != nil || receipt.client != m || !receipt.postValidated || receipt.fingerprint != DisabledPoolSpecFingerprint(canonical) || !m.repositories[receipt.repo] {
		return ManagedPool{}, ErrManagementUnsafe
	}
	fields, err := m.exchange(ctx, http.MethodGet, "/repositories/"+url.PathEscape(receipt.repo)+"/pools/"+url.PathEscape(receipt.id), nil)
	if err != nil {
		return ManagedPool{}, ErrManagementOutcomeUnknown
	}
	return m.verifyDisabledPool(fields, receipt, canonical)
}
