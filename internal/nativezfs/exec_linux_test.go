//go:build linux

package nativezfs

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"os/exec"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "zfs-test-before" {
		if cleanExec([]string{"zfs-test-after"}) != nil {
			os.Exit(111)
		}
		os.Exit(112)
	}
	if len(os.Args) == 2 && os.Args[1] == "zfs-test-after" {
		var st unix.Stat_t
		if unix.Fstat(100, &st) != unix.EBADF {
			os.Exit(113)
		}
		if unix.Fstat(3, &st) != nil || unix.Fstat(4, &st) != nil {
			os.Exit(114)
		}
		fmt.Print("only-pinned-descriptors")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestChildHelperClosesAmbientSupervisorSocket(t *testing.T) {
	path, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	binary, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer binary.Close()
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
	command := exec.Command(path, "zfs-test-before")
	command.ExtraFiles = make([]*os.File, 98)
	command.ExtraFiles[0] = binary
	command.ExtraFiles[1] = binary
	command.ExtraFiles[97] = socket
	raw, e := command.CombinedOutput()
	if e != nil || string(raw) != "only-pinned-descriptors" {
		t.Fatalf("descriptor boundary failed %q %v", raw, e)
	}
}
func TestHelperGrammarRejectsArbitraryMutation(t *testing.T) {
	for _, a := range [][]string{{"destroy", "tank/root"}, {"set", "quota=0", "tank/root"}, {"unzone", "/proc/self/fd/4", "tank/root"}, {"zone", "/proc/123/ns/user", "tank/root"}, {"create", "-p", "tank/root"}, {"version", "extra"}} {
		if validCommand(a) {
			t.Fatal(a)
		}
	}
}
func TestEngineCommandsFitClosedHelperGrammar(t *testing.T) {
	c, id, ns := testInputs(t)
	m := &mockCommands{}
	if _, e := provision(t.Context(), c, id, 42, ns, m, &mockJournal{}, 100000, 100000, func(Record) error { return nil }); e != nil {
		t.Fatal(e)
	}
	for _, a := range m.calls {
		if !validCommand(a) {
			t.Fatal("helper rejects engine", a)
		}
	}
}
