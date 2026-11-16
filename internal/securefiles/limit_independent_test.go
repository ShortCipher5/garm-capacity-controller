package securefiles

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestIndependentReadLimitBoundaries(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fixture")
	for _, size := range []int{0, 1, 2, 255, 256} {
		content := bytes.Repeat([]byte{0xa5}, size)
		if e := os.WriteFile(p, content, 0600); e != nil {
			t.Fatal(e)
		}
		for _, limit := range []int64{math.MinInt64, -1, 0, 1, 2, 255, 256, math.MaxInt64 - 1, math.MaxInt64} {
			got, e := Read(p, limit)
			allowed := limit > 0 && limit < math.MaxInt64 && int64(size) <= limit
			if allowed {
				if e != nil || !bytes.Equal(got, content) {
					t.Fatalf("valid read failed size=%d limit=%d", size, limit)
				}
			} else if e != ErrFile || got != nil {
				t.Fatalf("invalid read accepted size=%d limit=%d", size, limit)
			}
		}
		after, e := os.ReadFile(p)
		if e != nil || !bytes.Equal(after, content) {
			t.Fatal("source modified")
		}
	}
}
