package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstbootConfigurationCrossPlanCredentialSeparation(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	counter := 0
	key := func() (ssh.Signer, string) {
		_, p, _ := ed25519.GenerateKey(rand.Reader)
		s, e := ssh.NewSignerFromKey(p)
		if e != nil {
			t.Fatal(e)
		}
		block, e := ssh.MarshalPrivateKey(p, "")
		if e != nil {
			t.Fatal(e)
		}
		counter++
		return s, write(fmt.Sprintf("key-%d", counter), pem.EncodeToMemory(block))
	}
	admin, path := key()
	other, otherPath := key()
	inventory, _ := key()
	host, _ := key()
	b := make([]byte, 64)
	copy(b, []byte{127, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	hash := sha256.Sum256(b)
	artifact := write("artifact", b)
	base := hostbootstrap.Plan{Address: "192.0.2.20", Port: 22, AdminUser: "bootstrap", ControllerIP: "192.0.2.10", HostKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), InventoryKey: string(ssh.MarshalAuthorizedKey(inventory.PublicKey())), AdminKeyFingerprint: ssh.FingerprintSHA256(admin.PublicKey()), ArtifactSHA256: hex.EncodeToString(hash[:]), Architecture: "amd64"}
	plan := firstboot.Plan{Base: base, ListenAddress: base.Address, InventoryAddress: base.Address, InventoryPort: 19922, InstallerSHA256: firstboot.InstallerSHA256(), Manifest: firstboot.Manifest{Profile: "ubuntu-24.04-systemd-nftables/v1", Architecture: "amd64", ArtifactSHA256: base.ArtifactSHA256, InstallerSHA256: firstboot.InstallerSHA256()}}
	manifest, _ := json.Marshal(plan.Manifest)
	manifestFile := write("manifest", manifest)

	a := &adminConfiguration{TokenFile: write("read", []byte(strings.Repeat("a", 64))), Firstboot: &firstbootConfiguration{TokenFile: write("bootstrap", []byte(strings.Repeat("e", 64))), Credentials: map[string]string{"admin": path, "other": otherPath}, Targets: map[string]firstbootTarget{"one": {Plan: plan, Digest: firstboot.Digest(plan), CredentialAlias: "admin", ArtifactFile: artifact, ManifestFile: manifestFile}}}}
	if _, _, _, e := loadFirstboot(a); e != nil {
		t.Fatal(e)
	}
	second := plan
	second.Base.Address = "192.0.2.21"
	second.Base.AdminKeyFingerprint = ssh.FingerprintSHA256(other.PublicKey())
	second.Base.InventoryKey = string(ssh.MarshalAuthorizedKey(admin.PublicKey()))
	a.Firstboot.Targets["two"] = firstbootTarget{Plan: second, Digest: firstboot.Digest(second), CredentialAlias: "other", ArtifactFile: artifact, ManifestFile: manifestFile}
	if _, _, _, e := loadFirstboot(a); e == nil {
		t.Fatal("admin key provisioned as another inventory key")
	}
	delete(a.Firstboot.Targets, "two")
	// Prepared-helper admin credentials cannot become first-time full-admin credentials.
	a.Bootstrap = &bootstrapConfiguration{Credentials: map[string]string{"helper": path}, Targets: map[string]bootstrapTarget{}, TokenFile: write("prepared-token", []byte(strings.Repeat("f", 64)))}
	if _, _, _, e := loadFirstboot(a); e == nil {
		t.Fatal("prepared helper credential reused for full admin")
	}
	a.Bootstrap = nil
	os.WriteFile(manifestFile, []byte(`{}`), 0600)
	if _, _, _, e := loadFirstboot(a); e == nil {
		t.Fatal("untrusted manifest accepted")
	}
	os.WriteFile(manifestFile, manifest, 0600)
	a.Firstboot.TokenFile = a.TokenFile
	if _, _, _, e := loadFirstboot(a); e == nil {
		t.Fatal("read token reused")
	}
}
