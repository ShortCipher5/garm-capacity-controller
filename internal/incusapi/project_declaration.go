package incusapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// incus702DeclarationReview contains expectations supplied independently of the
// two GET response bodies. It says nothing about who supplied those expectations.
type incus702DeclarationReview struct {
	project, profile, pool       string
	projectSHA256, profileSHA256 string
}

// Only configured declarations. These are not usage, headroom, live enforcement,
// a GARM route, daemon/build identity, or permission to launch/reuse capacity.
type incus702ProjectDeclaration struct {
	instances, containers, cpu, memoryBytes, diskBytes int64
	profileCPU, profileMemoryBytes, profileRootBytes   int64
}

// compareIncus702ProjectDeclaration is deliberately unwired. It accepts only
// bounded Incus v7.0.2 single-project and single-profile sync GET bodies, not
// discovery/usage lists or later versions. A future trusted caller must establish
// the build, authenticated source, freshness, independent review pins, and
// exact GARM project/profile/overrides; successful comparison cannot establish any.
func compareIncus702ProjectDeclaration(projectRaw, profileRaw []byte, reviewed incus702DeclarationReview) (incus702ProjectDeclaration, error) {
	var none incus702ProjectDeclaration
	if !name(reviewed.project) || strings.EqualFold(reviewed.project, "default") || !name(reviewed.profile) ||
		!name(reviewed.pool) || !fingerprint(reviewed.projectSHA256) || !fingerprint(reviewed.profileSHA256) {
		return none, ErrConfiguration
	}
	project, err := incus702DeclarationMetadata(projectRaw, "name", "config", "description", "used_by")
	if err != nil {
		return none, err
	}
	profile, err := incus702DeclarationMetadata(profileRaw, "name", "project", "config", "devices", "description", "used_by")
	if err != nil {
		return none, err
	}
	projectName, a := field[string](project, "name")
	profileName, b := field[string](profile, "name")
	profileOwner, c := field[string](profile, "project")
	if a != nil || b != nil || c != nil || projectName != reviewed.project || profileName != reviewed.profile || profileOwner != reviewed.project {
		return none, ErrResponse
	}
	projectConfig, a := incus702DeclarationStringMap(project["config"], 128)
	profileConfig, b := incus702DeclarationStringMap(profile["config"], 128)
	devices, c := incus702DeclarationDevices(profile["devices"], reviewed.pool)
	if a != nil || b != nil || c != nil || projectConfig["features.profiles"] != "true" || projectConfig["restricted"] != "true" ||
		projectConfig["limits.virtual-machines"] != "0" || profileConfig["limits.memory.enforce"] != "hard" {
		return none, ErrResponse
	}
	for key := range projectConfig {
		// This variant does not interpret parallel pool-specific disk limits.
		if strings.HasPrefix(key, "limits.disk.pool.") {
			return none, ErrResponse
		}
	}
	// Hash exact decoded maps (no defaults, filtered subset, or unit rewriting).
	// Include the owning names and use separate, versioned hash domains.
	projectHash := incus702DeclarationHash("incus-project-config/v7.0.2", struct {
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}{projectName, projectConfig})
	profileHash := incus702DeclarationHash("incus-profile-config/v7.0.2", struct {
		Project string                       `json:"project"`
		Name    string                       `json:"name"`
		Config  map[string]string            `json:"config"`
		Devices map[string]map[string]string `json:"devices"`
	}{profileOwner, profileName, profileConfig, devices})
	if projectHash != reviewed.projectSHA256 || profileHash != reviewed.profileSHA256 {
		return none, ErrResponse
	}
	out := incus702ProjectDeclaration{
		instances:          incus702PositiveDecimal(projectConfig["limits.instances"], 32),
		containers:         incus702PositiveDecimal(projectConfig["limits.containers"], 32),
		cpu:                incus702PositiveDecimal(projectConfig["limits.cpu"], 32),
		memoryBytes:        incus702DeclarationBytes(projectConfig["limits.memory"]),
		diskBytes:          incus702DeclarationBytes(projectConfig["limits.disk"]),
		profileCPU:         incus702PositiveDecimal(profileConfig["limits.cpu"], 32),
		profileMemoryBytes: incus702DeclarationBytes(profileConfig["limits.memory"]),
		profileRootBytes:   incus702DeclarationBytes(devices["root"]["size"]),
	}
	if out.instances == 0 || out.containers == 0 || out.cpu == 0 || out.memoryBytes == 0 || out.diskBytes == 0 ||
		out.profileCPU == 0 || out.profileMemoryBytes == 0 || out.profileRootBytes == 0 ||
		out.profileCPU > out.cpu || out.profileMemoryBytes > out.memoryBytes || out.profileRootBytes > out.diskBytes {
		return none, ErrResponse
	}
	return out, nil
}

func incus702DeclarationMetadata(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	metadata, err := incus702SyncMetadata(raw, names...)
	if err != nil {
		return nil, err
	}
	description, a := field[string](metadata, "description")
	used, b := field[[]string](metadata, "used_by")
	if a != nil || b != nil || !incus702DeclarationText(description, 4096) || len(used) > 1024 {
		return nil, ErrResponse
	}
	// Informational links are never resolved, hashed as config, or exposed.
	for _, entry := range used {
		if !strings.HasPrefix(entry, "/1.0/") || !incus702DeclarationText(entry, 1024) {
			return nil, ErrResponse
		}
	}
	return metadata, nil
}

func incus702SyncMetadata(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	const max = 64 << 10
	if len(raw) == 0 || len(raw) > max || !utf8.Valid(raw) {
		return nil, ErrResponse
	}
	response, err := incus702DeclarationObject(raw, "type", "status", "status_code", "operation", "error_code", "error", "metadata")
	if err != nil {
		return nil, err
	}
	typ, a := field[string](response, "type")
	status, b := field[string](response, "status")
	code, c := field[int](response, "status_code")
	op, d := field[string](response, "operation")
	errorCode, e := field[int](response, "error_code")
	errorText, f := field[string](response, "error")
	if a != nil || b != nil || c != nil || d != nil || e != nil || f != nil ||
		typ != "sync" || status != "Success" || code != 200 || op != "" || errorCode != 0 || errorText != "" {
		return nil, ErrResponse
	}
	return incus702DeclarationObject(response["metadata"], names...)
}

func incus702DeclarationObject(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	fields, err := object(raw)
	if err != nil || len(fields) != len(names) {
		return nil, ErrResponse
	}
	for _, key := range names {
		if _, present := fields[key]; !present {
			return nil, ErrResponse
		}
	}
	return fields, nil
}

func incus702DeclarationText(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for _, r := range s {
		// No malformed surrogate replacement or control content in the reviewed subset.
		if r == utf8.RuneError || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func incus702DeclarationStringMap(raw []byte, max int) (map[string]string, error) {
	fields, err := object(raw)
	if err != nil || len(fields) > max {
		return nil, ErrResponse
	}
	out := make(map[string]string, len(fields))
	for key := range fields {
		if key == "" || !incus702DeclarationText(key, 128) {
			return nil, ErrResponse
		}
		v, err := field[string](fields, key)
		if err != nil || !incus702DeclarationText(v, 4096) {
			return nil, ErrResponse
		}
		out[key] = v
	}
	return out, nil
}

func incus702DeclarationDevices(raw []byte, pool string) (map[string]map[string]string, error) {
	fields, err := object(raw)
	if err != nil || len(fields) == 0 || len(fields) > 8 {
		return nil, ErrResponse
	}
	out := make(map[string]map[string]string, len(fields))
	for key, value := range fields {
		config, err := incus702DeclarationStringMap(value, 64)
		if !name(key) || err != nil {
			return nil, ErrResponse
		}
		if key == "root" {
			if config["type"] != "disk" || config["path"] != "/" || config["pool"] != pool || config["source"] != "" {
				return nil, ErrResponse
			}
		} else if config["type"] != "nic" {
			return nil, ErrResponse
		}
		out[key] = config
	}
	if out["root"] == nil {
		return nil, ErrResponse
	}
	return out, nil
}

func incus702DeclarationHash(domain string, value any) string {
	// Only typed structs containing string maps above are used. Marshal sorts map keys.
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(domain + "\x00"))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// A positive canonical decimal, no aliases, CPU pin sets, signs or leading zeroes.
func incus702PositiveDecimal(s string, bits int) int64 {
	n, err := strconv.ParseUint(s, 10, bits)
	if err != nil || n == 0 || n > math.MaxInt64 || strconv.FormatUint(n, 10) != s {
		return 0
	}
	return int64(n)
}

// Conservative subset of upstream ParseByteSizeString: integral raw bytes/B
// and binary KiB–TiB only. Other valid Incus spellings are not inferred.
func incus702DeclarationBytes(s string) int64 {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	var shift uint
	switch s[i:] {
	case "", "B":
	case "KiB":
		shift = 10
	case "MiB":
		shift = 20
	case "GiB":
		shift = 30
	case "TiB":
		shift = 40
	default:
		return 0
	}
	n := incus702PositiveDecimal(s[:i], 63)
	if n > math.MaxInt64>>shift {
		return 0
	}
	return n << shift
}
