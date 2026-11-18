package hostinventory

import (
	"bytes"
	"testing"
)

func TestCPUs(t *testing.T) {
	for raw, want := range map[string]uint64{"0": 1, "0-3": 4, "0-1,4,6-7\n": 5} {
		got := CPUs(raw)
		if got == nil || *got != want {
			t.Fatal(raw, got)
		}
	}
	for _, raw := range []string{"", "0-2,2-4", "3-1", "1,0", "-1", "1-2-3", "0,", "9999999999999"} {
		if CPUs(raw) != nil {
			t.Fatal(raw)
		}
	}
}
func TestMemory(t *testing.T) {
	a, b := memory([]byte("MemTotal: 8192 kB\nMemAvailable: 4096 kB\n"))
	if a == nil || b == nil || *a != 8192 || *b != 4096 {
		t.Fatal(a, b)
	}
	for _, raw := range []string{"MemTotal: 8192 kB\nMemTotal: 8192 kB", "MemTotal: 8192 B", "MemTotal: 8192 kB\nMemAvailable: 9999 kB"} {
		a, b = memory([]byte(raw))
		if a != nil || b != nil {
			t.Fatal(raw)
		}
	}
}
func TestOutputUnknownAndOverflow(t *testing.T) {
	var out bytes.Buffer
	if e := Write(&out, Values{}); e != nil {
		t.Fatal(e)
	}
	if out.String() != "garm_inventory_v1\ncpu_online=unknown\nram_kib=unknown\navailable_ram_kib=unknown\nroot_disk_kib=unknown\nroot_available_kib=unknown\n" {
		t.Fatal(out.String())
	}
	if diskKib(^uint64(0), 4096) != nil {
		t.Fatal("overflow")
	}
}
