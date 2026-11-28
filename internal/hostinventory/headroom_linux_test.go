package hostinventory

import (
	"math"
	"strings"
	"testing"
)

func TestHostCPUInterval(t *testing.T) {
	a, ok := readTimes(strings.NewReader("cpu  10 3 7 40 2 0 4 20 1 1\ncpu0 1 2\n"))
	if !ok {
		t.Fatal("valid first observation rejected")
	}
	b, ok := readTimes(strings.NewReader("cpu  15 3 7 50 5 0 4 22 2 2\n"))
	if !ok {
		t.Fatal("valid last observation rejected")
	}
	if n := idleBasisPoints(a, b); n == nil || *n != 5000 {
		t.Fatal("did not exclude guest double count/iowait/steal from idle", n)
	}
	if idleBasisPoints(a, a) != nil || idleBasisPoints(b, a) != nil || idleBasisPoints(cpuTimes{}, cpuTimes{math.MaxUint64, 1}) != nil {
		t.Fatal("zero/reset/overflow inferred a rate")
	}
	if n := idleBasisPoints(cpuTimes{}, cpuTimes{1}); n == nil || *n != 0 {
		t.Fatal("actual measured zero is not missing")
	}
	for _, v := range []string{"", "cpu0 1 2 3 4 5 6 7 8\n", "cpu 1 2 ? 4 5 6 7 8\n", strings.Repeat("1", 2049) + "\n"} {
		if _, ok := readTimes(strings.NewReader(v)); ok {
			t.Fatal("accepted incomplete/invalid stat")
		}
	}
}
