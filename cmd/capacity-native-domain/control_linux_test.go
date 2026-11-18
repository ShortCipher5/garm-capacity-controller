//go:build linux

package main

import (
	"io"
	"testing"
)

func TestControlCannotRunWithoutBoundaryConfiguration(t *testing.T) {
	if run([]string{"--domain-dir=/var/lib/native/new", "--incusd=/usr/libexec/incus/incusd", "--launcher=/usr/libexec/native-launch", "--control-config=/etc/native/control.json"}, io.Discard) == nil {
		t.Fatal("unbounded control")
	}
}
func TestControlSecretPathRefusesRelativeData(t *testing.T) {
	if _, e := rootData("relative", true, 1<<20); e == nil {
		t.Fatal("relative key")
	}
	if serveConfiguredOwner(t.Context(), "/missing", nil, nil) == nil {
		t.Fatal("no birth proof")
	}
}

func TestControlActivationRefusesBeforeAnyHostEffects(t *testing.T) {
	err := run([]string{"--domain-dir=/nonexistent/native/new", "--incusd=/nonexistent/incusd", "--launcher=/nonexistent/launcher", "--boundary-config=/nonexistent/boundary.json", "--control-config=/nonexistent/control.json"}, io.Discard)
	if err != errConfinement {
		t.Fatalf("must refuse activation before reading files or launching: %v", err)
	}
}
