package nativeimage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture() Manifest {
	m := Manifest{DNSResolvers: []string{"1.1.1.1"}, Version: Version, Epoch: 1700000000, SizeMiB: 64, Architecture: "amd64", IncusVersion: "6.0.4", ZFSVersion: "2.2.6", LXCVersion: "7.0.1", LXCLibrary: "usr/lib/liblxc.so.1", Files: map[string]File{}, Links: map[string]string{}}
	dnsSum := sha256.Sum256([]byte("nameserver 1.1.1.1\n"))
	m.Files["etc/resolv.conf"] = File{hex.EncodeToString(dnsSum[:]), 0444}
	for _, p := range required {
		m.Files[p] = File{strings.Repeat("a", 64), 0555}
	}
	m.Files[m.LXCLibrary] = File{strings.Repeat("b", 64), 0444}
	return m
}
func TestManifestClosedInventory(t *testing.T) {
	m := fixture()
	if m.Validate() != nil {
		t.Fatal("valid")
	}
	for _, p := range []string{"../escape", "/absolute", "proc/self/mem", "var/lib/garm-capacity-native/incus", "etc/fstab"} {
		n := fixture()
		n.Files[p] = File{strings.Repeat("a", 64), 0444}
		if n.Validate() == nil {
			t.Fatal(p)
		}
	}
	m.Files[required[0]] = File{strings.Repeat("a", 64), 04755}
	if m.Validate() == nil {
		t.Fatal("setuid")
	}
}
func TestManifestLinks(t *testing.T) {
	m := fixture()
	m.Files["lib/x.so.1"] = File{strings.Repeat("b", 64), 0444}
	m.Links["lib/x.so"] = "x.so.1"
	if m.Validate() != nil {
		t.Fatal("valid link")
	}
	m.Links["lib/x.so"] = "../../outside"
	if m.Validate() == nil {
		t.Fatal("escape")
	}
	m.Links["lib/x.so"] = "x.so"
	if m.Validate() == nil {
		t.Fatal("cycle")
	}
}
func TestDeterministicManifestDigest(t *testing.T) {
	a, b := fixture(), fixture()
	a.Links["usr/lib/a"] = "/usr/sbin/zfs"
	b.Links["usr/lib/a"] = "/usr/sbin/zfs"
	if a.Digest() != b.Digest() {
		t.Fatal("map ordering")
	}
	b.Epoch++
	if a.Digest() == b.Digest() {
		t.Fatal("epoch unbound")
	}
}
func TestMissingELFRefused(t *testing.T) {
	if fixture().CheckELF(required[0], bytes.NewReader([]byte("not ELF"))) == nil {
		t.Fatal("nonELF")
	}
}
func TestMaterializeExactBytesAndModes(t *testing.T) {
	source, root := t.TempDir(), t.TempDir()
	raw := []byte("runtime input")
	h := sha256.Sum256(raw)
	m := fixture()
	m.Files = map[string]File{"usr/share/data": {hex.EncodeToString(h[:]), 0444}}
	m.Links = map[string]string{"usr/share/link": "data"}
	os.MkdirAll(filepath.Join(source, "usr/share"), 0755)
	os.WriteFile(filepath.Join(source, "usr/share/data"), raw, 0444)
	if materialize(source, root, m) != nil {
		t.Fatal("materialize")
	}
	got, _ := os.ReadFile(filepath.Join(root, "usr/share/data"))
	if !bytes.Equal(got, raw) {
		t.Fatal("bytes")
	}
	st, _ := os.Stat(filepath.Join(root, "usr/share/data"))
	if st.Mode().Perm() != 0444 || st.ModTime().Unix() != m.Epoch {
		t.Fatal(st)
	}
	os.Chmod(filepath.Join(source, "usr/share/data"), 0600)
	os.WriteFile(filepath.Join(source, "usr/share/data"), []byte("changed"), 0444)
	if materialize(source, t.TempDir(), m) == nil {
		t.Fatal("changed input")
	}
}

func TestPlaintextDependencyCannotSatisfyELFClosure(t *testing.T) {
	m := fixture()
	m.Files["lib/libc.so.6"] = File{strings.Repeat("a", 64), 0444}
	m.Files["lib/loader"] = File{strings.Repeat("a", 64), 0555}
	refs := map[string][]dependency{"usr/sbin/zfs": {{"lib/libc.so.6", false}, {"lib/loader", true}}}
	if dependencyKinds(m, map[string]bool{"lib/loader": true}, refs) {
		t.Fatal("plaintext libc accepted")
	}
	if dependencyKinds(m, map[string]bool{"lib/libc.so.6": true}, refs) {
		t.Fatal("plaintext loader accepted")
	}
	actual := map[string]bool{"lib/libc.so.6": true, "lib/loader": true}
	if !dependencyKinds(m, actual, refs) {
		t.Fatal("real ELF closure")
	}
	m.Files["lib/loader"] = File{strings.Repeat("a", 64), 0444}
	if dependencyKinds(m, actual, refs) {
		t.Fatal("nonexecutable interpreter")
	}
}

func TestLXCArtifactContract(t *testing.T) {
	for _, version := range []string{"", "6.0.4", "7.0.0", "7.0.1-custom", "7.0.2"} {
		m := fixture()
		m.LXCVersion = version
		if m.Validate() == nil {
			t.Fatal(version)
		}
	}
	m := fixture()
	delete(m.Files, m.LXCLibrary)
	if m.Validate() == nil {
		t.Fatal("missing actual library inventory")
	}
}

func TestVerifiedDebianLibrary(t *testing.T) {
	m := fixture()
	delete(m.Files, m.LXCLibrary)
	m.LXCVersion = "7.0.0"
	m.LXCPackage = "1:7.0.0-3"
	m.LXCLibrary = "usr/lib/x86_64-linux-gnu/liblxc.so.1.9.0"
	m.Files[m.LXCLibrary] = File{"d6f72b1de7484cc42fa5c84e5b0429f2f544ebd0e38367b08ee5cffbd482f5c2", 0444}
	m.Links["usr/lib/x86_64-linux-gnu/liblxc.so.1"] = "liblxc.so.1.9.0"
	if m.Validate() != nil {
		t.Fatal("verified vendor profile")
	}
	m.Files[m.LXCLibrary] = File{strings.Repeat("e", 64), 0444}
	if m.Validate() == nil {
		t.Fatal("caller checksum substituted for vendor bytes")
	}
}
