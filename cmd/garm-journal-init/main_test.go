package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

func TestInitIsExplicitNewOnlyAndDoesNotAdoptOrOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "management.db")
	var out, stderr bytes.Buffer
	if code := run([]string{"-journal", path}, &out, &stderr); code != 0 {
		t.Fatalf("new init failed %d: %s", code, stderr.String())
	}
	var result struct {
		Lineage string `json:"journal_lineage"`
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Lineage) != 64 {
		t.Fatal("missing explicit lineage result")
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"-journal", path}, &out, &stderr); code == 0 || out.Len() != 0 {
		t.Fatal("existing journal silently adopted or initialized again")
	}
	journal, err := garmmanagement.OpenJournal(path, result.Lineage)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if journal.Lineage() != result.Lineage {
		t.Fatal("repeat init changed journal identity")
	}
	empty := filepath.Join(dir, "preexisting-empty.db")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-journal", empty}, &out, &stderr); code == 0 {
		t.Fatal("preexisting empty state adopted")
	}
	info, err := os.Stat(empty)
	if err != nil || info.Size() != 0 {
		t.Fatal("preexisting empty file mutated")
	}
}
func TestInitRequiresPrivateParentAndNeverChangesItsPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"-journal", filepath.Join(dir, "journal.db")}, &out, &stderr); code == 0 {
		t.Fatal("public parent accepted")
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("CLI silently changed user directory permissions")
	}
	if strings.Contains(stderr.String(), "journal_lineage") {
		t.Fatal("failure emitted a usable lineage")
	}
}

func TestInitArgumentFailuresDoNotEchoPrivateInput(t *testing.T) {
	for _, args := range [][]string{{"-unexpected-private-sentinel"}, {"-journal", "/private-sentinel/path", "extra-private-sentinel"}, {"-journal"}} {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 2 {
			t.Fatal("invalid arguments accepted")
		}
		if strings.Contains(out.String()+stderr.String(), "private-sentinel") {
			t.Fatal("private argument echoed")
		}
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"-h"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "Usage:") {
		t.Fatal("safe help unavailable")
	}
}
