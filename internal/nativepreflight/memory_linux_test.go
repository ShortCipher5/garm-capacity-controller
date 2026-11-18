//go:build linux

package nativepreflight

import (
	"math"
	"testing"
)

func TestMemoryPlanNeverAuthorizes(t *testing.T) {
	for _, v := range [][3]uint64{{1024, 128, 512}, {1024, 512, 512}, {1024, 513, 512}, {1024, 2048, 1}} {
		r, e := evaluateMemoryPlan(v[0], v[1], v[2])
		if e != nil {
			t.Fatal(e)
		}
		if r.ActivationAuthorized || r.ReleaseAuthorized {
			t.Fatal("configuration became authority")
		}
		want := v[1] <= v[0] && v[2] <= v[0]-v[1]
		if r.FitsPhysicalTotal != want {
			t.Fatal(r)
		}
		if !want && r.State != "blocked_physical_overcommit" {
			t.Fatal(r)
		}
	}
}
func TestMemoryPlanRejectsInvalidBounds(t *testing.T) {
	for _, v := range [][3]uint64{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}, {math.MaxUint64, 1, 1}, {1, math.MaxUint64, 1}, {1, 1, math.MaxUint64}} {
		if _, e := evaluateMemoryPlan(v[0], v[1], v[2]); e == nil {
			t.Fatal(v)
		}
	}
}
func TestParseMemoryTotal(t *testing.T) {
	n, e := parseMemoryTotal("MemTotal: 123 kB\nMemFree: 1 kB\n")
	if e != nil || n != 123*1024 {
		t.Fatal(n, e)
	}
	for _, s := range []string{"", "MemTotal: 1 bytes", "MemTotal: 1 kB\nMemTotal: 2 kB", "MemTotal: -1 kB", "MemTotal: 01 kB", "MemTotal: 0 kB", "MemTotal: 18446744073709551615 kB", "MemTotal: 1 kB extra"} {
		if _, e := parseMemoryTotal(s); e == nil {
			t.Fatal(s)
		}
	}
}
