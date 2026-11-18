//go:build linux

package nativeboundary

import (
	"strings"
	"testing"
)

func TestRequireActualPrivateNSDelegateMount(t *testing.T) {
	good := "25 24 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec - cgroup2 cgroup rw,nsdelegate\n"
	if !privateMount(good) {
		t.Fatal("valid")
	}
	for _, bad := range []string{strings.Replace(good, ",nsdelegate", "", 1), strings.Replace(good, " / /sys", " /system.slice /sys", 1), strings.Replace(good, " - cgroup2", " shared:1 - cgroup2", 1), strings.Replace(good, "cgroup2", "tmpfs", 1), good + good} {
		if privateMount(bad) {
			t.Fatal("accepted", bad)
		}
	}
}
func TestFiniteKernelLimits(t *testing.T) {
	want := Limits{1 << 30, 2000, 100}
	base := func() map[string]string {
		return map[string]string{"memory.max": "1073741824", "memory.swap.max": "0", "memory.oom.group": "0", "cpu.max": "200000 100000", "pids.max": "100"}
	}
	actual, e := parseLimits(base(), want)
	if e != nil || actual != want {
		t.Fatal(actual, e)
	}
	for k, v := range map[string]string{"memory.max": "max", "memory.swap.max": "1", "memory.oom.group": "1", "cpu.max": "max 100000", "pids.max": "101"} {
		m := base()
		m[k] = v
		if _, e := parseLimits(m, want); e == nil {
			t.Fatal(k)
		}
	}
	m := base()
	m["cpu.max"] = "200001 100000"
	if _, e := parseLimits(m, want); e == nil {
		t.Fatal("rounded down CPU")
	}
}
func TestZeroObservationNotAuthority(t *testing.T) {
	if (Observation{}).Valid() {
		t.Fatal("zero")
	}
	if _, e := Observe(Limits{}); e == nil {
		t.Fatal("zero accepted")
	}
}
