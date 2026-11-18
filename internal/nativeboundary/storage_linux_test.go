//go:build linux

package nativeboundary

import (
	"strings"
	"testing"
)

func TestExactDatasetMountNoNestedBypass(t *testing.T) {
	good := "25 24 0:26 / /var/lib/native rw - zfs tank/native rw\n"
	if !datasetMount(good, "/var/lib/native", "tank/native") {
		t.Fatal("valid")
	}
	for _, bad := range []string{strings.Replace(good, "tank/native", "tank/other", 1), strings.Replace(good, " - zfs", " - ext4", 1), strings.Replace(good, "/ /var", "/sub /var", 1), good + good, good + "26 25 0:27 / /var/lib/native/cache rw - tmpfs tmpfs rw\n"} {
		if datasetMount(bad, "/var/lib/native", "tank/native") {
			t.Fatal("accepted", bad)
		}
	}
}
func TestNoStorageObservationFromInputsAlone(t *testing.T) {
	if (StorageObservation{}).Valid() {
		t.Fatal("zero")
	}
	if _, e := ObserveStorage(t.Context(), "/var/lib/native", "/var/lib/native/new", "tank/native", 1, nil); e == nil {
		t.Fatal("nil reader")
	}
}
func TestActualReadonlyHostMountsRequired(t *testing.T) {
	good := "1 0 8:1 / / ro - ext4 root ro\n2 1 0:2 / /proc rw - proc proc rw\n3 1 0:3 / /sys rw - sysfs sysfs rw\n4 1 0:4 / /dev rw - tmpfs tmpfs rw\n5 1 0:5 / /var/lib/native rw - zfs tank/native rw\n"
	if !confinedMounts(good, "/var/lib/native") {
		t.Fatal("valid")
	}
	if confinedMounts(strings.Replace(good, "/ / ro", "/ / rw", 1), "/var/lib/native") {
		t.Fatal("root writable")
	}
	if confinedMounts(good+"6 1 0:6 / /var/log rw - tmpfs logs rw\n", "/var/lib/native") {
		t.Fatal("unbounded logs")
	}
}
func TestRuntimeMountsCannotBypassAncestorDiskBudget(t *testing.T) {
	root := "1 0 0:1 / /var/lib/native rw - zfs tank/native rw\n"
	for _, child := range []string{"2 1 0:2 / /var/lib/native/guest rw - zfs tank/native/containers/guest rw\n", "2 1 0:2 / /var/lib/native/tmp rw - tmpfs tmpfs rw\n", "2 1 0:2 / /var/lib/native/view ro - ext4 other ro\n"} {
		if !runtimeDatasetMount(root+child, "/var/lib/native", "tank/native") {
			t.Fatal(child)
		}
	}
	for _, child := range []string{"2 1 0:2 / /var/lib/native/guest rw - zfs tank/other rw\n", "2 1 0:2 / /var/lib/native/view rw - ext4 host rw\n"} {
		if runtimeDatasetMount(root+child, "/var/lib/native", "tank/native") {
			t.Fatal("unbounded", child)
		}
	}
}
