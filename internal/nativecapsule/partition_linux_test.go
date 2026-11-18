//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedevice"
	"golang.org/x/sys/unix"
	"testing"
)

func TestBirthPartitionMissingAuthorityRefused(t *testing.T) {
	if _, e := preparePartition(t.TempDir(), "", capsulepeer.PartitionSpec{}, nativeboundary.Observation{}); e == nil {
		t.Fatal("unbound partition")
	}
	if _, e := StartWithPartition(context.Background(), Configuration{}, nativeboundary.Observation{}, nativedevice.Boundary{}, capsulepeer.PartitionSpec{}); e == nil {
		t.Fatal("unbounded birth")
	}
	var p *birthPartition
	if p.recheck(context.Background()) == nil {
		t.Fatal("fake proof")
	}
}
func TestPartitionCPUQuotaConversion(t *testing.T) {
	got := partitionFiles(nativeboundary.Limits{MemoryBytes: 1024, CPUMilli: 2500, Tasks: 10})
	if got["cpu.max"] != "250000 100000" || got["memory.swap.max"] != "0" || got["pids.max"] != "10" {
		t.Fatal(got)
	}
}

func TestPartitionCloseIsTerminalAcrossCopies(t *testing.T) {
	fds := []int{0, 0}
	if unix.Pipe2(fds, unix.O_CLOEXEC) != nil {
		t.Fatal("pipe")
	}
	defer unix.Close(fds[1])
	p := &birthPartition{root: fds[0], groups: map[string]int{}}
	s := Staged{partition: p}
	copy := s
	s.Close()
	if unix.Dup3(fds[1], fds[0], unix.O_CLOEXEC) != nil {
		t.Fatal("reuse")
	}
	defer unix.Close(fds[0])
	copy.Close()
	if _, e := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); e != nil {
		t.Fatal("closed reused descriptor", e)
	}
	if p.recheck(context.Background()) == nil {
		t.Fatal("closed partition revived")
	}
}
