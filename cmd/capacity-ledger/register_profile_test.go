package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterProfileValidatesWithoutEchoingSecretsOrActivation(t *testing.T) {
	for _, args := range [][]string{
		{"register-pool-profile"},
		{"register-pool-profile", "--source", "private-source", "--identity-file", "private-key", "--config", "private-secret", "--activate"},
	} {
		var out, diagnostic bytes.Buffer
		if run(context.Background(), args, &out, &diagnostic) != 2 || strings.Contains(diagnostic.String(), "private-") {
			t.Fatal("invalid arguments accepted or disclosed", diagnostic.String())
		}
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "registration")
	os.WriteFile(path, []byte("{\"endpoint\":\"secret-endpoint\",\"unknown\":\"secret-token\"}"), 0600)
	var out, diagnostic bytes.Buffer
	code := run(context.Background(), []string{"register-pool-profile", "--source", filepath.Join(dir, "ledger"), "--identity-file", filepath.Join(dir, "identity"), "--config", path}, &out, &diagnostic)
	if code != 1 || strings.Contains(diagnostic.String(), "secret-") || out.Len() != 0 {
		t.Fatal("invalid config disclosure", diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "ledger")); !os.IsNotExist(err) {
		t.Fatal("invalid configuration created a ledger")
	}
}
