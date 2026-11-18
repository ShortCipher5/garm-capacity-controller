//go:build linux

package nativedomain

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestConsumeDurableAndNeverAdopts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	birth, err := consume(dir, strings.Repeat("a", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "birth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded Birth
	if err = json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != birth || birth.Version != 1 || len(birth.ID) != 64 || birth.LaunchConsumedAt.IsZero() {
		t.Fatal("missing durable identity")
	}
	for _, path := range []string{dir, filepath.Join(dir, "incus")} {
		i, e := os.Stat(path)
		if e != nil || i.Mode().Perm() != 0700 {
			t.Fatal("unsafe directory", path, e)
		}
	}
	i, err := os.Stat(filepath.Join(dir, "birth.json"))
	if err != nil || i.Mode().Perm() != 0600 {
		t.Fatal("unsafe birth mode", err)
	}
	if _, err = consume(dir, strings.Repeat("b", 64), strings.Repeat("c", 64)); !errors.Is(err, ErrDomain) {
		t.Fatal("reused consumed domain", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "birth.json"))
	if string(after) != string(data) {
		t.Fatal("rewrote birth")
	}
}
func TestConsumeRefusesEvenEmptyOrSymlinkDomain(t *testing.T) {
	parent := t.TempDir()
	empty := filepath.Join(parent, "empty")
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"empty", "link"} {
		path := filepath.Join(parent, name)
		if name == "link" {
			if err := os.Symlink(empty, path); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := consume(path, strings.Repeat("a", 64), strings.Repeat("c", 64)); !errors.Is(err, ErrDomain) {
			t.Fatal("adopted inherited domain", err)
		}
	}
}
func TestBirthIDsNeverReused(t *testing.T) {
	parent := t.TempDir()
	a, e := consume(filepath.Join(parent, "a"), "digest", "helper")
	if e != nil {
		t.Fatal(e)
	}
	b, e := consume(filepath.Join(parent, "b"), "digest", "helper")
	if e != nil {
		t.Fatal(e)
	}
	if a.ID == b.ID {
		t.Fatal("duplicate identity")
	}
}
func TestStartFreshRejectsBeforeMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(t.TempDir(), "fresh")
	if _, err := StartFresh(ctx, dir, "/does-not-exist", "/does-not-exist"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("cancelled launch mutated disk")
	}
	// Even when tests run as root, /tmp's writable ancestor must fail before
	// execution. This test never starts any actual daemon or infrastructure.
	if _, err := StartFresh(context.Background(), dir, "/does-not-exist", "/does-not-exist"); !errors.Is(err, ErrDomain) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("invalid launch mutated disk")
	}
}
func TestSecurePathRejectsWritableAndSymlinkAncestors(t *testing.T) {
	parent := t.TempDir()
	if err := securePath(parent, true); !errors.Is(err, ErrDomain) {
		t.Fatal("accepted writable ancestor", err)
	}
	if err := securePath("relative", true); !errors.Is(err, ErrDomain) {
		t.Fatal(err)
	}
	if err := securePath("/tmp/../tmp", true); !errors.Is(err, ErrDomain) {
		t.Fatal(err)
	}
}
func TestTransportCannotBeManufacturedFromZeroBirth(t *testing.T) {
	var d Domain
	for _, url := range []string{"http://native.invalid/1.0", "http://other.invalid/1.0", "https://native.invalid/1.0", "http://user@native.invalid/1.0"} {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if _, err := d.RoundTrip(req); !errors.Is(err, ErrDomain) {
			t.Fatal("zero domain usable", err)
		}
	}
	if _, err := d.dial(context.Background(), "unix", "ignored"); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	d.Close()
}
func TestProcessStartAndDeadPeer(t *testing.T) {
	stamp, err := processStart(os.Getpid())
	if err != nil || stamp == "" {
		t.Fatal(err)
	}
	d := Domain{pid: os.Getpid(), startTime: stamp, directory: t.TempDir()}
	d.dead.Store(true)
	if _, err = d.dial(context.Background(), "tcp", "native.invalid:80"); !errors.Is(err, ErrUnknown) {
		t.Fatal("dead owner accepted", err)
	}
	d.dead.Store(false)
	d.startTime = "0"
	if _, err = d.dial(context.Background(), "tcp", "native.invalid:80"); !errors.Is(err, ErrUnknown) {
		t.Fatal("incarnation mismatch accepted", err)
	}
}

func TestBirthPidfdRemainsDeadAfterReap(t *testing.T) {
	cmd := exec.Command("/bin/true")
	fd := -1
	cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &fd}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if fd < 0 {
		_ = cmd.Wait()
		t.Fatal("birth pidfd unavailable")
	}
	defer unix.Close(fd)
	// A process that exits immediately is still our unreaped child. Capture its
	// observation BEFORE Wait; then prove the immutable birth handle cannot be
	// treated as live following reap, regardless of any later numeric PID reuse.
	if _, err := processStart(cmd.Process.Pid); err != nil {
		_ = cmd.Wait()
		t.Fatal(err)
	}
	_ = cmd.Wait()
	d := Domain{pid: cmd.Process.Pid, pidfd: fd, startTime: "1"}
	if d.alive() {
		t.Fatal("reaped child remained live")
	}
	if _, err := d.dial(context.Background(), "tcp", "native.invalid:80"); !errors.Is(err, ErrUnknown) {
		t.Fatal("reaped birth authorized peer", err)
	}
}
