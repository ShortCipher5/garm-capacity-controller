package garmapi

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
	"testing"
)

func TestManagedLocalCannotBeEnabledByUnobservedSession(t *testing.T) {
	for _, s := range []*managedgarm.Session{nil, {}} {
		if _, e := NewManagedLocal(s, []string{"capacity"}, []string{"11111111-1111-4111-8111-111111111111"}, nil); e == nil {
			t.Fatal("unobserved session accepted")
		}
	}
}
