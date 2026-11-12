// Package supervisorlaunch compiles inert inputs for a future protected native
// launcher. It contains no executor, enrollment or activation authority.
package supervisorlaunch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const (
	MaxIntentBytes = 4096
	Protocol       = "garm-supervisor-intent/v1"
	Operation      = "single_native_birth_intent"
	Profile        = "linux-protected-init-v1"
)

var ErrIntent = errors.New("supervisor launch: refused inert intent")

// Claims are syntactically checked caller claims, NOT authenticated bindings,
// artifact measurements, open descriptors, verified job identity or enrollment.
// Even invented well-shaped hashes are accepted as claims. A future qualified
// broker must independently resolve and authorize every reference.
type Claims struct {
	Protocol           string `json:"protocol"`
	Operation          string `json:"operation"`
	Profile            string `json:"profile"`
	Scope              string `json:"scope"`
	Attempt            string `json:"attempt"`
	InitArtifactSHA256 string `json:"init_artifact_sha256"`
	WorkspaceRef       string `json:"workspace_ref"`
	PayloadRef         string `json:"payload_ref"`
}

// Intent is immutable compiled DATA. It cannot launch or select a backend, and
// cannot be upgraded through an operator flag, raw proof or injected callback.
// Its digest identifies canonical claims, not measured or authorized reality.
type Intent struct {
	claims    Claims
	canonical string
	digest    string
}

// NativeRequirements describe what a FUTURE separately qualified actor would
// need. These strings are never evidence that any requirement has been met.
type NativeRequirements struct {
	Birth              string
	Executable         string
	HostEnvironment    string
	Descriptors        string
	Fallback           string
	Qualification      string
	ExecutionAuthority string
}

// Compile enforces the actual closed ingress contract: exactly eight canonical
// key names with non-null strings, one protocol/operation/profile and bounded
// lowercase reference identifiers. It refuses duplicate/case-fold/unknown keys,
// trailing data, arbitrary executable/path/argv/env/UID/FD/mount/fallback inputs,
// and unsupported profiles. No cleanup, retry or legacy path is invoked.
//
// A successful return is NOT a durable attempt consumption, protected resource
// handle, verified artifact, native process, sandbox qualification or permission.
func Compile(input []byte) (Intent, error) {
	if len(input) == 0 || len(input) > MaxIntentBytes || !utf8.Valid(input) {
		return Intent{}, ErrIntent
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return Intent{}, ErrIntent
	}
	seen := make(map[string]bool, 8)
	var claims Claims
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return Intent{}, ErrIntent
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return Intent{}, ErrIntent
		}
		seen[key] = true
		var target *string
		switch key {
		case "protocol":
			target = &claims.Protocol
		case "operation":
			target = &claims.Operation
		case "profile":
			target = &claims.Profile
		case "scope":
			target = &claims.Scope
		case "attempt":
			target = &claims.Attempt
		case "init_artifact_sha256":
			target = &claims.InitArtifactSHA256
		case "workspace_ref":
			target = &claims.WorkspaceRef
		case "payload_ref":
			target = &claims.PayloadRef
		default:
			return Intent{}, ErrIntent
		}
		var raw json.RawMessage
		if err = decoder.Decode(&raw); err != nil || bytes.Equal(raw, []byte("null")) {
			return Intent{}, ErrIntent
		}
		if err = json.Unmarshal(raw, target); err != nil {
			return Intent{}, ErrIntent
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || len(seen) != 8 {
		return Intent{}, ErrIntent
	}
	var extra json.RawMessage
	if decoder.Decode(&extra) != io.EOF {
		return Intent{}, ErrIntent
	}
	if claims.Protocol != Protocol || claims.Operation != Operation || claims.Profile != Profile || !lowerHex(claims.Scope, 64) || !lowerHex(claims.Attempt, 32) || !lowerHex(claims.InitArtifactSHA256, 64) || !lowerHex(claims.WorkspaceRef, 64) || !lowerHex(claims.PayloadRef, 64) {
		return Intent{}, ErrIntent
	}
	canonical, err := json.Marshal(claims)
	if err != nil {
		return Intent{}, ErrIntent
	}
	digest := sha256.Sum256(canonical)
	return Intent{claims: claims, canonical: string(canonical), digest: hex.EncodeToString(digest[:])}, nil
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}

// Parsed reports syntax compilation only. It is not an eligibility check.
func (i Intent) Parsed() bool   { return i.canonical != "" }
func (i Intent) Claims() Claims { return i.claims }
func (i Intent) Canonical() []byte {
	if !i.Parsed() {
		return nil
	}
	return []byte(i.canonical)
}
func (i Intent) Digest() string { return i.digest }

// ActivationAuthorized is false for every intent, including zero and parsed
// values. This package has no activation switch or positive execution port.
func (i Intent) ActivationAuthorized() bool { return false }
func (i Intent) ExecutionAuthority() string { return "none" }

func (i Intent) Requirements() NativeRequirements {
	if !i.Parsed() {
		return NativeRequirements{}
	}
	return NativeRequirements{
		Birth:              "clone3_into_qualified_cgroup_fd_with_birth_pidfd",
		Executable:         "qualified_immutable_init_via_verified_fd_exec",
		HostEnvironment:    "fixed_broker_owned_allowlist",
		Descriptors:        "fixed_qualified_role_mapping",
		Fallback:           "never",
		Qualification:      "unimplemented",
		ExecutionAuthority: "none",
	}
}
