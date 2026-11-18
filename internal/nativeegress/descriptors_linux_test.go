//go:build linux

package nativeegress

import (
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"testing"
)

func TestEgressToolDescriptorProbe(t *testing.T) {
	if os.Getenv("NATIVE_EGRESS_PROBE_PARENT") == "yes" {
		t.Skip("harness only")
	}
	if _, e := os.Stat("/proc/self/fd/100"); e == nil {
		t.Fatal("ambient descriptor leaked")
	}
	self, _ := os.Executable()
	want, _ := os.Stat(self)
	if got, e := os.Stat("/proc/self/fd/4"); e == nil && want != nil && os.SameFile(got, want) {
		t.Fatal("pinned executable descriptor leaked past exec")
	}
}
func TestEgressSanitizerSubprocess(t *testing.T) {
	if os.Getenv("NATIVE_EGRESS_PROBE_PARENT") == "yes" {
		if sanitizeToolDescriptors() != nil {
			os.Exit(2)
		}
		if _, e := runTool(4, []string{"-test.run=^TestEgressToolDescriptorProbe$"}, nil); e != nil {
			os.Exit(3)
		}
		return
	}
	self, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(self)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	dummy, e := os.Open("/dev/null")
	if e != nil {
		t.Fatal(e)
	}
	defer dummy.Close()
	var st unix.Stat_t
	if unix.Fstat(100, &st) == nil {
		t.Skip("reserved test descriptor in use")
	}
	if unix.Dup3(int(dummy.Fd()), 100, 0) != nil {
		t.Fatal("fixture")
	}
	defer unix.Close(100)
	c := exec.Command(self, "-test.run=^TestEgressSanitizerSubprocess$")
	c.Env = []string{"NATIVE_EGRESS_PROBE_PARENT=yes"}
	c.ExtraFiles = []*os.File{dummy, f}
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatal(string(out), e)
	}
}
