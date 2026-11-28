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

func TestHeadroomRequiresExactCommandAndEmptyInput(t *testing.T) {
	args := []string{"-c", "garm-inventory-v1"}
	for _, cmd := range []string{"garm-host-headroom-v1 --path /", "garm-host-headroom-v1; id"} {
		if run(args, cmd, strings.NewReader(""), &bytes.Buffer{}) == nil {
			t.Fatal("accepted modified command")
		}
	}
	if run(args, "garm-host-headroom-v1", strings.NewReader("path=/"), &bytes.Buffer{}) == nil {
		t.Fatal("accepted input")
	}
	var out bytes.Buffer
	if err := run(args, "garm-host-headroom-v1", strings.NewReader(""), &out); err != nil || !strings.HasPrefix(out.String(), "garm-host-headroom-v1\n") {
		t.Fatal(err)
	}
}
