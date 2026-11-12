package main

import (
	"context"
	"testing"
)

func TestListenerMustBeExplicitLoopback(t *testing.T) {
	for _, listen := range []string{"", ":8443", "0.0.0.0:8443", "example.invalid:8443", "[::]:8443"} {
		if err := serve(context.Background(), configuration{Listen: listen}, true); err == nil {
			t.Fatal("unsafe implicit listener accepted")
		}
	}
}
