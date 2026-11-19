//go:build linux

package nativeplanning

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"testing"
)

func TestMixProposalRespectsConfiguredGuestCPUAndRAM(t *testing.T) {
	for _, v := range []struct{ cpu, ram int64 }{{1000, 2048}, {3999, 8191}, {8000, 16384}, {16000, 32768}, {1000000, 1000000}} {
		s := Selection{Binding: Binding{Host: "host"}, Partition: capsulepeer.PartitionSpec{Guests: nativeboundary.Limits{CPUMilli: v.cpu, MemoryBytes: v.ram*MiB + 123}}}
		p, e := proposeMix(s, "digest", 4096)
		if e != nil {
			t.Fatal(e)
		}
		var cpu, ram int64
		for _, entry := range p.Entries {
			cpu += entry.CPU * entry.Count
			ram += entry.RAM * entry.Count
			if entry.Disk != 4096 {
				t.Fatal("disk inferred")
			}
		}
		if cpu > v.cpu || ram > v.ram || p.GuestRAM != v.ram || p.DiskFitVerified || p.State != "proposed_from_saved_configuration_not_applied" {
			t.Fatal(p)
		}
	}
}
func TestMixProposalDoesNotInventMinimumCapacity(t *testing.T) {
	for _, v := range []struct{ cpu, ram int64 }{{999, 4096}, {1000, 2047}} {
		s := Selection{Partition: capsulepeer.PartitionSpec{Guests: nativeboundary.Limits{CPUMilli: v.cpu, MemoryBytes: v.ram * MiB}}}
		if _, e := proposeMix(s, "digest", 1); e == nil {
			t.Fatal("undersized selection accepted")
		}
	}
}
