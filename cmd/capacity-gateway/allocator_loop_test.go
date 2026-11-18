package main

import (
	"context"
	"testing"
)

func TestAllocatorStartupCannotBeEnabledByConfiguration(t *testing.T) {
	for _, v := range []allocatorLoopConfiguration{{0, 0}, {59, 60}, {60, 3601}, {3601, 60}} {
		if validateAllocatorLoop(&v) == nil {
			t.Fatal("unbounded config accepted")
		}
	}
	c := &allocatorLoopConfiguration{60, 300}
	if validateAllocatorLoop(c) != nil {
		t.Fatal("bounded config refused")
	}
	if _, _, err := startAllocatorLoop(context.Background(), c, nil); err == nil {
		t.Fatal("configuration minted runtime store")
	}
	service, close, err := startAllocatorLoop(context.Background(), nil, nil)
	if err != nil || service != nil {
		t.Fatal("absent config became active")
	}
	close()
}
