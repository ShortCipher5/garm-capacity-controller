//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedevice"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"testing"
)

func TestArtifactPathsCannotReplaceActualRoot(t *testing.T) {
	if _, e := StartWithArtifacts(context.Background(), Configuration{Image: "/tmp/arbitrary", Registry: "/tmp/ids", StateParent: "/tmp/state"}, nativeboundary.Observation{}, nativedevice.Boundary{}, capsulepeer.PartitionSpec{}, nativezfs.HostArtifactRoot{}); e == nil {
		t.Fatal("fake root")
	}
	var r *Running
	if _, e := r.ArtifactRoot(context.Background()); e == nil {
		t.Fatal("nil proof")
	}
}
