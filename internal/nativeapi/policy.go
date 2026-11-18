// Package nativeapi is the closed local Incus adapter for an owned fresh domain.
// Policy compilation and native success are never allocation or release proof.
package nativeapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrPolicy = errors.New("native policy refused")
var ErrNative = errors.New("native operation outcome unknown; retain charge")

const PolicyVersion = "garm-native-policy/v2"
const ProtectedPolicyVersion = "garm-native-policy/v3"
const NativeVersion = "6.0.4"

type PolicySpec struct {
	Version        string          `json:"version"`
	Controller     string          `json:"controller"`
	Host           string          `json:"host"`
	DomainID       string          `json:"domain_id"`
	NativeSHA256   string          `json:"native_sha256"`
	LauncherSHA256 string          `json:"launcher_sha256"`
	ServerVersion  string          `json:"server_version"`
	Pools          map[string]Pool `json:"pools"`
}
type Pool struct {
	Flavor      string `json:"flavor"`
	Image       string `json:"image"`
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Arch        string `json:"arch"`
	// Logical labels expected in legacy canonical plans; NOT Incus inheritance.
	ExpectedProfiles []string                       `json:"expected_profiles"`
	StoragePool      string                         `json:"storage_pool"`
	Bridge           string                         `json:"bridge"`
	ZFSDataset       string                         `json:"zfs_dataset"`
	ProtectedImage   *protectedboot.ImageDescriptor `json:"protected_image,omitempty"`
}
type Policy struct {
	spec   PolicySpec
	digest string
}

func NewPolicy(spec PolicySpec) (Policy, error) {
	if (spec.Version != PolicyVersion && spec.Version != ProtectedPolicyVersion) || spec.ServerVersion != NativeVersion || !label(spec.Controller) || !label(spec.Host) || !hash(spec.DomainID) || !hash(spec.NativeSHA256) || !hash(spec.LauncherSHA256) || len(spec.Pools) == 0 || len(spec.Pools) > 256 {
		return Policy{}, ErrPolicy
	}
	for id, p := range spec.Pools {
		if spec.Version == ProtectedPolicyVersion {
			if p.ProtectedImage == nil || protectedboot.ValidateDescriptor(*p.ProtectedImage) != nil {
				return Policy{}, ErrPolicy
			}
		} else if p.ProtectedImage != nil {
			return Policy{}, ErrPolicy
		}
		if !label(id) || !label(p.Flavor) || !label(p.Image) || !hash(p.Fingerprint) || p.Type != "container" || (p.Arch != "amd64" && p.Arch != "arm64") || !name(p.StoragePool) || !name(p.Bridge) || len(p.ExpectedProfiles) == 0 || len(p.ExpectedProfiles) > 16 {
			return Policy{}, ErrPolicy
		}
		seen := map[string]bool{}
		for _, v := range p.ExpectedProfiles {
			if !name(v) || seen[v] {
				return Policy{}, ErrPolicy
			}
			seen[v] = true
		}
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return Policy{}, ErrPolicy
	}
	var copy PolicySpec
	if json.Unmarshal(raw, &copy) != nil {
		return Policy{}, ErrPolicy
	}
	sum := sha256.Sum256(raw)
	return Policy{spec: copy, digest: hex.EncodeToString(sum[:])}, nil
}
func (p Policy) Digest() string     { return p.digest }
func (p Policy) DomainID() string   { return p.spec.DomainID }
func (p Policy) Controller() string { return p.spec.Controller }
func (p Policy) Host() string       { return p.spec.Host }

// ProtectedImage returns a detached immutable-policy descriptor, never proof
// that a live image or boot environment has been independently qualified.
func (p Policy) ProtectedImage(pool string) (protectedboot.ImageDescriptor, bool) {
	v, ok := p.spec.Pools[pool]
	if !ok || p.spec.Version != ProtectedPolicyVersion || v.ProtectedImage == nil {
		return protectedboot.ImageDescriptor{}, false
	}
	return protectedboot.CloneDescriptor(*v.ProtectedImage), true
}
func (p Policy) Protected() bool { return p.spec.Version == ProtectedPolicyVersion && hash(p.digest) }

// Allocation must come from the canonical native envelope, not caller-selected
// free-form labels/resources. The owner verifies that envelope by fresh central
// consumption before invoking Create. This structure alone grants nothing.
type Allocation struct {
	Controller, Host, Pool, Name, RequestKey string
	CPU, RAM, Disk, Runners                  int64
	Flavor, Image, Fingerprint, Type, Arch   string
	Profiles                                 []string
}
type Prepared struct {
	body       string
	digest     string
	name       string
	pool       Pool
	allocation Allocation
}

func (p Prepared) Body() []byte   { return []byte(p.body) }
func (p Prepared) Digest() string { return p.digest }
func (p Prepared) Name() string   { return p.name }
func (p Policy) Compile(a Allocation, userData string) (Prepared, error) {
	pool, ok := p.spec.Pools[a.Pool]
	if !hash(p.digest) || !ok || a.Controller != p.spec.Controller || a.Host != p.spec.Host || !name(a.Name) || !hash(a.RequestKey) || a.CPU <= 0 || a.CPU%1000 != 0 || a.RAM <= 0 || a.RAM > (1<<63-1)/(1<<20) || a.Disk <= 0 || a.Disk > (1<<63-1)/(1<<20) || a.Runners != 1 || a.Flavor != pool.Flavor || a.Image != pool.Image || a.Fingerprint != pool.Fingerprint || a.Type != pool.Type || a.Arch != pool.Arch || !reflect.DeepEqual(a.Profiles, pool.ExpectedProfiles) || len(userData) == 0 || len(userData) > 1<<20 || !utf8.ValidString(userData) {
		return Prepared{}, ErrPolicy
	}
	if p.spec.Version == ProtectedPolicyVersion {
		payload, e := protectedboot.Decode([]byte(userData))
		if e != nil || payload.RunnerName != a.Name || pool.ProtectedImage == nil || !reflect.DeepEqual(payload.Image, *pool.ProtectedImage) {
			return Prepared{}, ErrPolicy
		}
	}
	config := map[string]string{
		"limits.cpu": strconv.FormatInt(a.CPU/1000, 10), "limits.memory": strconv.FormatInt(a.RAM, 10) + "MiB",
		"user.user-data": userData, "user.runner-controller-id": a.Controller, "user.runner-pool-id": a.Pool, "user.garm-capacity.reservation": a.RequestKey, "user.garm-capacity.domain": p.spec.DomainID, "user.garm-capacity.policy": p.digest,
		"boot.autostart": "false", "boot.autorestart": "false", "snapshots.schedule": "", "security.guestapi": "false",
	}
	if pool.Type == "container" {
		config["limits.memory.enforce"] = "hard"
		config["limits.memory.swap"] = "false"
		config["limits.processes"] = "4096"
		config["security.privileged"] = "false"
		config["security.nesting"] = "false"
		config["security.idmap.isolated"] = "true"
	} else {
		config["security.secureboot"] = "true"
	}
	payload := struct {
		Name         string                       `json:"name"`
		Type         string                       `json:"type"`
		Architecture string                       `json:"architecture"`
		Profiles     []string                     `json:"profiles"`
		Config       map[string]string            `json:"config"`
		Devices      map[string]map[string]string `json:"devices"`
		Source       map[string]string            `json:"source"`
		Start        bool                         `json:"start"`
	}{Name: a.Name, Type: pool.Type, Architecture: map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[pool.Arch], Profiles: []string{}, Config: config, Devices: map[string]map[string]string{
		"root": {"type": "disk", "path": "/", "pool": pool.StoragePool, "size": strconv.FormatInt(a.Disk, 10) + "MiB"},
		"eth0": {"type": "nic", "nictype": "bridged", "parent": pool.Bridge, "name": "eth0", "security.mac_filtering": "true", "security.port_isolation": "true"},
	}, Source: map[string]string{"type": "image", "fingerprint": pool.Fingerprint}, Start: false}
	body, err := json.Marshal(payload)
	if err != nil {
		return Prepared{}, ErrPolicy
	}
	sum := sha256.Sum256(body)
	a.Profiles = append([]string(nil), a.Profiles...)
	return Prepared{body: string(body), digest: hex.EncodeToString(sum[:]), name: a.Name, pool: pool, allocation: a}, nil
}
func hash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func name(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' && i > 0) {
			return false
		}
	}
	return true
}
func label(s string) bool {
	if len(s) == 0 || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, c := range s {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}
