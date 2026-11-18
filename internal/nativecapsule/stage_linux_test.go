//go:build linux

package nativecapsule

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"os"
	"strings"
	"testing"
)

func TestBootReleaseRequiresExactFreshDataset(t *testing.T) {
	v := nativeboundary.Limits{MemoryBytes: 100, CPUMilli: 100, Tasks: 100}
	b := bootSpec{capsulepeer.PartitionSpec{Bootstrap: v, Daemon: v, Monitors: v, Guests: v}, nativeboundary.Limits{MemoryBytes: 400, CPUMilli: 400, Tasks: 400}, []string{"1.1.1.1"}, 3, strings.Repeat("a", 64), "tank/root/domain-" + strings.Repeat("a", 64), "1", "2", "3", 1048576, strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)}
	if !validBoot(b) {
		t.Fatal("fixture")
	}
	for _, mutate := range []func(*bootSpec){func(v *bootSpec) { v.DNSResolvers = nil }, func(v *bootSpec) { v.DNSResolvers = []string{"127.0.0.1"} }, func(v *bootSpec) { v.Dataset = "tank/existing" }, func(v *bootSpec) { v.RuntimeGUID = v.DatasetGUID }, func(v *bootSpec) { v.QuotaBytes = 0 }, func(v *bootSpec) { v.DatasetGUID = "01" }, func(v *bootSpec) { v.Dataset = "tank/../domain-" + v.BirthID }} {
		v := b
		mutate(&v)
		if validBoot(v) {
			t.Fatal("invalid release")
		}
	}
}
func TestHostMapAndNonInitRefused(t *testing.T) {
	if nonHostMap([]byte("0 0 4294967295\n")) {
		t.Fatal("host mapping")
	}
	if os.Getpid() != 1 && RunStage() == nil {
		t.Fatal("noninit ran")
	}
}
