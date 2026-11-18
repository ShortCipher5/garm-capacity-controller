//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecapsule"
	"testing"
)

func TestCapsuleConstructorRejectsFabricatedAuthority(t *testing.T) {
	p := resourcePolicy(t, nil)
	for _, r := range []*nativecapsule.Running{nil, {}, new(nativecapsule.Running)} {
		if c, e := NewCapsule(context.Background(), r, p); e == nil || c != nil {
			t.Fatal("fabricated capsule accepted")
		}
	}
}
func TestCapsuleDoesNotOpenAllocationBoundary(t *testing.T) {
	c := &Client{capsule: new(nativecapsule.Running)}
	if c.CheckBoundary(context.Background()) == nil {
		t.Fatal("unqualified capsule granted authority")
	}
}
