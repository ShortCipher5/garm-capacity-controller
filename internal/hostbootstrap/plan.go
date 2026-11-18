// Package hostbootstrap installs inventory on explicitly prepared hosts. It does
// not create accounts, change sshd policy, enroll capacity or revoke remote keys.
package hostbootstrap

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/crypto/ssh"
	"net/netip"
	"regexp"
)

const MaxArtifact = 16 << 20
const Command = "/usr/bin/sudo -n /usr/local/libexec/capacity-inventory-bootstrap"

var ErrInvalid = errors.New("bootstrap plan invalid")
var name = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type Plan struct {
	Address             string `json:"address"`
	Port                uint16 `json:"port"`
	AdminUser           string `json:"admin_user"`
	HostKey             string `json:"host_key"`
	AdminKeyFingerprint string `json:"admin_key_fingerprint"`
	ControllerIP        string `json:"controller_ip"`
	InventoryKey        string `json:"inventory_key"`
	ArtifactSHA256      string `json:"artifact_sha256"`
	Architecture        string `json:"architecture"`
}
type Request struct {
	Plan         Plan   `json:"plan"`
	ReviewDigest string `json:"review_digest"`
	Artifact     []byte `json:"artifact"`
}

func IP(s string) bool {
	ip, e := netip.ParseAddr(s)
	return e == nil && ip.Zone() == "" && !ip.Is4In6() && ip.String() == s && ip.IsGlobalUnicast()
}
func PublicKey(s string) (ssh.PublicKey, error) {
	k, _, o, r, e := ssh.ParseAuthorizedKey([]byte(s))
	if e != nil || len(o) != 0 || len(bytes.TrimSpace(r)) != 0 || k.Type() != ssh.KeyAlgoED25519 {
		return nil, ErrInvalid
	}
	return k, nil
}
func Digest(p Plan) string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Validate(r Request) error {
	p := r.Plan
	if !IP(p.Address) || !IP(p.ControllerIP) || p.Port == 0 || !name.MatchString(p.AdminUser) || p.AdminUser == "garm-inventory" || len(p.HostKey) > 16384 || len(p.InventoryKey) > 16384 || len(p.AdminKeyFingerprint) != 50 || r.ReviewDigest != Digest(p) || len(r.Artifact) == 0 || len(r.Artifact) > MaxArtifact {
		return ErrInvalid
	}
	if _, e := PublicKey(p.HostKey); e != nil {
		return e
	}
	k, e := PublicKey(p.InventoryKey)
	if e != nil || ssh.FingerprintSHA256(k) == p.AdminKeyFingerprint {
		return ErrInvalid
	}
	h := sha256.Sum256(r.Artifact)
	if hex.EncodeToString(h[:]) != p.ArtifactSHA256 {
		return ErrInvalid
	}
	f, e := elf.NewFile(bytes.NewReader(r.Artifact))
	if e != nil {
		return ErrInvalid
	}
	defer f.Close()
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return ErrInvalid
	}
	if (p.Architecture != "amd64" || f.Machine != elf.EM_X86_64) && (p.Architecture != "arm64" || f.Machine != elf.EM_AARCH64) {
		return ErrInvalid
	}
	// Only a static Linux helper is supported: no remote loader/library trust.
	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			return ErrInvalid
		}
	}
	return nil
}
func AuthorizedKey(p Plan) ([]byte, error) {
	k, e := PublicKey(p.InventoryKey)
	if e != nil || !IP(p.ControllerIP) {
		return nil, ErrInvalid
	}
	return []byte("restrict,from=\"" + p.ControllerIP + "\",command=\"/usr/local/libexec/capacity-host-inventory\" " + string(ssh.MarshalAuthorizedKey(k))), nil
}
