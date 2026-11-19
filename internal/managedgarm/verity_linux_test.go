package managedgarm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerityCannotBeInventedForOrdinaryFiles(t *testing.T) {
	if _, err := measuredFileIdentity(nil); err == nil {
		t.Fatal("nil descriptor accepted")
	}
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte("ordinary unsealed config"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := measuredFileIdentity(f); err == nil {
		t.Fatal("ordinary bytes accepted as kernel sealed")
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "ordinary unsealed config" {
		t.Fatal("read-only measurement changed file", err)
	}
}
