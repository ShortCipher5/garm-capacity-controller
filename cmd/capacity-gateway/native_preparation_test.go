package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePreparationDeploymentLoadAndRestrictedKeyRefusal(t *testing.T) {
	cwd, _ := os.Getwd()
	dir, e := os.MkdirTemp(cwd, ".preparation-load-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	bundleDir := filepath.Join(dir, "bundle")
	journal := filepath.Join(dir, "journal")
	os.Mkdir(bundleDir, 0700)
	os.Mkdir(journal, 0700)
	m := nativeprepare.Manifest{Version: nativeprepare.Version, ID: strings.Repeat("a", 64), Files: map[string]string{}}
	for _, n := range nativeprepare.FileNames() {
		raw := []byte(n)
		h := sha256.Sum256(raw)
		m.Files[n] = hex.EncodeToString(h[:])
		os.WriteFile(filepath.Join(bundleDir, n), raw, 0600)
	}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(bundleDir, "manifest.json"), raw, 0600)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	block, e := ssh.MarshalPrivateKey(priv, "")
	if e != nil {
		t.Fatal(e)
	}
	keyfile := filepath.Join(dir, "admin-key")
	os.WriteFile(keyfile, pem.EncodeToMemory(block), 0600)
	tokenfile := filepath.Join(dir, "token")
	os.WriteFile(tokenfile, []byte(strings.Repeat("b", 64)), 0600)
	target := nativeprovision.Target{Address: "192.0.2.1", Port: 22, AdminUser: "administrator", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey()))), AdminKeyFingerprint: ssh.FingerprintSHA256(key.PublicKey())}
	bundle, e := nativeprovision.LoadBundle(bundleDir)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := bundle.Plan(target)
	bundle.Close()
	if e != nil {
		t.Fatal(e)
	}
	a := &adminConfiguration{NativePreparation: &nativePreparationConfiguration{TokenFile: tokenfile, Credentials: map[string]string{"admin": keyfile}, Targets: map[string]nativePreparationTarget{"one": {Target: target, CredentialAlias: "admin", BundleDirectory: bundleDir, JournalDirectory: journal, ReviewDigest: nativeprovision.Digest(plan)}}}}
	targets, token, closeAll, e := loadNativePreparation(a)
	if e != nil || len(targets) != 1 || token != strings.Repeat("b", 64) {
		t.Fatal(e)
	}
	view, e := targets["one"].Service.View("one")
	closeAll()
	if e != nil || view.State.Status != "not_started" || view.ActivationAuthorized {
		t.Fatal(view, e)
	}
	a.SSHInventory = &sshInventoryConfiguration{Keys: map[string]string{"restricted": keyfile}}
	_, _, closeAll, e = loadNativePreparation(a)
	closeAll()
	if e == nil {
		t.Fatal("restricted inventory signer reused")
	}
	a.SSHInventory = nil
	original := a.NativePreparation.Targets["one"]
	changed := original
	changed.ReviewDigest = strings.Repeat("c", 64)
	a.NativePreparation.Targets["one"] = changed
	_, _, closeAll, e = loadNativePreparation(a)
	closeAll()
	if e == nil {
		t.Fatal("review drift accepted")
	}
	a.NativePreparation.Targets["one"] = original
	os.WriteFile(filepath.Join(bundleDir, "owner-client.key"), []byte("changed"), 0600)
	_, _, closeAll, e = loadNativePreparation(a)
	closeAll()
	if e == nil {
		t.Fatal("unreviewed secret accepted")
	}
}
