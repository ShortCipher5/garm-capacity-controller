package main

import (
	"bytes"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratePrivateAndPublicKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key")
	var out bytes.Buffer
	if e := generate(p, &out); e != nil {
		t.Fatal(e)
	}
	i, e := os.Stat(p)
	if e != nil || i.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", i, e)
	}
	data, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	s, e := ssh.ParsePrivateKey(data)
	if e != nil {
		t.Fatal(e)
	}
	key, _, _, _, e := ssh.ParseAuthorizedKey(out.Bytes())
	if e != nil || !bytes.Equal(key.Marshal(), s.PublicKey().Marshal()) {
		t.Fatal("public/private mismatch", e)
	}
	if e = generate(p, &out); e == nil {
		t.Fatal("overwrote existing key")
	}
	again, _ := os.ReadFile(p)
	if !bytes.Equal(data, again) {
		t.Fatal("changed existing key")
	}
}
func TestRejectSymlink(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "link")
	if e := os.Symlink(filepath.Join(d, "target"), p); e != nil {
		t.Fatal(e)
	}
	if e := generate(p, &bytes.Buffer{}); e == nil {
		t.Fatal("followed symlink")
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, os.ErrClosed }
func TestOutputFailureRetainsPrivateKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key")
	if e := generate(p, failedOutput{}); e == nil {
		t.Fatal("expected output failure")
	}
	data, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ssh.ParsePrivateKey(data); e != nil {
		t.Fatal(e)
	}
}
