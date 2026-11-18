package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRefreshOverheadRejectsMissingPrivateInputs(t *testing.T) {
	for _, args := range [][]string{{"refresh-overhead"}, {"refresh-overhead", "--source", "private-do-not-log"}, {"refresh-overhead", "--source", "private-do-not-log", "--identity-file", "also-private", "--config", "missing-private"}} {
		var out, err bytes.Buffer
		if run(context.Background(), args, &out, &err) == 0 {
			t.Fatal("invalid inputs accepted")
		}
		if strings.Contains(err.String(), "private-do-not-log") || strings.Contains(err.String(), "missing-private") {
			t.Fatal("private path leaked")
		}
	}
}
