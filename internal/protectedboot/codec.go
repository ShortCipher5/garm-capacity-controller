// Package protectedboot defines a canonical protected-image boot payload. Parsing
// authenticates no image or owner; callers must bind it to the canonical native
// body digest and the independently configured image policy.
package protectedboot

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
)

const Version = "protected-runner-boot/v1"
const ManifestPath = "/etc/garm-capacity/launch.json"
const JITPath = "/etc/garm-capacity/jit"

var ErrPayload = errors.New("canonical protected image bootstrap required")

type ImageDescriptor struct {
	InitSHA256     string            `json:"init_sha256"`
	LauncherSHA256 string            `json:"launcher_sha256"`
	BundleDigest   string            `json:"bundle_digest"`
	RunnerRelease  string            `json:"runner_release"`
	UID            uint32            `json:"uid"`
	GID            uint32            `json:"gid"`
	Files          map[string]string `json:"files"`
}

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func ValidHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func (v ImageDescriptor) Valid() bool {
	return ValidHash(v.InitSHA256) && ValidHash(v.LauncherSHA256) && ValidHash(v.BundleDigest) && len(v.RunnerRelease) > 0 && len(v.RunnerRelease) < 64 && strings.IndexFunc(v.RunnerRelease, func(r rune) bool { return !(r >= '0' && r <= '9' || r == '.' || r == '-') }) < 0 && v.UID >= 1000 && v.GID >= 1000 && v.UID != 65534 && v.GID != 65534
}

type Payload struct {
	Version    string          `json:"version"`
	RunnerName string          `json:"runner_name"`
	Image      ImageDescriptor `json:"image"`
	JIT        string          `json:"jit"`
}

// Decode insists on exact canonical JSON, rejecting duplicate/case-alias keys,
// unsupported fields and trailing bytes by roundtrip equality. Secret JIT bytes
// remain only in caller memory and never belong in ledger/audit payloads.
func Decode(raw []byte) (Payload, error) {
	var p Payload
	if len(raw) == 0 || len(raw) > 1<<20 {
		return p, ErrPayload
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return Payload{}, ErrPayload
	}
	canonical, e := json.Marshal(p)
	if e != nil || !bytes.Equal(raw, canonical) || p.Version != Version || ValidateDescriptor(p.Image) != nil || p.RunnerName == "" || len(p.RunnerName) > 63 || len(p.Image.Files) == 0 || len(p.Image.Files) > 20000 {
		return Payload{}, ErrPayload
	}
	for _, c := range p.RunnerName {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return Payload{}, ErrPayload
		}
	}
	if !validJIT(p.JIT, p.RunnerName) {
		return Payload{}, ErrPayload
	}
	return p, nil
}
func validJIT(s, name string) bool {
	b, e := base64.StdEncoding.Strict().DecodeString(s)
	if e != nil || len(b) > 1<<20 {
		return false
	}
	var files map[string]string
	d := json.NewDecoder(bytes.NewReader(b))
	if d.Decode(&files) != nil || d.Decode(new(any)) != io.EOF || len(files) != 3 {
		return false
	}
	// Canonical JIT file order is not prescribed by GitHub, so explicitly reject
	// duplicate keys rather than canonicalizing upstream content.
	d = json.NewDecoder(bytes.NewReader(b))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		tok, e = d.Token()
		key, ok := tok.(string)
		if e != nil || !ok || seen[key] || (key != ".runner" && key != ".credentials" && key != ".credentials_rsaparams") {
			return false
		}
		seen[key] = true
		var value string
		if d.Decode(&value) != nil {
			return false
		}
	}
	settings := map[string]json.RawMessage{}
	for name, value := range files {
		raw, e := base64.StdEncoding.Strict().DecodeString(value)
		if e != nil || len(raw) == 0 || len(raw) > 1<<19 {
			return false
		}
		if name == ".runner" {
			d = json.NewDecoder(bytes.NewReader(raw))
			tok, e = d.Token()
			if e != nil || tok != json.Delim('{') {
				return false
			}
			for d.More() {
				tok, e = d.Token()
				k, ok := tok.(string)
				if e != nil || !ok {
					return false
				}
				k = strings.ToLower(k)
				if _, ok = settings[k]; ok {
					return false
				}
				var v json.RawMessage
				if d.Decode(&v) != nil {
					return false
				}
				settings[k] = v
			}
			tok, e = d.Token()
			if e != nil || tok != json.Delim('}') || d.Decode(new(any)) != io.EOF {
				return false
			}
		}
	}
	var actual, work, monitor string
	var ephemeral, disable bool
	if json.Unmarshal(settings["agentname"], &actual) != nil || actual != name || json.Unmarshal(settings["workfolder"], &work) != nil || work != "_work" || json.Unmarshal(settings["ephemeral"], &ephemeral) != nil || !ephemeral || json.Unmarshal(settings["disableupdate"], &disable) != nil || !disable {
		return false
	}
	if value, ok := settings["monitorsocketaddress"]; ok && (json.Unmarshal(value, &monitor) != nil || monitor != "") {
		return false
	}
	return true
}

type Binding struct{ DomainID, PolicyDigest, ResourceID, ImageFingerprint string }
type Manifest struct {
	Version          string          `json:"version"`
	DomainID         string          `json:"domain_id"`
	PolicyDigest     string          `json:"policy_digest"`
	ResourceID       string          `json:"resource_id"`
	ImageFingerprint string          `json:"image_fingerprint"`
	RunnerName       string          `json:"runner_name"`
	Image            ImageDescriptor `json:"image"`
}
type File struct {
	Path   string
	SHA256 string
	Bytes  int64
	Data   []byte
}

// Materialize derives the only two fixed files after successful stopped CREATE.
// Data is sensitive; callers must never persist it in public ledger or logs.
func Materialize(p Payload, b Binding) ([2]File, error) {
	raw, e := json.Marshal(p)
	if e != nil {
		return [2]File{}, ErrPayload
	}
	p, e = Decode(raw)
	if e != nil || !ValidHash(b.DomainID) || !ValidHash(b.PolicyDigest) || !ValidHash(b.ImageFingerprint) || !uuid(b.ResourceID) {
		return [2]File{}, ErrPayload
	}
	manifest, e := json.Marshal(Manifest{Version, b.DomainID, b.PolicyDigest, b.ResourceID, b.ImageFingerprint, p.RunnerName, p.Image})
	if e != nil {
		return [2]File{}, ErrPayload
	}
	jit := []byte(p.JIT)
	return [2]File{{ManifestPath, Hash(manifest), int64(len(manifest)), manifest}, {JITPath, Hash(jit), int64(len(jit)), jit}}, nil
}
func uuid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s != "00000000-0000-0000-0000-000000000000"
}

func ValidateDescriptor(v ImageDescriptor) error {
	if !v.Valid() || len(v.Files) == 0 || len(v.Files) > 20000 {
		return ErrPayload
	}
	for name, hash := range v.Files {
		if path.Clean(name) != name || (!strings.HasPrefix(name, "bin/") && !strings.HasPrefix(name, "externals/")) || !ValidHash(hash) || strings.ContainsAny(name, "\x00\r\n\\") {
			return ErrPayload
		}
	}
	if !ValidHash(v.Files["bin/Runner.Listener"]) || !ValidHash(v.Files["bin/Runner.Listener.dll"]) {
		return ErrPayload
	}
	fileJSON, _ := json.Marshal(v.Files)
	if Hash(fileJSON) != v.BundleDigest {
		return ErrPayload
	}
	return nil
}
func CloneDescriptor(v ImageDescriptor) ImageDescriptor {
	m := make(map[string]string, len(v.Files))
	for k, x := range v.Files {
		m[k] = x
	}
	v.Files = m
	return v
}
