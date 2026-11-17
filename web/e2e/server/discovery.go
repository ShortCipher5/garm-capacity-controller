package main

import (
	"context"
	"sync/atomic"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// browserDiscoverySource is a disposable read-only discovery fixture behind
// the real onboarding manager: the admin handler still runs the real audited
// Discover path, and the browser still receives the real result structs.
// Values stay fixed so Playwright assertions stay deterministic. It has no
// create, delete, pool edit, credential or binding operation.
type browserDiscoverySource struct{ calls atomic.Int32 }

func (b *browserDiscoverySource) PhysicalIdentity(context.Context) (incusapi.PhysicalServerIdentity, error) {
	b.calls.Add(1)
	return incusapi.PhysicalServerIdentity{}, nil
}
func (b *browserDiscoverySource) HostTotals(context.Context) (incusapi.HostTotals, error) {
	return incusapi.HostTotals{CPUMillisTotal: 8000, RAMMiBTotal: 32768, DiskMiBTotal: 524288, DiskMiBUsed: 131072}, nil
}
func (b *browserDiscoverySource) Guests(context.Context) ([]incusapi.Guest, error) {
	return []incusapi.Guest{
		{Name: "fixture-runner", Status: "Running", CPUMillis: 2000, RAMMiB: 4096, DiskMiBRoot: 20480, Controller: "fixture-controller", Pool: "pool-a", Reservation: "fixture-reservation"},
		{Name: "fixture-sidecar", Status: "Running", CPUMillis: 500, RAMMiB: 1024, DiskMiBRoot: 5120},
	}, nil
}
func (b *browserDiscoverySource) SampleUtilization(context.Context) (incusapi.UtilizationSample, error) {
	return incusapi.UtilizationSample{CPUUsedMillis: 750, RAMUsedMiB: 2560, DiskUsedMiB: 25600, Guests: 2}, nil
}
