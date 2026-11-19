package main

import (
	"context"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
)

func TestPilotRejectsControlsBeforeRuntime(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"serve"}, {"bootstrap"}, {"serve-existing-readonly", "/tmp/config"}, {"serve-existing-readonly", "--listen=:9445"}, {"--help"}, {"serve-existing-readonly "}} {
		if err := run(context.Background(), args); err != managedgarm.ErrBoundary {
			t.Fatalf("accepted caller controls: %q", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if run(c, []string{"serve-existing-readonly"}) != managedgarm.ErrBoundary {
			t.Fatal("invalid context reached runtime")
		}
	}
}
