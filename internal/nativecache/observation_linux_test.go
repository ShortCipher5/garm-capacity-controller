//go:build linux

package nativecache

import (
	"context"
	"strings"
	"testing"
	"time"
)

func fixture() map[string]string {
	return map[string]string{"/proc/sys/kernel/random/boot_id": "12345678-1234-1234-1234-123456789abc\n", "/sys/module/zfs/version": "2.2.6-1\n", "/sys/module/zfs/parameters/zfs_arc_max": "0\n", "/proc/spl/kstat/zfs/arcstats": "13 1 0x01 4 192 123 456\nname type data\nc 4 100\nc_min 4 10\nc_max 4 200\nsize 4 250\n"}
}
func TestActualSampleNeverTurnsTargetIntoBound(t *testing.T) {
	f := fixture()
	o, e := observe(context.Background(), func(p string, _ int64) ([]byte, error) { return []byte(f[p]), nil })
	if e != nil || !o.Fresh() || o.Snapshot().SizeBytes <= o.Snapshot().TargetMaxBytes || o.Snapshot().State != Unqualified {
		t.Fatalf("%+v %v", o, e)
	}
	o.began = time.Now().Add(-6 * time.Second)
	if o.Fresh() || o.Snapshot().State != "" {
		t.Fatal("stale observation")
	}
}
func TestRefuseDriftAndMalformedSamples(t *testing.T) {
	for _, change := range []func(map[string]string){func(f map[string]string) { f["/sys/module/zfs/version"] = "2.3.0" }, func(f map[string]string) { f["/sys/module/zfs/parameters/zfs_arc_max"] = "-1" }, func(f map[string]string) { f["/proc/spl/kstat/zfs/arcstats"] += "size 4 1\n" }, func(f map[string]string) {
		f["/proc/spl/kstat/zfs/arcstats"] = strings.ReplaceAll(f["/proc/spl/kstat/zfs/arcstats"], "size 4 250", "size 4 18446744073709551616")
	}} {
		f := fixture()
		change(f)
		if _, e := observe(context.Background(), func(p string, _ int64) ([]byte, error) { return []byte(f[p]), nil }); e == nil {
			t.Fatal("bad sample accepted")
		}
	}
	f := fixture()
	n := 0
	if _, e := observe(context.Background(), func(p string, _ int64) ([]byte, error) {
		if p == "/sys/module/zfs/parameters/zfs_arc_max" {
			n++
			if n == 2 {
				return []byte("200"), nil
			}
		}
		return []byte(f[p]), nil
	}); e == nil {
		t.Fatal("changed config")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := observe(ctx, func(string, int64) ([]byte, error) { t.Fatal("read after cancel"); return nil, nil }); e == nil {
		t.Fatal("cancel accepted")
	}
}
func TestZeroAndNonKernelFilesRefused(t *testing.T) {
	if (Observation{}).Fresh() {
		t.Fatal("zero")
	}
	if _, e := kernelRead("/etc/passwd", 4096); e == nil {
		t.Fatal("nonkernel path")
	}
}
