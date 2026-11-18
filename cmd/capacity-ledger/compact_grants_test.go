package main

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactGrantsCLIRequiresExistingIndependentIdentity(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "not-created.db")
	identity := filepath.Join(dir, "id")
	for _, args := range [][]string{{"compact-grants", "--source", path}, {"compact-grants", "--source", path, "--identity-file", identity, "--limit", "0"}, {"compact-grants", "--source", path, "--identity-file", identity}} {
		code, _, _ := invoke(context.Background(), args...)
		if code == 0 {
			t.Fatal("invalid accepted")
		}
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("missing source created", e)
	}
}
func TestCompactGrantsCLILeavesReadinessBlocked(t *testing.T) {
	_, fixtureStore := fixture(t)
	state, e := fixtureStore.ReadSnapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	dir := privateDir(t)
	path, identity := filepath.Join(dir, "runtime.db"), filepath.Join(dir, "identity")
	if e := admission.InitializeSQLite(path, identity, state.Config); e != nil {
		t.Fatal(e)
	}
	code, out, diagnostic := invoke(context.Background(), "compact-grants", "--source", path, "--identity-file", identity, "--limit", "1")
	if code != 0 || diagnostic != "" || !strings.Contains(out, "Compacted 0") {
		t.Fatalf("%d %s %s", code, out, diagnostic)
	}
	store, e := admission.OpenRuntimeSQLite(path, identity)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	snapshot, e := store.ReadSnapshot(context.Background())
	if e != nil || snapshot.Ready {
		t.Fatal("compaction activated ledger", e)
	}
}
