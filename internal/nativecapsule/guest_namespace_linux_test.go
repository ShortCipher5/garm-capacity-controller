//go:build linux

package nativecapsule

import (
	"context"
	"testing"
)

func TestPIDNamespaceTerminationCannotBeFabricated(t *testing.T) {
	var g GuestKernel
	if _, e := g.ObservePIDNamespaceTermination(context.Background()); e == nil {
		t.Fatal("fabricated guest")
	}
	var o PIDNamespaceTermination
	if o.Terminated() || o.NamespaceInode() != 0 {
		t.Fatal("fabricated observation")
	}
	if pidfdTerminated(-1) {
		t.Fatal("invalid descriptor")
	}
}
