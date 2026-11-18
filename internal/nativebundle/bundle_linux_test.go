//go:build linux

package nativebundle

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cert(t *testing.T, serial int64) ([]byte, []byte) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	v := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, v, v, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	pk, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})
}
func elfFixture() []byte {
	b := make([]byte, 64)
	copy(b, []byte{127, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	return b
}
func fixture(t *testing.T) (Configuration, map[string]any) {
	t.Helper()
	e, p, parent := t.TempDir(), t.TempDir(), t.TempDir()
	c := Configuration{ExportDirectory: e, PrivateDirectory: p, OutputDirectory: filepath.Join(parent, "bundle"), ID: strings.Repeat("c", 64), Architecture: "amd64"}
	sums := ""
	for _, n := range nativeprepare.ExecutableNames() {
		b := elfFixture()
		if os.WriteFile(filepath.Join(e, n), b, 0555) != nil {
			t.Fatal("write")
		}
		sums += digest(b) + "  " + n + "\n"
	}
	os.WriteFile(filepath.Join(e, "SHA256SUMS"), []byte(sums), 0444)
	c.ExportManifestSHA256 = digest([]byte(sums))
	sc, sk := cert(t, 1)
	cc, ck := cert(t, 2)
	for n, b := range map[string][]byte{"owner-server.crt": sc, "owner-server.key": sk, "owner-client.crt": cc, "owner-client.key": ck, "controller-ca.crt": sc} {
		os.WriteFile(filepath.Join(p, n), b, 0600)
	}
	cfg := map[string]any{"control": map[string]any{"policy": map[string]any{"version": "garm-native-policy/v5", "domain_id": ""}, "controller_client_leaf": strings.Repeat("a", 64), "controller_server_leaf": strings.Repeat("b", 64), "pool_reader": map[string]any{}}, "launch": map[string]any{}, "storage": map[string]any{}, "artifacts": map[string]any{}, "network_tools": map[string]any{}, "quota_reader": map[string]any{}}
	writeConfig(t, c, cfg)
	return c, cfg
}
func writeConfig(t *testing.T, c Configuration, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if os.WriteFile(filepath.Join(c.PrivateDirectory, "capsule.json"), b, 0600) != nil {
		t.Fatal("config")
	}
}
func TestBuildExactPrivateBundleWithoutExecution(t *testing.T) {
	c, _ := fixture(t)
	r, e := Build(t.Context(), c)
	if e != nil || r.ActivationAuthorized || r.State != "bundle_created_not_prepared" {
		t.Fatal(r, e)
	}
	raw, e := os.ReadFile(filepath.Join(c.OutputDirectory, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	var m nativeprepare.Manifest
	if json.Unmarshal(raw, &m) != nil || nativeprepare.Validate(m) != nil {
		t.Fatal("manifest")
	}
	h, _ := nativeprepare.ManifestDigest(m)
	if h != r.ManifestDigest {
		t.Fatal("digest")
	}
	for _, n := range nativeprepare.FileNames() {
		b, e := os.ReadFile(filepath.Join(c.OutputDirectory, n))
		s, se := os.Stat(filepath.Join(c.OutputDirectory, n))
		if e != nil || se != nil || s.Mode().Perm() != 0600 || digest(b) != m.Files[n] {
			t.Fatal("file", n, e, se)
		}
	}
	raw, _ = os.ReadFile(filepath.Join(c.OutputDirectory, "capsule.json"))
	cfg, e := decodeObject(raw)
	if e != nil {
		t.Fatal(e)
	}
	control, _ := object(cfg, "control")
	if get(control, "server_key") != filepath.Join(nativeprepare.Parent, c.ID, "private/owner-server.key") {
		t.Fatal("path not bound")
	}
	if _, e = Build(t.Context(), c); e == nil {
		t.Fatal("overwrote bundle")
	}
}
func TestBuildRejectsWrongPinRoleAliasAndArchitecture(t *testing.T) {
	for _, mode := range []string{"pin", "role", "architecture", "duplicate", "link", "shared-key"} {
		t.Run(mode, func(t *testing.T) {
			c, cfg := fixture(t)
			switch mode {
			case "pin":
				c.ExportManifestSHA256 = strings.Repeat("a", 64)
			case "role":
				ctrl := cfg["control"].(map[string]any)
				ctrl["controller_server_leaf"] = ctrl["controller_client_leaf"]
				writeConfig(t, c, cfg)
			case "architecture":
				c.Architecture = "arm64"
			case "duplicate":
				os.WriteFile(filepath.Join(c.PrivateDirectory, "capsule.json"), []byte(`{"control":{},"CONTROL":{}}`), 0600)
			case "shared-key":
				raw, _ := os.ReadFile(filepath.Join(c.PrivateDirectory, "owner-server.key"))
				block, _ := pem.Decode(raw)
				key, e := x509.ParsePKCS8PrivateKey(block.Bytes)
				if e != nil {
					t.Fatal(e)
				}
				signer := key.(crypto.Signer)
				template := &x509.Certificate{SerialNumber: big.NewInt(99), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
				der, e := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
				if e != nil {
					t.Fatal(e)
				}
				os.WriteFile(filepath.Join(c.PrivateDirectory, "owner-client.key"), raw, 0600)
				os.WriteFile(filepath.Join(c.PrivateDirectory, "owner-client.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
			case "link":
				name := nativeprepare.ExecutableNames()[0]
				os.Remove(filepath.Join(c.ExportDirectory, name))
				os.Symlink("capacity-provider", filepath.Join(c.ExportDirectory, name))
			}
			if _, e := Build(t.Context(), c); e == nil {
				t.Fatal("accepted", mode)
			}
			if _, e := os.Stat(filepath.Join(c.OutputDirectory, "manifest.json")); !os.IsNotExist(e) {
				t.Fatal("partial appeared complete")
			}
		})
	}
}
func TestManifestGrammarAndCancellation(t *testing.T) {
	for _, b := range []string{"{} trailing", `{"a":{"x":1,"X":2}}`, `{"a":1,"a":2}`, `{"storage":{},"ſtorage":{}}`} {
		if _, e := decodeObject([]byte(b)); e == nil {
			t.Fatal("ambiguous config")
		}
	}
	c, _ := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := Build(ctx, c); e == nil {
		t.Fatal("cancellation")
	}
	if _, e := os.Stat(c.OutputDirectory); !os.IsNotExist(e) {
		t.Fatal("created canceled output")
	}
}
