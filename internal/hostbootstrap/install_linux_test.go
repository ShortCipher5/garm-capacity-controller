package hostbootstrap

import (
	"context"
	"os"
	"strings"
	"testing"
)

const policy = `forcecommand none
usedns no
authenticationmethods publickey
pubkeyauthentication yes
passwordauthentication no
kbdinteractiveauthentication no
gssapiauthentication no
hostbasedauthentication no
authorizedkeyscommand none
trustedusercakeys none
authorizedprincipalscommand none
permituserenvironment no
authorizedkeysfile /etc/ssh/authorized_keys/garm-inventory
`

func TestPolicyRejectsAlternativeAuthenticationPaths(t *testing.T) {
	if e := CheckPolicy(policy); e != nil {
		t.Fatal(e)
	}
	for _, line := range strings.Split(strings.TrimSpace(policy), "\n") {
		if CheckPolicy(strings.Replace(policy, line+"\n", "", 1)) == nil {
			t.Fatal("missing", line)
		}
		if CheckPolicy(policy+line+"\n") == nil {
			t.Fatal("duplicate", line)
		}
	}
	for _, pair := range [][2]string{{"passwordauthentication no", "passwordauthentication yes"}, {"usedns no", "usedns yes"}, {"authorizedkeyscommand none", "authorizedkeyscommand /bin/other"}, {"authenticationmethods publickey", "authenticationmethods any"}, {"authorizedkeysfile /etc/ssh/authorized_keys/garm-inventory", "authorizedkeysfile .ssh/authorized_keys"}} {
		if CheckPolicy(strings.Replace(policy, pair[0], pair[1], 1)) == nil {
			t.Fatal(pair)
		}
	}
}
func TestInstallRefusesUnprivilegedBeforeMutation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged test process")
	}
	r, _ := sample(t)
	if e := Install(context.Background(), r); e == nil || !strings.Contains(e.Error(), "privileged") {
		t.Fatal(e)
	}
}
func TestProtectedRejectsWritableAncestorAndSymlink(t *testing.T) {
	dir := t.TempDir()
	if protected(dir, true) == nil {
		t.Fatal("untrusted temp path accepted")
	}
	link := dir + "/link"
	if e := os.Symlink("/", link); e != nil {
		t.Fatal(e)
	}
	if protected(link, true) == nil {
		t.Fatal("symlink accepted")
	}
}

func TestHostPolicyBindsProtectedArtifactAndEndpoint(t *testing.T) {
	r, _ := sample(t)
	p := HostPolicy{Address: r.Plan.Address, Port: r.Plan.Port, ControllerIP: r.Plan.ControllerIP, LocalAddress: "192.0.2.20", LocalPort: 22, ArtifactSHA256: r.Plan.ArtifactSHA256}
	if p.Check(r) != nil {
		t.Fatal("valid policy")
	}
	r.Plan.ArtifactSHA256 = strings.Repeat("a", 64)
	if p.Check(r) == nil {
		t.Fatal("unapproved artifact")
	}
	r, _ = sample(t)
	p.ControllerIP = "192.0.2.11"
	if p.Check(r) == nil {
		t.Fatal("unapproved source")
	}
}

func TestImmutablePublicationAndPartialRetry(t *testing.T) {
	dir := t.TempDir()
	check := func(path string, isDir bool) error {
		if path == dir {
			return nil
		}
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return ErrInvalid
		}
		return nil
	}
	path := dir + "/binary"
	data := []byte("reviewed artifact")
	if e := installFile(path, data, 0755, check); e != nil {
		t.Fatal(e)
	}
	if e := installFile(path, data, 0755, check); e != nil {
		t.Fatal("idempotent retry", e)
	}
	if installFile(path, []byte("different artifact"), 0755, check) == nil {
		t.Fatal("overwritten")
	}
	got, e := os.ReadFile(path)
	if e != nil || string(got) != string(data) {
		t.Fatal("changed destination")
	}
	// Simulate an interrupted private staging file: destination remains absent,
	// retry writes a complete new file and does not consume unknown staging data.
	if e = os.WriteFile(dir+"/.inventory-install-abandoned", []byte("partial"), 0600); e != nil {
		t.Fatal(e)
	}
	key := dir + "/key"
	deny := func(string, bool) error { return ErrInvalid }
	if installFile(key, []byte("restricted-key"), 0644, deny) == nil {
		t.Fatal("permission failure ignored")
	}
	if _, e = os.Stat(key); !os.IsNotExist(e) {
		t.Fatal("partial key published")
	}
	if e = installFile(path, data, 0755, check); e != nil {
		t.Fatal(e)
	}
	if e = installFile(key, []byte("restricted-key"), 0644, check); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(key, 0666); e != nil {
		t.Fatal(e)
	}
	if installFile(key, []byte("restricted-key"), 0644, check) == nil {
		t.Fatal("unsafe existing mode accepted")
	}
}

func TestPolicyRejectsDangerousAcceptedEnvironment(t *testing.T) {
	for _, v := range []string{"*", "ENV", "BASH_ENV", "LD_PRELOAD", "LC_* ENV", "LANG *"} {
		if CheckPolicy(policy+"acceptenv "+v+"\n") == nil {
			t.Fatal(v)
		}
	}
	if e := CheckPolicy(policy + "acceptenv LANG LC_*\n"); e != nil {
		t.Fatal(e)
	}
}

func TestTrustedShellRejectsUnprotectedLinkChain(t *testing.T) {
	dir := t.TempDir()
	link := dir + "/sh"
	if e := os.Symlink("/bin/sh", link); e != nil {
		t.Fatal(e)
	}
	if trustedShell(link, 0) == nil {
		t.Fatal("untrusted link chain accepted")
	}
	if trustedShell("relative", 0) == nil || trustedShell("/bin/sh", 41) == nil {
		t.Fatal("invalid traversal accepted")
	}
}

func TestPublicationRejectsPrivilegedExistingMode(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/binary"
	data := []byte("trusted")
	check := func(string, bool) error { return nil }
	if e := installFile(path, data, 0755, check); e != nil {
		t.Fatal(e)
	}
	for _, bit := range []os.FileMode{os.ModeSetuid, os.ModeSetgid, os.ModeSticky} {
		if e := os.Chmod(path, 0755|bit); e != nil {
			t.Fatal(e)
		}
		if installFile(path, data, 0755, check) == nil {
			t.Fatal("accepted special mode", bit)
		}
	}
}
