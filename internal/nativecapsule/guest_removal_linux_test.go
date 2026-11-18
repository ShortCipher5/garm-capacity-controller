//go:build linux

package nativecapsule

import (
	"context"
	"golang.org/x/sys/unix"
	"testing"
)

func TestRemovalRejectsFabricatedAndAmbiguousPaths(t *testing.T) {
	var g GuestKernel
	if _, e := g.ObserveCgroupRemoval(context.Background()); e == nil {
		t.Fatal("fabricated")
	}
	var o GuestCgroupRemoval
	if o.Removed() {
		t.Fatal("fabricated observation")
	}
	for _, p := range []string{"", "/tmp/guest", "/sys/fs/cgroup/capsule-test/a (deleted)"} {
		if removedHeldCgroup(-1, p) {
			t.Fatal(p)
		}
	}
}

func TestRemovalRequiresUnlinkedAndInactiveCoreInterface(t *testing.T) {
	p := "/sys/fs/cgroup/capsule-birth/guest"
	for _, tc := range []struct {
		link, after string
		err         error
		ok          bool
	}{
		{p + " (deleted)", p + " (deleted)", unix.ENOENT, true},
		{p + " (deleted)", p + " (deleted)", unix.ENODEV, true},
		{p + " (deleted)", p + " (deleted)", nil, false},
		{p + " (deleted)", p + " (deleted)", unix.EACCES, false},
		{p + " (deleted)", p + " (deleted)", unix.EIO, false},
		{p, p, unix.ENOENT, false},
		{p + " (deleted)", p, unix.ENOENT, false},
		{p + "-renamed (deleted)", p + "-renamed (deleted)", unix.ENOENT, false},
	} {
		if removedCgroupEvidence(p, tc.link, tc.err, tc.after) != tc.ok {
			t.Fatal(tc)
		}
	}
}
