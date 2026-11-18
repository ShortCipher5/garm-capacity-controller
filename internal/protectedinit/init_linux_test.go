//go:build linux

package protectedinit

import (
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRefusesOutsidePID1(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("test process PID1")
	}
	if Run(Config{}) == nil {
		t.Fatal("unqualified process accepted")
	}
}
func TestPrivateJournalPublicationAndCrashResidue(t *testing.T) {
	d := t.TempDir()
	r := Record{Domain: strings.Repeat("a", 64), Incarnation: strings.Repeat("b", 64), State: "launch_unknown", ExitCode: -1}
	if e := writeRecord(d, r); e != nil {
		t.Fatal(e)
	}
	var got Record
	b, e := os.ReadFile(filepath.Join(d, "state.json"))
	if e != nil || json.Unmarshal(b, &got) != nil || got.JobCompletionAuthorized || got.State != "launch_unknown" {
		t.Fatal("bad journal")
	}
	if e = os.WriteFile(filepath.Join(d, "next"), []byte("partial"), 0600); e != nil {
		t.Fatal(e)
	}
	r.State = "process_tree_exited_unqualified"
	if writeRecord(d, r) == nil {
		t.Fatal("crash residue overwritten")
	}
	b, _ = os.ReadFile(filepath.Join(d, "state.json"))
	json.Unmarshal(b, &got)
	if got.State != "launch_unknown" {
		t.Fatal("lost unknown")
	}
}
func TestRejectMutableAndSymlinkArtifactPath(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "launcher")
	os.WriteFile(p, []byte("bad"), 0700)
	if f, e := rootFile(p, true); e == nil {
		f.Close()
		t.Fatal("world writable ancestor accepted")
	}
	link := filepath.Join(d, "link")
	os.Symlink("/bin/true", link)
	if f, e := rootFile(link, true); e == nil {
		f.Close()
		t.Fatal("symlink accepted")
	}
}
func TestReaperWaitsForOrphanedDescendant(t *testing.T) {
	c := exec.Command(os.Args[0], "-test.run=^TestReapHelper$")
	c.Env = append(os.Environ(), "PROTECTED_REAP_HELPER=1")
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%s %v", b, e)
	}
}
func TestReapHelper(t *testing.T) {
	if os.Getenv("PROTECTED_REAP_HELPER") != "1" {
		return
	}
	// Test-only subreaper models PID1 orphan adoption without claiming real guest
	// PID namespaces/cgroups qualified. No flag exposes this alternate production mode.
	if e := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	p, e := os.StartProcess("/bin/sh", []string{"sh", "-c", "(sleep 0.2) & exit 7"}, &os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}, Sys: &syscall.SysProcAttr{Setsid: true}})
	if e != nil {
		t.Fatal(e)
	}
	id := p.Pid
	p.Release()
	code, e := reap(id)
	if e != nil || code != 7 || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("reaped early or wrong: %d %v", code, e)
	}
}
