//go:build linux

package nativecapsule

import "testing"

func TestActualCPUSetFormat(t *testing.T) {
	for s, want := range map[string]int64{"0": 1, "0-3": 4, "1-3,5,8-10": 7} {
		got, e := cpuSetCount(s)
		if e != nil || got != want {
			t.Fatal(s, got, e)
		}
	}
	for _, s := range []string{"", "max", "0,0", "1,0", "0-3,2-4", "1-1", "01", "0-1048576", "0,", "0\n", "-1", "1-2-3"} {
		if _, e := cpuSetCount(s); e == nil {
			t.Fatal(s)
		}
	}
}
