//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativelxc"
	"golang.org/x/sys/unix"
	"strings"
	"testing"
)

func TestMappedPatchedLibrary(t *testing.T) {
	line := "7f00-7f01 r-xp 00000000 08:01 123 /" + nativelxc.DebianLibrary + "\n"
	if !mappedLibrary(line, unix.Mkdev(8, 1), 123) {
		t.Fatal("exact mapping")
	}
	for _, bad := range []string{"", strings.ReplaceAll(line, "123", "124"), strings.ReplaceAll(line, "08:01", "08:02"), line + strings.ReplaceAll(line, "1.9.0", "1.8.0"), strings.TrimSpace(line) + " (deleted)\n", strings.ReplaceAll(line, "r-xp", "r--p")} {
		if mappedLibrary(bad, unix.Mkdev(8, 1), 123) {
			t.Fatal(bad)
		}
	}
	var r Running
	if _, e := r.LXCVersion(context.Background()); e == nil {
		t.Fatal("zero capsule")
	}
}
