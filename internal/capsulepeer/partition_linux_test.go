//go:build linux

package capsulepeer

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"testing"
)

func TestPartitionSpecRejectsOverflowAndNonfiniteBudgets(t *testing.T) {
	v := nativeboundary.Limits{MemoryBytes: 100, CPUMilli: 100, Tasks: 100}
	p := nativeboundary.Limits{MemoryBytes: 400, CPUMilli: 400, Tasks: 400}
	s := PartitionSpec{v, v, v, v}
	if s.Validate(p) != nil {
		t.Fatal("exact finite sum")
	}
	for _, bad := range []PartitionSpec{{}, {v, v, v, nativeboundary.Limits{MemoryBytes: 101, CPUMilli: 100, Tasks: 100}}, {v, v, v, nativeboundary.Limits{MemoryBytes: 100, CPUMilli: 101, Tasks: 100}}, {v, v, v, nativeboundary.Limits{MemoryBytes: 100, CPUMilli: 100, Tasks: 101}}, {v, v, v, nativeboundary.Limits{MemoryBytes: 100, CPUMilli: 1<<63 - 1, Tasks: 100}}} {
		if bad.Validate(p) == nil {
			t.Fatal("unsafe budget", bad)
		}
	}
}
