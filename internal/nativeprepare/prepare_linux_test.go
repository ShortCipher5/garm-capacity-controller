//go:build linux

package nativeprepare

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, string, Manifest) {
	t.Helper()
	source := t.TempDir()
	parent := t.TempDir()
	m := Manifest{Version, strings.Repeat("a", 64), map[string]string{}}
	for _, n := range names() {
		b := []byte("private fixture")
		if strings.HasPrefix(n, "capacity-") {
			b = fixtureELF()
		}
		if e := os.WriteFile(filepath.Join(source, n), b, 0600); e != nil {
			t.Fatal(e)
		}
		m.Files[n] = hash(b)
	}
	save(t, source, m)
	return source, parent, m
}
func save(t *testing.T, source string, m Manifest) {
	b, _ := json.Marshal(m)
	if e := os.WriteFile(filepath.Join(source, "manifest.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestPrepareFreshReadonlyRecoveryAndPermanentFailure(t *testing.T) {
	source, parent, m := fixture(t)
	uid := uint32(os.Getuid())
	r, e := prepareAt(t.Context(), source, parent, uid, false)
	if e != nil || r.ActivationAuthorized || r.State != "prepared_not_activated" {
		t.Fatal(r, e)
	}
	if _, e = prepareAt(t.Context(), source, parent, uid, false); e == nil {
		t.Fatal("reused ID")
	}
	got, e := inspectAt(t.Context(), parent, m.ID, uid)
	if e != nil || got != r {
		t.Fatal(got, e)
	}
	path := filepath.Join(parent, m.ID, "private/owner-client.key")
	if e = os.WriteFile(path, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = inspectAt(t.Context(), parent, m.ID, uid); e == nil {
		t.Fatal("accepted changed secret")
	}
	if _, e = os.Stat(filepath.Join(parent, m.ID, "garm-capacity-native-domain.service.staged")); e != nil {
		t.Fatal(e)
	}
}
func TestPrepareRejectsUnboundOrLinkedSources(t *testing.T) {
	for _, mode := range []string{"hash", "extra", "symlink", "hardlink", "secret-mode"} {
		t.Run(mode, func(t *testing.T) {
			s, p, m := fixture(t)
			name := "owner-client.key"
			path := filepath.Join(s, name)
			switch mode {
			case "hash":
				m.Files[name] = strings.Repeat("b", 64)
				save(t, s, m)
			case "extra":
				os.WriteFile(filepath.Join(s, "extra"), []byte("x"), 0600)
			case "symlink":
				os.Remove(path)
				os.Symlink(filepath.Join(s, "owner-server.key"), path)
			case "hardlink":
				os.Remove(path)
				os.Link(filepath.Join(s, "owner-server.key"), path)
			case "secret-mode":
				os.Chmod(path, 0644)
			}
			if _, e := prepareAt(t.Context(), s, p, uint32(os.Getuid()), false); e == nil {
				t.Fatal("accepted", mode)
			}
			if _, e := inspectAt(t.Context(), p, m.ID, uint32(os.Getuid())); e == nil {
				t.Fatal("partial completion")
			}
		})
	}
}
func TestConfigRolePathsExact(t *testing.T) {
	d := "/var/lib/garm-capacity-prepared/" + strings.Repeat("a", 64)
	c := map[string]any{"control": map[string]string{"server_certificate": d + "/private/owner-server.crt", "server_key": d + "/private/owner-server.key", "owner_certificate": d + "/private/owner-client.crt", "owner_key": d + "/private/owner-client.key", "controller_ca": d + "/private/controller-ca.crt"}}
	b, _ := json.Marshal(c)
	if validateConfigPaths(b, d) != nil {
		t.Fatal("exact rejected")
	}
	for k := range c["control"].(map[string]string) {
		c["control"].(map[string]string)[k] = d + "/private/controller-ca.crt"
	}
	b, _ = json.Marshal(c)
	if validateConfigPaths(b, d) == nil {
		t.Fatal("aliased roles")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Prepare(ctx, d); e == nil {
		t.Fatal("cancel accepted")
	}
}

func TestSharedReceiptContractBindsExactUnitAndReturnsOwnedNames(t *testing.T) {
	_, _, m := fixture(t)
	digest, e := ManifestDigest(m)
	if e != nil {
		t.Fatal(e)
	}
	r := Receipt{Version, m.ID, "prepared_not_activated", digest, hash(stagedUnit(filepath.Join(Parent, m.ID))), false}
	if ValidateReceipt(m, r) != nil {
		t.Fatal("valid rejected")
	}
	r.ActivationAuthorized = true
	if ValidateReceipt(m, r) == nil {
		t.Fatal("activation accepted")
	}
	r.ActivationAuthorized = false
	r.UnitDigest = strings.Repeat("f", 64)
	if ValidateReceipt(m, r) == nil {
		t.Fatal("foreign unit")
	}
	names := FileNames()
	names[0] = "other"
	if FileNames()[0] == "other" {
		t.Fatal("shared mutable allowlist")
	}
}

func fixtureELF() []byte {
	b := make([]byte, 64)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	machine := uint16(62)
	if runtime.GOARCH == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(b[18:], machine)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	return b
}
func TestCheckerChildClosesInheritedListener(t *testing.T) {
	if os.Getenv("NATIVEPREPARE_FD_TEST") == "1" {
		if sanitizeChildDescriptors() != nil {
			os.Exit(2)
		}
		// Observe the exec boundary, not premature closing of Go runtime FDs.
		if e := unix.Exec("/bin/sh", []string{"sh", "-c", "test -e /proc/self/fd/3 && test ! -e /proc/self/fd/4"}, []string{"PATH=/usr/bin:/bin"}); e != nil {
			os.Exit(3)
		}
		os.Exit(4)
	}
	held, e := os.CreateTemp(t.TempDir(), "held")
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	fd, e := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if e != nil {
		t.Fatal(e)
	}
	listener := os.NewFile(uintptr(fd), "listener")
	defer listener.Close()
	if unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}) != nil || unix.Listen(fd, 1) != nil {
		t.Fatal("listen")
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestCheckerChildClosesInheritedListener$")
	cmd.Env = []string{"NATIVEPREPARE_FD_TEST=1"}
	cmd.ExtraFiles = []*os.File{held, listener}
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("child %v %s", e, out)
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		t.Fatal("parent listener closed")
	}
}
func TestELFHeaderAndExactModes(t *testing.T) {
	if !validELF(fixtureELF()) || validELF([]byte("\x7fELFfake")) {
		t.Fatal("ELF parser")
	}
	s, p, m := fixture(t)
	if _, e := prepareAt(t.Context(), s, p, uint32(os.Getuid()), false); e != nil {
		t.Fatal(e)
	}
	os.Chmod(filepath.Join(p, m.ID, "bin", executables[0]), 0755)
	if _, e := inspectAt(t.Context(), p, m.ID, uint32(os.Getuid())); e == nil {
		t.Fatal("mutable binary mode")
	}
}

func TestPreparationNormalizesRestrictiveUmaskInChild(t *testing.T) {
	if os.Getenv("NATIVEPREPARE_UMASK_TEST") == "1" {
		s, p, m := fixture(t)
		old := unix.Umask(0777)
		defer unix.Umask(old)
		if _, e := prepareAt(t.Context(), s, p, uint32(os.Getuid()), false); e != nil {
			t.Fatal(e)
		}
		if _, e := inspectAt(t.Context(), p, m.ID, uint32(os.Getuid())); e != nil {
			t.Fatal(e)
		}
		return
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestPreparationNormalizesRestrictiveUmaskInChild$")
	cmd.Env = append(os.Environ(), "NATIVEPREPARE_UMASK_TEST=1")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("restrictive umask child %v %s", e, out)
	}
}
