//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"testing"
)

func TestGuestPIDMapping(t *testing.T) {
	parent := []int{100, 1}
	for _, tc := range []struct {
		p  []int
		ok bool
	}{{[]int{200, 25, 1}, true}, {[]int{200, 25}, false}, {[]int{200, 26, 1}, false}, {[]int{200, 25, 2}, false}, {[]int{201, 25, 1}, false}, {[]int{200, 25, 3, 1}, false}} {
		if guestPIDMatch(parent, tc.p, 25, 200) != tc.ok {
			t.Fatal(tc)
		}
	}
	if _, e := namespacePIDs([]byte("NSpid:\t1\nNSpid:\t1\n")); e == nil {
		t.Fatal("duplicate")
	}
}
func TestGuestCgroupScope(t *testing.T) {
	for _, s := range []string{"0::/\n", "0::/capsule-birth\n", "0::/capsule-birth-other/guest\n", "0::/capsule-birth/../other\n", "0::/capsule-birth/x\n0::/bad\n"} {
		if _, e := guestCgroup([]byte(s), "birth"); e == nil {
			t.Fatal(s)
		}
	}
	if _, e := guestCgroup([]byte("0::/capsule-birth/lxc/guest\n"), "birth"); e != nil {
		t.Fatal(e)
	}
}
func TestGuestEmptyAndFabricated(t *testing.T) {
	for _, s := range []string{"", "populated 1\n", "populated 0\npopulated 0\n", "populated 0 extra\n"} {
		if cgroupUnpopulated([]byte(s)) {
			t.Fatal(s)
		}
	}
	if !cgroupUnpopulated([]byte("populated 0\nfrozen 0\n")) {
		t.Fatal("empty")
	}
	var g GuestKernel
	g.Close()
	if _, e := g.ObserveExit(context.Background()); e == nil {
		t.Fatal("zero guest")
	}
	var r *Running
	if _, e := r.ObserveGuestPID(context.Background(), 25, "runner", nativeboundary.Limits{}); e == nil {
		t.Fatal("zero running")
	}
	var out GuestExit
	if out.PID1Exited() || out.CgroupEmpty() {
		t.Fatal("zero observation")
	}
}

func TestGuestPlacementRequiresExactCompiledSibling(t *testing.T) {
	if !exactGuestPath("capsule-birth/guests/runner/payload", "birth", "runner") {
		t.Fatal("exact")
	}
	for _, p := range []string{"capsule-birth/init.scope/runner/payload", "capsule-birth/bootstrap/payload", "capsule-birth/monitors/runner", "capsule-birth/guests/other/payload", "capsule-other/guests/runner/payload", "capsule-birth/guests/runner/payload/child"} {
		if exactGuestPath(p, "birth", "runner") {
			t.Fatal(p)
		}
	}
	for _, n := range []string{"", "../runner", "a/b", "-runner", "runner\n"} {
		if guestName(n) {
			t.Fatal(n)
		}
	}
}
