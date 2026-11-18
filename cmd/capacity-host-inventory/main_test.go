package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRejectCommandsAndArguments(t *testing.T) {
	for _, cmd := range []string{"", "id", "/bin/sh", "/bin/sh -s; id"} {
		if run(nil, cmd, strings.NewReader(""), &bytes.Buffer{}) == nil {
			t.Fatal(cmd)
		}
	}
	if run([]string{"/etc/passwd"}, "/bin/sh -s", strings.NewReader(""), &bytes.Buffer{}) == nil {
		t.Fatal("arguments accepted")
	}
}
func TestFixedProtocol(t *testing.T) {
	var out bytes.Buffer
	if e := run(nil, "/bin/sh -s", strings.NewReader(""), &out); e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(out.String(), "garm_inventory_v1\n") || strings.Count(out.String(), "\n") != 6 {
		t.Fatal(out.String())
	}
}

func TestDiscardsBoundedUntrustedInput(t *testing.T) {
	var out bytes.Buffer
	if e := run(nil, "/bin/sh -s", strings.NewReader("touch /tmp/never-execute; exit 7"), &out); e != nil {
		t.Fatal(e)
	}
	if e := run(nil, "/bin/sh -s", strings.NewReader(strings.Repeat("x", 8193)), &bytes.Buffer{}); e == nil {
		t.Fatal("unbounded input")
	}
}
