package firstboot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"golang.org/x/crypto/ssh"
	"net"
	"os/exec"
	"strings"
	"testing"
)

func key(t *testing.T) ssh.Signer {
	t.Helper()
	_, p, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	k, e := ssh.NewSignerFromKey(p)
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func fixture(t *testing.T) (Request, ssh.Signer) {
	admin, host, inventory := key(t), key(t), key(t)
	b := make([]byte, 64)
	copy(b, []byte{127, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	hash := sha256.Sum256(b)
	artifact := hex.EncodeToString(hash[:])
	p := Plan{Base: hostbootstrap.Plan{Address: "192.0.2.20", Port: 22, AdminUser: "bootstrap", HostKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), ControllerIP: "192.0.2.10", InventoryKey: string(ssh.MarshalAuthorizedKey(inventory.PublicKey())), AdminKeyFingerprint: ssh.FingerprintSHA256(admin.PublicKey()), ArtifactSHA256: artifact, Architecture: "amd64"}, ListenAddress: "192.0.2.20", InventoryAddress: "192.0.2.20", InventoryPort: 19922, EnableAtBoot: true, InstallerSHA256: InstallerSHA256(), Manifest: Manifest{Profile: "ubuntu-24.04-systemd-nftables/v1", Architecture: "amd64", ArtifactSHA256: artifact, InstallerSHA256: InstallerSHA256()}}
	return Request{Plan: p, ReviewDigest: Digest(p), Artifact: b}, admin
}
func TestReviewBindsInstallerArtifactIdentityAndNetwork(t *testing.T) {
	r, admin := fixture(t)
	if e := Validate(r); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Request){func(r *Request) { r.Plan.ListenAddress = "192.0.2.20;id" }, func(r *Request) { r.Plan.Base.InventoryKey = string(ssh.MarshalAuthorizedKey(admin.PublicKey())) }, func(r *Request) { r.Plan.Manifest.ArtifactSHA256 = strings.Repeat("0", 64) }, func(r *Request) {
		r.Plan.InstallerSHA256 = strings.Repeat("0", 64)
		r.Plan.Manifest.InstallerSHA256 = r.Plan.InstallerSHA256
	}, func(r *Request) { r.Plan.Base.Port = 19922 }} {
		bad := r
		change(&bad)
		bad.ReviewDigest = Digest(bad.Plan)
		if Validate(bad) == nil {
			t.Fatal("invalid plan accepted")
		}
	}
	r.Plan.EnableAtBoot = false
	if Validate(r) == nil {
		t.Fatal("approval substitution")
	}
}
func TestFixedSourceAndCrossLanguageDigest(t *testing.T) {
	r, _ := fixture(t)
	raw, _ := json.Marshal(r)
	cmd := exec.Command("python3", "-c", `import importlib.util,json,sys
s=importlib.util.spec_from_file_location('installer','installer.py');m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
p,d,a=m.validate(json.load(sys.stdin));print(d)`)
	cmd.Stdin = bytes.NewReader(raw)
	out, e := cmd.CombinedOutput()
	if e != nil || strings.TrimSpace(string(out)) != r.ReviewDigest {
		t.Fatal(e, string(out))
	}
	if !strings.HasPrefix(command(), "/usr/bin/sudo -n /usr/bin/python3 -I -c '") {
		t.Fatal("unexpected command")
	}
	if strings.Contains(command(), r.Plan.Base.InventoryKey) || strings.Contains(command(), r.Plan.Base.Address) {
		t.Fatal("request interpolated into program")
	}
}
func TestWrongAdminFailsBeforeAnyNetwork(t *testing.T) {
	r, _ := fixture(t)
	called := false
	_, e := execute(context.Background(), r, key(t), func(context.Context, string, string) (net.Conn, error) { called = true; return nil, ErrInvalid })
	if e == nil || called {
		t.Fatal("wrong admin reached network")
	}
}
func TestPythonLifecycleRegressions(t *testing.T) {
	cmd := exec.Command("python3", "-m", "unittest", "discover", "-s", ".", "-p", "installer_test.py", "-v")
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatal(e, string(out))
	}
}
