package garmapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

// DiscoveredScaleSet uses the local GARM numeric ID, not a pool UUID or the
// separate forge scale_set_id. No route, physical host, capacity grant or edit
// authority is inferred. Fingerprint covers only these redacted display fields.
type DiscoveredScaleSet struct {
	// DesiredRunnerCount is the upstream requested target, not observed occupancy
	// or reserved capacity. Native v0.2.1 omits its zero value in JSON.
	DesiredRunnerCount   uint64   `json:"desired_runner_count"`
	ID                   uint64   `json:"id"`
	Name                 string   `json:"name"`
	ProviderName         string   `json:"provider_name"`
	OwnerKind            string   `json:"owner_kind"`
	OwnerID              string   `json:"owner_id,omitempty"`
	State                string   `json:"state"`
	Enabled              bool     `json:"enabled"`
	MaxRunners           uint64   `json:"max_runners"`
	MinIdleRunners       uint64   `json:"min_idle_runners"`
	Generation           uint64   `json:"generation"`
	Tags                 []string `json:"tags"`
	DiscoveryFingerprint string   `json:"discovery_fingerprint"`
}
type ScaleSetDiscovery struct {
	ScaleSets []DiscoveredScaleSet `json:"scalesets"`
}

func (c *PoolDiscoveryClient) DiscoverScaleSets(ctx context.Context) (ScaleSetDiscovery, error) {
	if c == nil || c.management == nil {
		return ScaleSetDiscovery{}, ErrManagement
	}
	return c.management.DiscoverScaleSets(ctx)
}

// DiscoverScaleSets explicitly reads the distinct v0.2.1 /scalesets collection.
// It is never called implicitly by DiscoverPools. Unsupported/unauthorized or
// malformed collections are unavailable, not an empty successful observation.
// Scope is constructor-fixed; reads never enroll IDs or mutate accounting.
func (m *ManagementClient) DiscoverScaleSets(ctx context.Context) (ScaleSetDiscovery, error) {
	if m == nil || ctx == nil || ctx.Err() != nil || m.discoveryGate == nil {
		return ScaleSetDiscovery{}, ErrManagement
	}
	select {
	case m.discoveryGate <- struct{}{}:
		defer func() { <-m.discoveryGate }()
	case <-ctx.Done():
		return ScaleSetDiscovery{}, ErrManagement
	}
	if ctx.Err() != nil {
		return ScaleSetDiscovery{}, ErrManagement
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/scalesets", nil)
	if err != nil {
		return ScaleSetDiscovery{}, ErrManagement
	}
	req.Header.Set("Authorization", "Bearer "+m.client.token)
	req.Header.Set("Accept", "application/json")
	res, err := m.client.http.Do(req)
	if err != nil {
		return ScaleSetDiscovery{}, ErrManagement
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ScaleSetDiscovery{}, ErrManagement
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 || !utf8.Valid(raw) || ctx.Err() != nil {
		return ScaleSetDiscovery{}, ErrManagement
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return ScaleSetDiscovery{}, ErrManagement
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || len(rows) > 10000 {
		return ScaleSetDiscovery{}, ErrManagement
	}
	out := ScaleSetDiscovery{ScaleSets: []DiscoveredScaleSet{}}
	seen := map[uint64]bool{}
	size := 0
	for _, rawRow := range rows {
		fields, err := row(rawRow)
		if err != nil {
			return ScaleSetDiscovery{}, ErrManagement
		}
		for key := range fields {
			for _, known := range []string{"id", "name", "provider_name", "repo_id", "org_id", "enterprise_id", "state", "enabled", "max_runners", "min_idle_runners", "generation", "tags", "desired_runner_count"} {
				if key != known && strings.EqualFold(key, known) {
					return ScaleSetDiscovery{}, ErrManagement
				}
			}
		}
		id, err := number(fields, "id", true)
		if err != nil || id == 0 || id > 9007199254740991 || seen[id] {
			return ScaleSetDiscovery{}, ErrManagement
		}
		seen[id] = true
		provider, err := textField(fields, "provider_name", true)
		if err != nil || !boundedSettingText(provider, 256) {
			return ScaleSetDiscovery{}, ErrManagement
		}
		if !m.client.providers[provider] {
			continue
		}
		item := DiscoveredScaleSet{ID: id, ProviderName: provider, Tags: []string{}}
		item.Name, err = textField(fields, "name", true)
		if err != nil || !boundedSettingText(item.Name, 256) {
			return ScaleSetDiscovery{}, ErrManagement
		}
		item.OwnerKind, item.OwnerID, err = discoveryPoolOwner(fields)
		if err != nil {
			return ScaleSetDiscovery{}, ErrManagement
		}
		item.State, err = textField(fields, "state", true)
		if err != nil {
			return ScaleSetDiscovery{}, ErrManagement
		}
		switch item.State {
		case "pending_create", "created", "error", "pending_delete", "pending_force_delete":
		default:
			return ScaleSetDiscovery{}, ErrManagement
		}
		for key, target := range map[string]*uint64{"max_runners": &item.MaxRunners, "min_idle_runners": &item.MinIdleRunners, "generation": &item.Generation} {
			*target, err = number(fields, key, key == "generation")
			if err != nil {
				return ScaleSetDiscovery{}, ErrManagement
			}
		}
		item.DesiredRunnerCount, err = number(fields, "desired_runner_count", false)
		if err != nil || item.DesiredRunnerCount > 9007199254740991 {
			return ScaleSetDiscovery{}, ErrManagement
		}
		// A reported target above max is still a valid observation, not a grant.
		if item.MinIdleRunners > item.MaxRunners || item.MaxRunners > 1000000 || item.Generation > 9007199254740991 {
			return ScaleSetDiscovery{}, ErrManagement
		}
		if raw, ok := fields["enabled"]; ok {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &item.Enabled) != nil {
				return ScaleSetDiscovery{}, ErrManagement
			}
		}
		if raw, ok := fields["tags"]; ok {
			var tags []json.RawMessage
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &tags) != nil || len(tags) > 100 {
				return ScaleSetDiscovery{}, ErrManagement
			}
			names := map[string]bool{}
			for _, rawTag := range tags {
				tag, err := row(rawTag)
				if err != nil {
					return ScaleSetDiscovery{}, ErrManagement
				}
				for key := range tag {
					if key != "name" && strings.EqualFold(key, "name") {
						return ScaleSetDiscovery{}, ErrManagement
					}
				}
				name, err := textField(tag, "name", true)
				if err != nil || !boundedSettingText(name, 256) || names[name] {
					return ScaleSetDiscovery{}, ErrManagement
				}
				names[name] = true
				item.Tags = append(item.Tags, name)
			}
		}
		sort.Strings(item.Tags)
		canonical, err := json.Marshal(item)
		if err != nil {
			return ScaleSetDiscovery{}, ErrManagement
		}
		digest := sha256.Sum256(canonical)
		item.DiscoveryFingerprint = hex.EncodeToString(digest[:])
		encoded, err := json.Marshal(item)
		if err != nil {
			return ScaleSetDiscovery{}, ErrManagement
		}
		size += len(encoded)
		if size > 1<<20 {
			return ScaleSetDiscovery{}, ErrManagement
		}
		out.ScaleSets = append(out.ScaleSets, item)
	}
	sort.Slice(out.ScaleSets, func(i, j int) bool { return out.ScaleSets[i].ID < out.ScaleSets[j].ID })
	if ctx.Err() != nil {
		return ScaleSetDiscovery{}, ErrManagement
	}
	return out, nil
}
