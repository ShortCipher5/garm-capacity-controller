package managedgarm

import (
	"context"
	"testing"
)

func TestStockReadQualificationInvalidContextNoIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if RunStockReadQualification(c) != ErrBoundary {
			t.Fatal("invalid qualification context accepted")
		}
	}
}
