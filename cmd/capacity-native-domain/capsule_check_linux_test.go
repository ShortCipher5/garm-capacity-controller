//go:build linux

package main

import (
	"bytes"
	"testing"
)

func TestCapsuleInspectionIsExplicitAndNeverStarts(t *testing.T) {
	for _, args := range [][]string{
		{"--check-capsule-config"},
		{"--check-capsule-config", "--capsule-config=relative"},
		{"--check-capsule-config", "--capsule-config=/missing", "--domain-dir=/old"},
		{"--check-capsule-config", "--capsule-config=/missing", "--render-bounded-unit"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("invalid inspection accepted: %v %s", err, out.String())
		}
	}
	var out bytes.Buffer
	if err := run([]string{"--capsule-config=/missing"}, &out); err != errConfinement || out.Len() != 0 {
		t.Fatal("normal path lost early refusal", err)
	}
}
