package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
)

func public(t *testing.T) string {
	p, _, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	k, e := ssh.NewPublicKey(p)
	if e != nil {
		t.Fatal(e)
	}
	return string(ssh.MarshalAuthorizedKey(k))
}
func TestRestrictedEntry(t *testing.T) {
	p := public(t)
	var out bytes.Buffer
	if e := entry(strings.NewReader(p), &out, "192.0.2.10"); e != nil {
		t.Fatal(e)
	}
	_, _, opts, rest, e := ssh.ParseAuthorizedKey(out.Bytes())
	if e != nil || len(rest) != 0 || len(opts) != 3 || opts[0] != "restrict" || !strings.Contains(out.String(), `command="/usr/local/libexec/capacity-host-inventory"`) {
		t.Fatal(out.String(), e)
	}
}
func TestRejectInput(t *testing.T) {
	p := public(t)
	for _, s := range []string{"", "localhost", `2001:db8::1%x",command="/bin/sh`, "2001:db8::1%eth0", "2001:db8::1%x\nssh-ed25519", "0.0.0.0", "192.0.2.10\",command=\"id"} {
		if entry(strings.NewReader(p), &bytes.Buffer{}, s) == nil {
			t.Fatal(s)
		}
	}
	for _, key := range []string{p + p, "restrict " + p, "not a key", strings.Repeat("x", 16385)} {
		if entry(strings.NewReader(key), &bytes.Buffer{}, "192.0.2.10") == nil {
			t.Fatal("accepted invalid key")
		}
	}
}
