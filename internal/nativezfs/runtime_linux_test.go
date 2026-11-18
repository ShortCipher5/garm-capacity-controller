//go:build linux

package nativezfs

import (
	"context"
	"strings"
	"testing"
)

func TestFreshMountRequiresExactKernelIdentity(t *testing.T) {
	path := "/private/birth/runtime-mount"
	source := "tank/root/domain-a/runtime"
	base := "10 1 0:4 / / rw - ext4 /dev/root rw\n"
	line := "20 10 0:8 / " + path + " rw,nosuid,nodev,noexec - zfs " + source + " rw\n"
	if !noMount([]byte(base), path) || !exactMount([]byte(base+line), path, source, 20) {
		t.Fatal("fixture")
	}
	for _, bad := range []string{base + line + line, base + strings.Replace(line, "20 10", "21 10", 1), base + strings.Replace(line, source, "tank/foreign", 1), base + strings.Replace(line, "/ "+path, "/sub "+path, 1), base + strings.Replace(line, " - zfs ", " - ext4 ", 1), base + strings.Replace(line, ",noexec", "", 1), base + line + strings.Replace(line, path, path+"/nested", 1)} {
		if exactMount([]byte(bad), path, source, 20) {
			t.Fatal("unsafe mount", bad)
		}
	}
	if noMount([]byte(base+line), path) {
		t.Fatal("existing mount")
	}
	if noMount([]byte(base+strings.Replace(line, path, path+"/nested", 1)), path) {
		t.Fatal("nested mount")
	}
}
func TestInitializationFailureCannotDelegate(t *testing.T) {
	c, id, ns := testInputs(t)
	m := &mockCommands{}
	j := &mockJournal{}
	called := false
	_, e := provision(context.Background(), c, id, 42, ns, m, j, 100000, 200000, func(r Record) error {
		called = true
		if r.MappedUID != 100000 || r.MappedGID != 200000 || r.RuntimeGUID != "3" {
			t.Fatal("wrong identity")
		}
		return ErrUnknown
	})
	if e != ErrUnknown || !called || !j.consumed {
		t.Fatal(e)
	}
	for _, call := range m.calls {
		if call[0] == "zone" || call[0] == "set" {
			t.Fatal("delegated after initialization failure")
		}
	}
}
