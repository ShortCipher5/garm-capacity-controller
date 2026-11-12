package garmapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
)

func preparationAliases(fields map[string]json.RawMessage, known []string) bool {
	for name := range fields {
		for _, key := range known {
			if strings.EqualFold(name, key) && name != key {
				return false
			}
		}
	}
	return true
}
func preparationUUID(value string) bool {
	if len(value) != 36 || value == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// PreparationPool reads actual authenticated pool routing before trusting source
// metadata. FieldsKnown means exact required metadata, NOT global visibility/CAS.
func (c *Client) PreparationPool(ctx context.Context, pool string) (gateway.PreparationPoolObservation, error) {
	if pool == "" || len(pool) > 256 {
		return gateway.PreparationPoolObservation{}, ErrInventory
	}
	endpoint := strings.TrimSuffix(c.endpoint, "/instances") + "/pools/" + url.PathEscape(pool)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return gateway.PreparationPoolObservation{}, ErrInventory
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return gateway.PreparationPoolObservation{}, ErrInventory
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return gateway.PreparationPoolObservation{}, ErrInventory
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || !utf8.Valid(data) {
		return gateway.PreparationPoolObservation{}, ErrInventory
	}
	fields, err := row(data)
	if err != nil || !preparationAliases(fields, []string{"id", "provider_name", "generation", "repo_id", "org_id", "enterprise_id", "endpoint", "template_id", "extra_specs", "enable_shell", "enabled", "image", "flavor", "os_type", "os_arch"}) {
		return gateway.PreparationPoolObservation{}, ErrInventory
	}
	result := gateway.PreparationPoolObservation{}
	result.PoolID, err = textField(fields, "id", true)
	if err != nil || result.PoolID != pool {
		return result, ErrInventory
	}
	result.ProviderName, err = textField(fields, "provider_name", true)
	if err != nil || !c.providers[result.ProviderName] {
		return result, ErrInventory
	}
	raw, ok := fields["generation"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result.Generation) != nil || result.Generation > math.MaxInt64 {
		return result, ErrInventory
	}
	found := 0
	for _, entity := range []struct{ field, kind string }{{"repo_id", "repository"}, {"org_id", "organization"}, {"enterprise_id", "enterprise"}} {
		id, err := textField(fields, entity.field, false)
		if err != nil {
			return result, ErrInventory
		}
		if id != "" {
			if !preparationUUID(id) {
				return result, ErrInventory
			}
			found++
			result.EntityID = id
			result.EntityType = entity.kind
		}
	}
	if found != 1 {
		return result, ErrInventory
	}
	endpointFields, err := row(fields["endpoint"])
	if err != nil || !preparationAliases(endpointFields, []string{"name"}) {
		return result, ErrInventory
	}
	result.ForgeEndpointName, err = textField(endpointFields, "name", true)
	if err != nil {
		return result, ErrInventory
	}
	for _, field := range []struct {
		key   string
		value *string
	}{{"image", &result.Image}, {"flavor", &result.Flavor}, {"os_type", &result.OSType}, {"os_arch", &result.OSArch}} {
		*field.value, err = textField(fields, field.key, true)
		if err != nil {
			return result, ErrInventory
		}
	}
	// Pin actual documented omitempty zero semantics, not an arbitrary null.
	if raw, ok := fields["template_id"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result.TemplateID) != nil {
			return result, ErrInventory
		}
	}
	if enabledRaw, ok := fields["enabled"]; ok {
		if bytes.Equal(bytes.TrimSpace(enabledRaw), []byte("null")) || json.Unmarshal(enabledRaw, &result.Enabled) != nil {
			return result, ErrInventory
		}
	}
	var enableShell bool
	shellRaw, shellKnown := fields["enable_shell"]
	if !shellKnown || bytes.Equal(bytes.TrimSpace(shellRaw), []byte("null")) || json.Unmarshal(shellRaw, &enableShell) != nil {
		return result, ErrInventory
	}
	result.ShellDisabled = !enableShell
	emptySpecs := true
	if raw, ok := fields["extra_specs"]; ok {
		specs, err := row(raw)
		emptySpecs = err == nil && len(specs) == 0
	}
	result.DefaultBootstrap = result.TemplateID == 0 && emptySpecs && result.ShellDisabled
	check := garmprotocol.PreparationRequest{Protocol: garmprotocol.PreparationVersion, Name: "metadata-validation", PoolID: pool, InstanceID: "11111111-1111-4111-8111-111111111111", Generation: result.Generation, ProviderName: result.ProviderName, EntityType: result.EntityType, EntityID: result.EntityID, ForgeEndpointName: result.ForgeEndpointName, JITEnabled: false}
	if check.Validate() != nil {
		return result, ErrInventory
	}
	result.FieldsKnown = true
	return result, nil
}
