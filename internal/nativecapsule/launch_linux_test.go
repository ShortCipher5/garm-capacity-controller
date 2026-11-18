//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedevice"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeids"
	"os"
	"strings"
	"testing"
)

func TestExplicitNoFallbackNamespaceArguments(t *testing.T) {
	s := launchSpec{"/var/lib/images/native.raw", "/var/lib/native/birth/bootstrap.sock", strings.Repeat("a", 64), 100000000, nativeids.BlockSize}
	a, e := args(s)
	if e != nil {
		t.Fatal(e)
	}
	joined := " " + strings.Join(a, " ") + " "
	for _, required := range []string{" --settings=no ", " --register=no ", " --keep-unit ", " --private-users=100000000:16777216 ", " --private-users-ownership=map ", " --private-network ", " --read-only "} {
		if !strings.Contains(joined, required) {
			t.Fatal(required)
		}
	}
	for _, forbidden := range []string{" -U ", " --private-users=pick ", " --network-veth ", " --boot ", " --bind=/ ", " --share-system "} {
		if strings.Contains(joined, forbidden) {
			t.Fatal(forbidden)
		}
	}
}
func TestRejectUnsafeConfigBeforeHostEffects(t *testing.T) {
	if _, e := Start(context.Background(), Configuration{}, nativeboundary.Observation{}, nativedevice.Boundary{}); e == nil {
		t.Fatal("zero authority")
	}
	for _, path := range []string{"relative", "/x:host", "/a/../b", "/x\ny"} {
		if _, e := args(launchSpec{path, "/valid", strings.Repeat("a", 64), 100000000, nativeids.BlockSize}); e == nil {
			t.Fatal(path)
		}
	}
	if os.Geteuid() != 0 && ExecLauncher() == nil {
		t.Fatal("unprivileged helper")
	}
}
