//go:build linux

package capsulepeer

import (
	"strings"
	"testing"
)

func TestManagedCgroupMountRequiresBoundedWritablePrivateRoot(t *testing.T) {
	good := "10 9 0:1 / /sys/fs/cgroup rw,nosuid,nodev,noexec - cgroup2 cgroup2 rw,nsdelegate"
	if !managedCgroupMount(good) {
		t.Fatal("fixture")
	}
	for _, bad := range []string{strings.Replace(good, " / /sys", " /host /sys", 1), strings.Replace(good, " rw,nosuid", " ro,nosuid", 1), strings.Replace(good, " - cgroup2", " shared:2 - cgroup2", 1), strings.Replace(good, ",nsdelegate", "", 1), good + "\n" + good} {
		if managedCgroupMount(bad) {
			t.Fatal(bad)
		}
	}
	if (*Peer)(nil).CompleteNativePlacement(strings.Repeat("a", 64)) {
		t.Fatal("nil placement")
	}
}
