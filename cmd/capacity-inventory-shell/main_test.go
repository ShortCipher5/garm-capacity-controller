package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestShellNeverInterpretsCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"-c", "id"}, {"-c", "garm-inventory-v1;id"}, {"-c", "garm-inventory-v1", "extra"}} {
		if run(args, "/bin/sh -s", strings.NewReader(""), &bytes.Buffer{}) == nil {
			t.Fatal(args)
		}
	}
	if run([]string{"-c", "garm-inventory-v1"}, "id", strings.NewReader(""), &bytes.Buffer{}) == nil {
		t.Fatal("original command escaped")
	}
	var out bytes.Buffer
	if e := run([]string{"-c", "garm-inventory-v1"}, "/bin/sh -s", strings.NewReader("touch /never-executed"), &out); e != nil || !strings.HasPrefix(out.String(), "garm_inventory_v1\n") {
		t.Fatal(e, out.String())
	}
}
