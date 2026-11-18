//go:build linux

package main

import (
	"bytes"
	"testing"
)

func TestNoMutationOrConfigurationArguments(t *testing.T) {
	for _, arg := range []string{"--activate", "--fix", "--config=/tmp/a"} {
		var out bytes.Buffer
		if run([]string{arg}, &out) == nil || out.Len() != 0 {
			t.Fatal(arg)
		}
	}
}
