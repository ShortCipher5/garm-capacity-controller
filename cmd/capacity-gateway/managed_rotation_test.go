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
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedRotationConfigurationRoleScopeAndArtifactBinding(t *testing.T) {
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
	inventory, inventoryPath := key()
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

	remote := firstboot.RotationPlan{RequestID: "11111111-1111-4111-8111-111111111111", Installation: plan, InstallationDigest: firstboot.Digest(plan), ListenerHostKey: base.HostKey, OldKey: base.InventoryKey, NewKey: string(ssh.MarshalAuthorizedKey(other.PublicKey())), ProgramSHA256: firstboot.RotationProgramSHA256(), RestartInventorySessions: true}
	old := sshinventory.Config{Address: plan.InventoryAddress, Port: 19922, User: "garm-inventory-listener", HostKey: base.HostKey, CredentialRef: "old"}
	next := old
	next.CredentialRef = "new"
	managed := firstboot.ManagedRotationPlan{Remote: remote, AdminAlias: "admin", OldID: "old-target", OldConfig: old, NewID: "new-target", NewConfig: next}
	a := &adminConfiguration{TokenFile: write("read", []byte(strings.Repeat("a", 64))), EnableSSHInventory: true, EnableSSHTargetManagement: true, SSHInventory: &sshInventoryConfiguration{TokenFile: write("inventory-token", []byte(strings.Repeat("b", 64))), TargetManagementTokenFile: write("target-token", []byte(strings.Repeat("c", 64))), Keys: map[string]string{"old": inventoryPath, "new": otherPath}}, Rotation: &rotationConfiguration{TokenFile: write("rotation-token", []byte(strings.Repeat("f", 64))), Credentials: map[string]string{"admin": path}, Targets: map[string]rotationTarget{"one": {Plan: managed, Digest: firstboot.ManagedRotationDigest(managed), ArtifactFile: artifact, ManifestFile: manifestFile}}}}
	if _, _, _, _, e := loadManagedRotation(a); e != nil {
		t.Fatal(e)
	}
	a.EnableSSHTargetManagement = false
	if _, _, _, _, e := loadManagedRotation(a); e == nil {
		t.Fatal("registry capability missing")
	}
	a.EnableSSHTargetManagement = true
	a.SSHInventory.Keys["new"] = path
	if _, _, _, _, e := loadManagedRotation(a); e == nil {
		t.Fatal("full admin key used for inventory")
	}
	a.SSHInventory.Keys["new"] = otherPath
	token := a.Rotation.TokenFile
	a.Rotation.TokenFile = a.SSHInventory.TokenFile
	if _, _, _, _, e := loadManagedRotation(a); e == nil {
		t.Fatal("token role collision")
	}
	a.Rotation.TokenFile = token
	os.WriteFile(manifestFile, []byte(`{}`), 0600)
	if _, _, _, _, e := loadManagedRotation(a); e == nil {
		t.Fatal("unreviewed manifest")
	}
	os.WriteFile(manifestFile, manifest, 0600)
	target := a.Rotation.Targets["one"]
	target.Plan.NewConfig.Address = "192.0.2.21"
	target.Digest = firstboot.ManagedRotationDigest(target.Plan)
	a.Rotation.Targets["one"] = target
	if _, _, _, _, e := loadManagedRotation(a); e == nil {
		t.Fatal("registry and remote endpoint mismatch")
	}
}
