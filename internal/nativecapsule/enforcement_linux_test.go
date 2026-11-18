//go:build linux

package nativecapsule

import (
	"context"
	"testing"
)

func TestEnforcementCannotComeFromZeroRunning(t *testing.T) {
	for _, r := range []*Running{nil, {}} {
		d, e := r.EnforcementSnapshot(context.Background())
		if e == nil || d.CapsuleBirthID != "" {
			t.Fatal("unconstructed enforcement")
		}
	}
}
