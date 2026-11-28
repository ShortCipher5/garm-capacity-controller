// Fixed, unprivileged observation of the SSH target's OS; never a capacity grant.
package hostinventory

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

const HeadroomCommand = "garm-host-headroom-v1"

type cpuTimes [8]uint64

func readTimes(r io.Reader) (cpuTimes, bool) {
	var ticks cpuTimes
	line, e := bufio.NewReaderSize(r, 2048).ReadSlice('\n')
	if e != nil {
		return ticks, false
	}
	fields := strings.Fields(string(line))
	if len(fields) < 9 || fields[0] != "cpu" {
		return ticks, false
	}
	// guest and guest_nice are already charged to user/nice; do not add them.
	for i := range ticks {
		n, e := strconv.ParseUint(fields[i+1], 10, 64)
		if e != nil {
			return ticks, false
		}
		ticks[i] = n
	}
	return ticks, true
}
func osTimes() (cpuTimes, bool) {
	f, e := os.Open("/proc/stat")
	if e != nil {
		return cpuTimes{}, false
	}
	defer f.Close()
	return readTimes(f)
}
func idleBasisPoints(first, last cpuTimes) *uint64 {
	var sum uint64
	for i, v := range last {
		if v < first[i] {
			return nil
		}
		delta := v - first[i]
		if delta > math.MaxUint64-sum {
			return nil
		}
		sum += delta
	}
	idle := last[3] - first[3]
	if sum == 0 || idle > math.MaxUint64/10000 {
		return nil
	}
	n := idle * 10000 / sum
	return &n
}

// WriteHeadroom samples CPU idle time across a real interval, not load average
// or 100% minus guest CPU. iowait and steal are not treated as headroom.
func WriteHeadroom(w io.Writer) error {
	start := time.Now()
	onlineBefore, errBefore := readBounded("/sys/devices/system/cpu/online")
	first, ok := osTimes()
	time.Sleep(time.Second)
	last, ok2 := osTimes()
	onlineAfter, errAfter := readBounded("/sys/devices/system/cpu/online")
	window := time.Since(start).Milliseconds()
	var idle *uint64
	if ok && ok2 && errBefore == nil && errAfter == nil && string(onlineBefore) == string(onlineAfter) && CPUs(string(onlineBefore)) != nil && window >= 1000 && window <= 5000 {
		idle = idleBasisPoints(first, last)
	}
	v := Collect()
	_, err := fmt.Fprintf(w, "%s\n%d\n%d\n%s\n", HeadroomCommand, start.UTC().UnixMilli(), window, value(idle))
	if err != nil {
		return err
	}
	return Write(w, v)
}
