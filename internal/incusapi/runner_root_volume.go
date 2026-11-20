package incusapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// RunnerRootVolumeExpected is the exact existing stock runner independently
// selected by the caller (Incus Name is the stock Incus provider ID). The
// client project remains fixed by its existing startup configuration.
type RunnerRootVolumeExpected struct {
	Name         string
	ControllerID string
	PoolID       string
}

// RunnerRootVolume projects only the effective named root disk of an existing
// container. Location is optional metadata, not a verified cluster member.
// This does not identify a physical mount, storage driver, loaded GARM route
// or future create destination, and supplies no capacity or write authority.
type RunnerRootVolume struct {
	Instance    string
	Project     string
	Location    string
	StoragePool string
}

func (e RunnerRootVolumeExpected) valid() bool {
	const nilUUID = "00000000-0000-0000-0000-000000000000"
	return name(e.Name) && operationID(e.ControllerID) && e.ControllerID != nilUUID &&
		operationID(e.PoolID) && e.PoolID != nilUUID
}

// RunnerRootVolume observes exactly one independently named, already existing
// stock Incus runner on this authenticated standalone client. A caller must
// already have installed the independently reviewed immutable physical identity
// pin. This operation never discovers or pins a peer on the caller's behalf.
// No state, storage contents, arbitrary paths, or other instances are read.
func (c *Client) RunnerRootVolume(ctx context.Context, expected RunnerRootVolumeExpected) (RunnerRootVolume, error) {
	var none RunnerRootVolume
	if c == nil || ctx == nil || !name(c.project) || !expected.valid() || c.pin == nil {
		return none, ErrConfiguration
	}
	pinned := c.pin.observed.Load()
	if pinned == nil || !pinned.Valid() {
		return none, ErrConfiguration
	}
	// This existing guard confirms fresh topology and the full root
	// identity against the transport's immutable startup pin.
	if err := c.trusted(ctx, false); err != nil {
		return none, err
	}
	out, err := c.request(ctx, http.MethodGet, "/1.0/instances/"+url.PathEscape(expected.Name), nil, nil)
	if err != nil {
		return none, err
	}
	if out.kind != "sync" || out.serverKeyFingerprint != pinned.ServerKeyFingerprint() || ctx.Err() != nil {
		return none, ErrResponse
	}
	result, err := parseRunnerRootVolume(out.metadata, c.project, expected)
	if err != nil || ctx.Err() != nil {
		return none, ErrResponse
	}
	return result, nil
}

const (
	stockRunnerControllerKey = "user.runner-controller-id"
	stockRunnerPoolKey       = "user.runner-pool-id"
)

// Keep compatible unknown Incus fields, without allowing aliases to supply or
// shadow critical keys. Exact duplicates are already refused by object.
func runnerRootVolumeObject(raw []byte, critical ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, ErrResponse
	}
	fields, err := object(raw)
	if err != nil {
		return nil, err
	}
	for key := range fields {
		for _, expected := range critical {
			if key != expected && strings.EqualFold(key, expected) {
				return nil, ErrResponse
			}
		}
	}
	return fields, nil
}

func parseRunnerRootVolume(raw []byte, project string, expected RunnerRootVolumeExpected) (RunnerRootVolume, error) {
	var none RunnerRootVolume
	fields, err := runnerRootVolumeObject(raw, "name", "project", "type", "location", "config", "expanded_config", "expanded_devices")
	if err != nil {
		return none, err
	}
	instance, a := field[string](fields, "name")
	effectiveProject, b := field[string](fields, "project")
	kind, c := field[string](fields, "type")
	if a != nil || b != nil || c != nil || instance != expected.Name || effectiveProject != project || kind != "container" {
		return none, ErrResponse
	}
	// The stock provider sets these tags at instance creation, not just
	// through a named profile. Require direct and effective values to agree.
	for _, source := range [...]string{"config", "expanded_config"} {
		config, err := runnerRootVolumeObject(fields[source], stockRunnerControllerKey, stockRunnerPoolKey)
		if err != nil {
			return none, err
		}
		controller, a := field[string](config, stockRunnerControllerKey)
		pool, b := field[string](config, stockRunnerPoolKey)
		if a != nil || b != nil || controller != expected.ControllerID || pool != expected.PoolID {
			return none, ErrResponse
		}
	}

	location := ""
	if _, present := fields["location"]; present {
		location, err = field[string](fields, "location")
		if err != nil || !runnerRootVolumeLocation(location) {
			return none, ErrResponse
		}
	}
	pool, err := runnerRootStoragePool(fields["expanded_devices"])
	if err != nil {
		return none, err
	}
	return RunnerRootVolume{Instance: instance, Project: effectiveProject, Location: location, StoragePool: pool}, nil
}

func runnerRootVolumeLocation(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 253 || s[0] == '.' || s[len(s)-1] == '.' || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func runnerRootStoragePool(raw []byte) (string, error) {
	devices, err := runnerRootVolumeObject(raw)
	if err != nil || len(devices) == 0 || len(devices) > 64 {
		return "", ErrResponse
	}
	pool := ""
	for _, raw := range devices {
		device, err := runnerRootVolumeObject(raw, "path", "type", "pool", "source")
		if err != nil {
			return "", err
		}
		if _, ok := device["path"]; !ok {
			continue
		}
		destination, err := field[string](device, "path")
		if err != nil {
			return "", ErrResponse
		}
		if destination != "/" {
			if strings.HasPrefix(destination, "/") && path.Clean(destination) == "/" {
				return "", ErrResponse
			}
			continue
		}
		kind, a := field[string](device, "type")
		namedPool, b := field[string](device, "pool")
		if a != nil || b != nil || kind != "disk" || !name(namedPool) || pool != "" {
			return "", ErrResponse
		}
		if _, hasSource := device["source"]; hasSource {
			value, err := field[string](device, "source")
			if err != nil || value != "" {
				return "", ErrResponse
			}
		}
		pool = namedPool
	}
	if pool == "" {
		return "", ErrResponse
	}
	return pool, nil
}
