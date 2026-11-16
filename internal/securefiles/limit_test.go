package securefiles

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRejectsOverflowingLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(p, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{math.MaxInt64, 0, -1} {
		b, e := Read(p, n)
		if e != ErrFile || b != nil {
			t.Fatal("invalid limit returned successful or partial data")
		}
	}
	for _, n := range []int64{9, 10, math.MaxInt64 - 1} {
		b, e := Read(p, n)
		if e != nil || string(b) != "synthetic" {
			t.Fatal("valid limit altered read semantics")
		}
	}
	if b, e := Read(p, 8); e != ErrFile || b != nil {
		t.Fatal("oversize data escaped")
	}
}
