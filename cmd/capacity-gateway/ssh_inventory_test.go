package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHInventoryStartupNeedsExplicitFlagAndDistinctToken(t *testing.T) {
	keys := credentials(t)
	dir := t.TempDir()
	budget := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
	store, e := admission.OpenSQLite(filepath.Join(dir, "ledger.db"), admission.Config{Global: budget, Hosts: map[string]admission.Host{"host": {Budget: budget, Mode: "disabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"host"}}}, MaxObservationAge: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	readPath, sshPath, keyPath := filepath.Join(dir, "read"), filepath.Join(dir, "ssh-role"), filepath.Join(dir, "private")
	os.WriteFile(readPath, []byte(strings.Repeat("a", 64)), 0600)
	os.WriteFile(sshPath, []byte(strings.Repeat("c", 64)), 0600)
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	der, _ := x509.MarshalPKCS8PrivateKey(private)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600)
	target := sshinventory.Config{Address: "192.0.2.20", Port: 22, User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), CredentialRef: "existing-key"}
	c := &adminConfiguration{Listen: "127.0.0.1:0", TLS: keys.serverFiles, TokenFile: readPath, SSHInventory: &sshInventoryConfiguration{TokenFile: sshPath, Keys: map[string]string{"existing-key": keyPath}, Targets: map[string]sshinventory.Config{"candidate": target}}}
	server, close, e := newAdminServer(c, store, false)
	if e != nil {
		t.Fatal(e)
	}
	close()
	server.Close()
	c.EnableSSHInventory = true
	server, close, e = newAdminServer(c, store, false)
	if e != nil {
		t.Fatal(e)
	}
	close()
	server.Close()
	c.SSHInventory.TokenFile = readPath
	if _, close, e = newAdminServer(c, store, false); e == nil {
		close()
		t.Fatal("read token accepted for SSH")
	}
	c.SSHInventory.TokenFile = sshPath
	target.CredentialRef = "other"
	c.SSHInventory.Targets["candidate"] = target
	if _, _, e = newSSHInventory(c.SSHInventory, store); e == nil {
		t.Fatal("unmapped target credential accepted")
	}
}

func TestSSHTargetRegistrationStartupPolicyAndSeparateRole(t *testing.T) {
	keys := credentials(t)
	dir := t.TempDir()
	budget := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
	store, e := admission.OpenSQLite(filepath.Join(dir, "ledger.db"), admission.Config{Global: budget, Hosts: map[string]admission.Host{"host": {Budget: budget, Mode: "disabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"host"}}}, MaxObservationAge: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if e := os.WriteFile(p, []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	readPath, sshPath, targetPath := write("read", strings.Repeat("a", 64)), write("ssh", strings.Repeat("c", 64)), write("targets", strings.Repeat("d", 64))
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(private)
	keyPath := write("key", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
	c := &adminConfiguration{Listen: "127.0.0.1:0", TLS: keys.serverFiles, TokenFile: readPath, EnableSSHTargetManagement: true, SSHInventory: &sshInventoryConfiguration{TokenFile: sshPath, TargetManagementTokenFile: targetPath, Keys: map[string]string{"key": keyPath}, Scopes: map[string]sshinventory.Scope{"key": {Networks: []string{"192.0.2.0/24"}, Users: []string{"inventory"}, Ports: []uint16{22}}}}}
	if _, close, e := newAdminServer(c, store, false); e == nil {
		close()
		t.Fatal("registration enabled without inventory opt-in")
	}
	c.EnableSSHInventory = true
	server, close, e := newAdminServer(c, store, false)
	if e != nil {
		t.Fatal(e)
	}
	close()
	server.Close()
	c.SSHInventory.TargetManagementTokenFile = sshPath
	if _, close, e := newAdminServer(c, store, false); e == nil {
		close()
		t.Fatal("inventory token gained target creation")
	}
	c.SSHInventory.TargetManagementTokenFile = targetPath
	c.SSHInventory.Scopes["unmapped"] = c.SSHInventory.Scopes["key"]
	if _, _, e := newSSHTargetRegistry(c.SSHInventory, store); e == nil {
		t.Fatal("unmapped credential accepted")
	}
}
