//go:build linux

package nativelaunch

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	switch os.Getenv("NATIVE_LAUNCH_TEST") {
	case "sanitize":
		os.Setenv("NATIVE_LAUNCH_TEST", "after")
		if sanitizeAndExec() != nil {
			os.Exit(111)
		}
		os.Exit(112)
	case "after":
		var s unix.Stat_t
		if unix.Fstat(100, &s) != unix.EBADF {
			fmt.Fprintln(os.Stderr, "inherited listener survived")
			os.Exit(113)
		}
		if unix.Fstat(3, &s) != nil || s.Mode&unix.S_IFMT != unix.S_IFREG {
			os.Exit(114)
		}
		fmt.Print("only-pinned-executable-survived")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestChildExecClosesInheritedNonCLOEXECListener(t *testing.T) {
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	target, e := os.Open(executable)
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	socket, e := listener.(*net.TCPListener).File()
	if e != nil {
		t.Fatal(e)
	}
	defer socket.Close()
	cmd := exec.Command(executable, "-test.run=^$")
	cmd.Env = append(os.Environ(), "NATIVE_LAUNCH_TEST=sanitize")
	// fd100 is intentionally non-CLOEXEC in the helper, just like an ambient
	// supervisor socket. The sanitizer must remove it at native exec, not merely
	// rely on os/exec's treatment of its explicitly listed descriptors.
	cmd.ExtraFiles = make([]*os.File, 98)
	cmd.ExtraFiles[0] = target
	cmd.ExtraFiles[97] = socket
	output, e := cmd.CombinedOutput()
	if e != nil || string(output) != "only-pinned-executable-survived" {
		t.Fatalf("boundary failed: %s %v", output, e)
	}
}
func TestPublicHelperRejectsUnprivilegedInvocation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root-only refusal is checked in rootless runs")
	}
	if Exec() != ErrLaunch {
		t.Fatal("unprivileged helper accepted")
	}
}
